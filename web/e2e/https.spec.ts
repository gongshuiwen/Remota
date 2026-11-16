import { chromium, expect, test as base } from '@playwright/test';
import { login, openCredential, receiveSecret, snapshot } from './console';
import { startHTTPS } from './https';

process.env.PLAYWRIGHT_NO_COPY_PROMPT = '1';
type Products = Awaited<ReturnType<typeof startHTTPS>>;
const test = base.extend<{}, { products: Products }>({
  products: [
    async ({}, use) => {
      const products = await startHTTPS();
      try {
        await use(products);
      } finally {
        await products.stop();
      }
    },
    { scope: 'worker' },
  ],
  browser: async ({ products }, use) => {
    const browser = await chromium.connectOverCDP(products.cdp);
    try {
      await use(browser);
    } finally {
      await browser.close();
    }
  },
});
test.afterEach(({}, info) => {
  for (const error of info.errors) delete error.errorContext;
});

type Task = {
  id: string;
  phase: string;
  outcome?: string;
  details_available: boolean;
  execution: { state: string; exit_code?: number };
  result?: { stdout: { text: string }; stderr: { text: string } };
};
type Observation = { polls: number; processes_stopped: boolean; agent_exited: boolean };

test('真实产品经 HTTPS 完成本机确认、MCP 执行、跨租约与浏览器撤权回收', async ({
  page,
  context,
  products,
}) => {
  await test.step('证书验证和代理鉴权边界', async () => {
    for (const [url, error] of [
      [products.untrustedOrigin, /CERT_AUTHORITY_INVALID/],
      [products.origin.replace('127.0.0.1', 'localhost'), /CERT_COMMON_NAME_INVALID/],
    ] as const) {
      const probe = await context.newPage();
      try {
        await expect(probe.goto(url)).rejects.toThrow(error);
      } finally {
        await probe.close();
      }
    }
    await page.goto(products.origin);
    expect(await page.evaluate(() => window.isSecureContext)).toBe(true);
    await products.call({ action: 'security' });
    await login(page, products.password);
    const cookie = (await context.cookies()).find((value) => value.name === 'remota_admin');
    expect(
      cookie?.secure === true &&
        cookie.httpOnly &&
        cookie.sameSite === 'Strict' &&
        cookie.path === '/',
    ).toBe(true);
    const denied = await page.evaluate(async () => {
      const response = await fetch('/api/v1/admin/logout', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: '{}',
      });
      return { status: response.status, code: (await response.json()).error.code };
    });
    expect(denied).toEqual({ status: 403, code: 'CSRF_INVALID' });
  });

  const form = await openCredential(page, 'HTTPS device');
  await form.getByRole('button', { name: '签发凭据', exact: true }).click();
  const access = await receiveSecret(page);
  const mcpForm = await openCredential(page, 'HTTPS MCP', 'mcp');
  await mcpForm.getByRole('checkbox').check();
  await mcpForm.getByRole('button', { name: '签发凭据', exact: true }).click();
  const mcp = await receiveSecret(page);
  await products.call({ action: 'agent', token: access.secret, trusted: false });
  expect((await snapshot(page)).sessions.length).toBe(0);
  await products.call({ action: 'connect', token: mcp.secret });

  const sid = await test.step('本机 full 二次确认', async () => {
    await products.call({ action: 'agent', token: access.secret, trusted: true });
    await expect
      .poll(
        async () => (await snapshot(page)).sessions.some((value) => value.state === 'pending'),
        { timeout: 15_000 },
      )
      .toBe(true);
    const sid = (await snapshot(page)).sessions.find((value) => value.state === 'pending')!.id;
    await products.call({ action: 'local', input: 'f' });
    expect((await snapshot(page)).sessions.find((value) => value.id === sid)!.state).toBe(
      'pending',
    );
    await products.call({ action: 'local', input: 'full' });
    await expect
      .poll(
        async () => {
          const session = (await snapshot(page)).sessions.find((value) => value.id === sid);
          return session?.consent?.mode === 'full' && session.connection.activity === 'recent';
        },
        { timeout: 15_000 },
      )
      .toBe(true);
    await page.getByRole('button', { name: /^设备/ }).click();
    await page.getByRole('button', { name: '刷新', exact: true }).click();
    await expect(page.getByRole('row').filter({ hasText: sid })).toContainText('命令执行');
    return sid;
  });
  const task = async (sid: string, tid: string) =>
    (
      await products.call<{ task: Task }>({
        action: 'tool',
        name: 'get_task',
        arguments: { session_id: sid, task_id: tid },
      })
    ).task;
  const settled = async (sid: string, tid: string) => {
    await expect
      .poll(async () => (await task(sid, tid)).phase, { timeout: 15_000 })
      .toBe('settled');
    return task(sid, tid);
  };
  await test.step('固定诊断、真实双流输出与 MCP 取消', async () => {
    await products.call({ action: 'tool', name: 'list_sessions', arguments: {} });
    await products.call({ action: 'tool', name: 'get_session', arguments: { session_id: sid } });
    await products.call({ action: 'tool', name: 'list_tasks', arguments: { session_id: sid } });
    const ping = await products.call<{ pong: boolean }>({
      action: 'tool',
      name: 'ping_agent',
      arguments: { session_id: sid },
    });
    expect(ping.pong).toBe(true);
    const info = await products.call<{ os: string; supported_shells: string[] }>({
      action: 'tool',
      name: 'get_agent_info',
      arguments: { session_id: sid },
    });
    expect(info.os).toBe('linux');
    expect(info.supported_shells).toContain('sh');
    const short = await products.call<{ task: Task }>({
      action: 'command',
      session: sid,
      kind: 'short',
    });
    const result = await settled(sid, short.task.id);
    expect(result.outcome).toBe('succeeded');
    expect(result.execution).toMatchObject({ state: 'exited', exit_code: 0 });
    expect(result.result?.stdout.text).toBe('tls-out');
    expect(result.result?.stderr.text).toBe('tls-err');
    const long = await products.call<{ task: Task }>({
      action: 'command',
      session: sid,
      kind: 'long',
    });
    await products.call({
      action: 'tool',
      name: 'cancel_task',
      arguments: { session_id: sid, task_id: long.task.id },
    });
    expect((await settled(sid, long.task.id)).execution.state).toBe('exited');
    await expect
      .poll(async () => (await products.call<Observation>({ action: 'observe' })).processes_stopped)
      .toBe(true);
  });
  await test.step('实际跨过首个租约，浏览器撤销后核对真实进程回收', async () => {
    const before = await products.call<Observation>({ action: 'observe' });
    const long = await products.call<{ task: Task }>({
      action: 'command',
      session: sid,
      kind: 'long',
    });
    // The controller has confirmed that both command processes have started.
    const started = performance.now();
    // This is an elapsed-time lease observation, not a scheduling delay or fake clock.
    await expect
      .poll(
        async () => {
          expect((await task(sid, long.task.id)).phase).toBe('running');
          return performance.now() - started >= 33_000;
        },
        { timeout: 40_000, intervals: [500] },
      )
      .toBe(true);
    expect(
      (await products.call<Observation>({ action: 'observe' })).polls - before.polls,
    ).toBeGreaterThanOrEqual(2);
    await page.getByRole('button', { name: /^凭据/ }).click();
    await page.getByRole('button', { name: '刷新', exact: true }).click();
    const row = page.getByRole('row').filter({ hasText: access.id });
    await expect(row).toContainText('1 个未结算任务');
    await row.getByRole('button', { name: '撤销凭据', exact: true }).click();
    await page.getByRole('button', { name: '确认撤销', exact: true }).click();
    await expect(page.getByRole('status')).toContainText('凭据已撤销');
    const result = await settled(sid, long.task.id);
    expect(result.execution.state).toBe('exited');
    expect(result.details_available).toBe(false);
    await expect
      .poll(
        async () => {
          const state = await products.call<Observation>({ action: 'observe' });
          return state.processes_stopped && state.agent_exited;
        },
        { timeout: 15_000 },
      )
      .toBe(true);
    await expect
      .poll(async () => (await snapshot(page)).sessions.find((value) => value.id === sid)?.cleanup)
      .toBe('confirmed');
    await page.getByRole('button', { name: /^设备/ }).click();
    await page.getByRole('button', { name: '刷新', exact: true }).click();
    await expect(page.getByRole('row').filter({ hasText: sid })).toContainText('回收已确认');
  });
  await test.step('MCP 轮换收回旧身份，新凭据重新初始化', async () => {
    await page.getByRole('button', { name: /^凭据/ }).click();
    await page
      .getByRole('row')
      .filter({ hasText: mcp.id })
      .getByRole('button', { name: '轮换凭据', exact: true })
      .click();
    await page.getByRole('button', { name: '确认轮换', exact: true }).click();
    const rotated = await receiveSecret(page);
    await products.call({ action: 'revoked' });
    await products.call({ action: 'connect', token: rotated.secret });
    await products.call({ action: 'tool', name: 'list_sessions', arguments: {} });
    expect(
      (await snapshot(page)).credentials.find((value) => value.id === rotated.id)?.allow_exec,
    ).toBe(true);
  });
  await page.getByRole('button', { name: '退出登录', exact: true }).click();
  await expect(page.getByRole('heading', { name: '登录管理台', exact: true })).toBeVisible();
  expect(await page.evaluate(() => localStorage.length + sessionStorage.length)).toBe(0);
});
