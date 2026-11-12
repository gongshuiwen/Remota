// @vitest-environment node
import { mkdtemp, readdir, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { expect, it, vi } from 'vitest';
import buildServer from './server';

it('编译启动失败仍清理临时目录并保留原始错误', async () => {
  const base = resolve(tmpdir());
  const directory = await mkdtemp(join(base, 'remota-browser-cleanup-'));
  try {
    for (const name of ['TEMP', 'TMP', 'TMPDIR']) vi.stubEnv(name, directory);
    vi.stubEnv('PATH', '');
    await expect(buildServer()).rejects.toMatchObject({ code: 'ENOENT' });
    expect(await readdir(directory)).toEqual([]);
    expect(process.env.REMOTA_E2E_BINARY).toBeUndefined();
  } finally {
    vi.unstubAllEnvs();
    if (dirname(directory) !== base) throw new Error('Unexpected test directory');
    await rm(directory, { recursive: true, force: true });
  }
});
