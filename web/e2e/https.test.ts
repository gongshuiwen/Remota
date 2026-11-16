// @vitest-environment node
import { spawn } from 'node:child_process';
import type { ChildProcessWithoutNullStreams } from 'node:child_process';
import { once } from 'node:events';
import { afterEach, expect, it, vi } from 'vitest';
import { connectHTTPS } from './https';

const children: ChildProcessWithoutNullStreams[] = [];
const bootstrap = `console.log(JSON.stringify({origin:'https://example.invalid',password:'${'p'.repeat(43)}',cdp:'http://127.0.0.1:1',untrustedOrigin:'https://example.invalid'}));`;
const limits = { replyMS: 2_000, graceMS: 100, killMS: 100 };
function launch(script: string) {
  const child = spawn(process.execPath, ['-e', script], { windowsHide: true });
  children.push(child);
  return child;
}
afterEach(async () => {
  vi.restoreAllMocks();
  for (const child of children) {
    if (child.exitCode === null && child.signalCode === null) {
      const closed = once(child, 'close');
      child.kill('SIGKILL');
      await closed;
    }
  }
  children.length = 0;
});

it('启动断言失败保留阶段和诊断，报告中的测试秘密被脱敏', async () => {
  const secret = 'x'.repeat(43);
  const child = launch(`
    console.log(JSON.stringify({failure:'startup'}));
    console.log('https_linux_test.go:88: product startup failed ${secret}');
    process.exitCode = 1;
  `);
  const error = await connectHTTPS(child, limits).catch((error: Error) => error);
  expect(error).toBeInstanceOf(Error);
  const message = (error as Error).message;
  expect(message).toContain('startup');
  expect(message).toContain('https_linux_test.go:88: product startup failed');
  expect(message).toContain('[redacted]');
  expect(message.includes(secret)).toBe(false);
});

it('启动程序不存在时报告 ENOENT，清理不等待退出事件', async () => {
  const child = spawn('remota-nonexistent-https-test-helper', [], {
    env: { PATH: '' },
    windowsHide: true,
  });
  children.push(child);
  await expect(connectHTTPS(child, limits)).rejects.toThrow(/startup.*ENOENT/s);
});

it('命令期间控制器退出时报告当前操作和 stderr 原因', async () => {
  const products = await connectHTTPS(
    launch(`${bootstrap}
      process.stdin.once('data', () => {
        console.error('http_test.go:88: POST /mcp: HTTP 403, want 200');
        process.exitCode = 1;
        process.stdin.destroy();
      });
    `),
    limits,
  );
  await expect(products.call({ action: 'tool' })).rejects.toThrow(/tool.*HTTP 403/s);
  await expect(products.stop()).rejects.toThrow('shutdown');
});

it.each([false, true])(
  '退出超时尝试强制回收，kill 无效=%s 时仍在最终截止返回',
  async (killFails) => {
    const child = launch(`${bootstrap}
    process.stdin.resume();
    process.stdin.on('end', () => {});
    setInterval(() => {}, 1000);
  `);
    const products = await connectHTTPS(child, limits);
    const kill = vi.spyOn(child, 'kill');
    if (killFails) kill.mockReturnValue(false);
    await expect(products.stop()).rejects.toThrow(
      killFails ? 'shutdown: timed out' : 'shutdown: required forced cleanup',
    );
    expect(kill).toHaveBeenCalledWith('SIGKILL');
    if (!killFails) expect(child.exitCode !== null || child.signalCode !== null).toBe(true);
  },
);
