// @vitest-environment node
import { mkdtemp, readdir, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { expect, it, vi } from 'vitest';
import buildServer from './server';
import buildHTTPS from './https';

it.each([
  { name: 'HTTP', build: buildServer, key: 'REMOTA_E2E_BINARY', code: 'ENOENT' },
  { name: 'HTTPS', build: buildHTTPS, key: 'REMOTA_HTTPS_HELPER', code: 'ENOENT' },
])('$name 编译启动失败仍清理临时目录并保留原始错误', async ({ build, key, code }) => {
  const base = resolve(tmpdir());
  const directory = await mkdtemp(join(base, 'remota-browser-cleanup-'));
  try {
    for (const name of ['TEMP', 'TMP', 'TMPDIR']) vi.stubEnv(name, directory);
    vi.stubEnv('PATH', '');
    vi.stubEnv('REMOTA_HTTPS_CHROMIUM', '/browser-test-only');
    await expect(build()).rejects.toMatchObject({ code });
    expect(await readdir(directory)).toEqual([]);
    expect(process.env[key]).toBeUndefined();
  } finally {
    vi.unstubAllEnvs();
    if (dirname(directory) !== base) throw new Error('Unexpected test directory');
    await rm(directory, { recursive: true, force: true });
  }
});
