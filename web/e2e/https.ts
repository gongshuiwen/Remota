import { execFile, spawn } from 'node:child_process';
import type { ChildProcessWithoutNullStreams } from 'node:child_process';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { createInterface } from 'node:readline';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';

const run = promisify(execFile);
const root = fileURLToPath(new URL('../../', import.meta.url));
const wsl = process.env.REMOTA_E2E_WSL_DISTRO
  ? ['--distribution', process.env.REMOTA_E2E_WSL_DISTRO]
  : [];
const linuxPath = async (path: string) =>
  process.platform === 'win32' && /^[a-z]:/i.test(path)
    ? (
        await run('wsl.exe', [...wsl, '--exec', 'wslpath', '-u', path], { windowsHide: true })
      ).stdout.trim()
    : path;

export default async function buildHTTPS() {
  const browser = process.env.REMOTA_HTTPS_CHROMIUM;
  if (!browser) throw new Error('Set REMOTA_HTTPS_CHROMIUM to a Linux Chromium executable');
  const base = resolve(tmpdir());
  const directory = await mkdtemp(join(base, 'remota-https-'));
  const clean = async () => {
    delete process.env.REMOTA_HTTPS_HELPER;
    if (dirname(directory) !== base) throw new Error('Unexpected HTTPS build directory');
    await rm(directory, { recursive: true, force: true });
  };
  try {
    const arch = process.arch === 'arm64' ? 'arm64' : 'amd64';
    const env = { ...process.env, GOOS: 'linux', GOARCH: arch, CGO_ENABLED: '0' };
    const helper = join(directory, 'https.test');
    await run('go', ['test', '-mod=readonly', '-c', '-o', helper, './tests/integration'], {
      cwd: root,
      env,
      windowsHide: true,
      timeout: 120_000,
    });
    process.env.REMOTA_HTTPS_HELPER = await linuxPath(helper);
    const binaries = process.env.REMOTA_BINARY_DIR ?? join(root, 'dist', `linux-${arch}`);
    if (!binaries.trim()) throw new Error('REMOTA_BINARY_DIR must identify the binaries to accept');
    process.env.REMOTA_HTTPS_BINARIES = await linuxPath(binaries);
    process.env.REMOTA_HTTPS_CHROMIUM = await linuxPath(browser);
    return clean;
  } catch (error) {
    try {
      await clean();
    } catch (cleanupError) {
      throw new AggregateError([error, cleanupError], 'HTTPS build and cleanup failed', {
        cause: error,
      });
    }
    throw error;
  }
}

type Info = { origin: string; password: string; cdp: string; untrustedOrigin: string };
type Command = {
  action: string;
  token?: string;
  input?: string;
  name?: string;
  session?: string;
  kind?: string;
  trusted?: boolean;
  arguments?: Record<string, unknown>;
};

export async function startHTTPS() {
  const helper = process.env.REMOTA_HTTPS_HELPER;
  if (!helper) throw new Error('HTTPS product helper has not been built');
  const args = [
    'REMOTA_HTTPS_TEST=1',
    `REMOTA_HTTPS_BINARIES=${process.env.REMOTA_HTTPS_BINARIES}`,
    `REMOTA_HTTPS_CHROMIUM=${process.env.REMOTA_HTTPS_CHROMIUM}`,
    helper,
    '-test.run=^TestHTTPSProducts$',
    '-test.timeout=3m',
  ];
  const child =
    process.platform === 'win32'
      ? spawn('wsl.exe', [...wsl, '--exec', 'env', ...args], { cwd: root, windowsHide: true })
      : spawn('env', args, { cwd: root, windowsHide: true });
  return connectHTTPS(child);
}

// Keep the private controller transport independently testable with owned child processes.
export async function connectHTTPS(
  child: ChildProcessWithoutNullStreams,
  { replyMS = 30_000, graceMS = 15_000, killMS = 5_000 } = {},
) {
  let diagnostic = '';
  const remember = (line: string) => {
    // Test credentials are base64url values of at least 43 characters. Redact before truncation.
    diagnostic = (diagnostic + '\n' + line.replace(/[A-Za-z0-9_-]{43,}/g, '[redacted]')).slice(
      -4096,
    );
  };
  const lines = createInterface({ input: child.stdout });
  const errors = createInterface({ input: child.stderr });
  errors.on('line', remember);
  lines.on('line', (line) => {
    if (!line.startsWith('{')) remember(line);
  });
  const replies = lines[Symbol.asyncIterator]();
  child.on('error', (error: NodeJS.ErrnoException) => remember(error.code ?? 'spawn failed'));
  child.stdin.on('error', () => {}); // The response/close path reports a broken private pipe.
  const closed = new Promise<number | null>((resolve) => child.once('close', resolve));
  const failure = (stage: string, reason: string) =>
    new Error(`HTTPS controller ${stage}: ${reason}${diagnostic}`);
  async function deadline<T>(promise: Promise<T>, milliseconds: number, stage: string) {
    let timer: ReturnType<typeof setTimeout> | undefined;
    try {
      return await Promise.race([
        promise,
        new Promise<never>((_, reject) => {
          timer = setTimeout(() => reject(failure(stage, 'timed out')), milliseconds);
        }),
      ]);
    } finally {
      clearTimeout(timer);
    }
  }
  async function receive<T>(stage: string) {
    return deadline(
      (async () => {
        const { value, done } = await replies.next();
        if (done) {
          await closed;
          throw failure(stage, 'helper exited before replying');
        }
        let reply;
        try {
          reply = JSON.parse(value);
        } catch {
          await closed;
          throw failure(stage, 'invalid helper response');
        }
        if (reply?.failure) {
          await closed; // Include the diagnostic report emitted after the structured failure.
          throw failure(stage, 'helper assertion failed');
        }
        return reply as T;
      })(),
      replyMS,
      stage,
    );
  }
  async function stop() {
    child.stdin.end();
    let forced = false;
    const timer = setTimeout(() => {
      forced = true;
      child.kill('SIGKILL');
    }, graceMS);
    try {
      const code = await deadline(closed, graceMS + killMS, 'shutdown');
      if (forced || code !== 0)
        throw failure('shutdown', forced ? 'required forced cleanup' : 'helper failed');
    } finally {
      clearTimeout(timer);
      lines.close();
      errors.close();
    }
  }
  try {
    const info = await receive<Info>('startup');
    if (
      !info ||
      !['origin', 'password', 'cdp', 'untrustedOrigin'].every(
        (key) => typeof info[key as keyof Info] === 'string',
      )
    )
      throw failure('startup', 'invalid bootstrap');
    return {
      ...info,
      stop,
      call: async <T = Record<string, unknown>>(command: Command) => {
        const reply = receive<T>(command.action);
        child.stdin.write(JSON.stringify(command) + '\n');
        return reply;
      },
    };
  } catch (error) {
    try {
      await stop();
    } catch (cleanupError) {
      throw new AggregateError([error, cleanupError], (error as Error).message, {
        cause: error,
      });
    }
    throw error;
  }
}
