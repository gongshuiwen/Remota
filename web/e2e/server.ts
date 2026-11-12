import { execFile, spawn } from 'node:child_process';
import { once } from 'node:events';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { createInterface } from 'node:readline';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';

export const origin = 'http://127.0.0.1:4173';
export const target = 'http://127.0.0.1:18080';

const wsl = process.env.REMOTA_E2E_WSL_DISTRO
  ? ['--distribution', process.env.REMOTA_E2E_WSL_DISTRO]
  : [];

export default async function buildServer() {
  const base = resolve(tmpdir());
  const directory = await mkdtemp(join(base, 'remota-browser-'));
  const clean = async () => {
    delete process.env.REMOTA_E2E_BINARY;
    if (dirname(directory) !== base) throw new Error('Unexpected test directory');
    await rm(directory, { recursive: true, force: true });
  };
  try {
    const binary = join(directory, 'server.test');
    const run = promisify(execFile);
    await run('go', ['test', '-mod=readonly', '-c', '-o', binary, './tests/integration'], {
      cwd: fileURLToPath(new URL('../../', import.meta.url)),
      env: {
        ...process.env,
        GOOS: 'linux',
        GOARCH: process.arch === 'arm64' ? 'arm64' : 'amd64',
        CGO_ENABLED: '0',
      },
      windowsHide: true,
      timeout: 120_000,
    });
    process.env.REMOTA_E2E_BINARY =
      process.platform === 'win32'
        ? (
            await run('wsl.exe', [...wsl, '--exec', 'wslpath', '-u', binary], { windowsHide: true })
          ).stdout.trim()
        : binary;
    return clean;
  } catch (error) {
    try {
      await clean();
    } catch (cleanupError) {
      throw new AggregateError([error, cleanupError], 'Browser-test build and cleanup failed', {
        cause: error,
      });
    }
    throw error;
  }
}

export async function startServer() {
  const linuxBinary = process.env.REMOTA_E2E_BINARY;
  if (!linuxBinary) throw new Error('Browser-test service has not been built');
  const helper = [linuxBinary, '-test.run=^TestConsoleServer$', '-test.timeout=3m'];
  const child =
    process.platform === 'win32'
      ? spawn(
          'wsl.exe',
          [
            ...wsl,
            '--exec',
            'env',
            'REMOTA_CONSOLE_TEST=1',
            'REMOTA_CONSOLE_LISTEN=127.0.0.1:18080',
            `REMOTA_CONSOLE_PUBLIC_URL=${origin}`,
            ...helper,
          ],
          { windowsHide: true },
        )
      : spawn(linuxBinary, helper.slice(1), {
          env: {
            ...process.env,
            REMOTA_CONSOLE_TEST: '1',
            REMOTA_CONSOLE_LISTEN: '127.0.0.1:18080',
            REMOTA_CONSOLE_PUBLIC_URL: origin,
          },
        });
  const lines = createInterface({ input: child.stdout });
  // Each test owns a fresh service; bootstrap secrets stay in pipes and fixture memory.
  const stop = async () => {
    const exited =
      child.pid !== undefined && child.exitCode === null && child.signalCode === null
        ? once(child, 'exit')
        : Promise.resolve([child.exitCode]);
    child.stdin.end();
    const timeout = setTimeout(() => child.kill(), 10_000);
    try {
      const [code] = await exited;
      if (code !== 0) throw new Error('Browser-test service did not exit cleanly');
    } finally {
      clearTimeout(timeout);
      lines.close();
    }
  };
  let startupTimeout: ReturnType<typeof setTimeout> | undefined;
  try {
    const line = await Promise.race([
      once(lines, 'line').then(([value]) => value as string),
      once(child, 'exit').then(() => {
        throw new Error('Browser-test service did not start');
      }),
      new Promise<never>((_, reject) => {
        startupTimeout = setTimeout(
          () => reject(new Error('Browser-test service startup timed out')),
          20_000,
        );
      }),
    ]);
    let info: { password: string; access: string; mcp: string };
    try {
      info = JSON.parse(line) as typeof info;
    } catch {
      throw new Error('Browser-test service bootstrap failed');
    }
    if (
      typeof info.password !== 'string' ||
      typeof info.access !== 'string' ||
      typeof info.mcp !== 'string'
    )
      throw new Error('Invalid service bootstrap');
    return { ...info, stop };
  } catch (error) {
    try {
      await stop();
    } catch (cleanupError) {
      throw new AggregateError([error, cleanupError], 'Browser-test startup and cleanup failed', {
        cause: error,
      });
    }
    throw error;
  } finally {
    clearTimeout(startupTimeout);
  }
}
