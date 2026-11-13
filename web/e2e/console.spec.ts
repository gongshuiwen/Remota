import { expect, test as base } from '@playwright/test';
import type { APIRequestContext, Page } from '@playwright/test';
import { origin, startServer } from './server';

type Service = Awaited<ReturnType<typeof startServer>>;
const test = base.extend<{ service: Service }>({
  service: [
    async ({}, use) => {
      const service = await startServer();
      try {
        await use(service);
      } finally {
        await service.stop();
      }
    },
    { auto: true },
  ],
});

async function login(page: Page, password: string) {
  await page.getByLabel('管理员口令').fill(password);
  await page.getByRole('button', { name: '登录管理台', exact: true }).click();
  await expect(page.getByRole('heading', { name: '设备会话', exact: true })).toBeVisible();
}

async function registerSession(request: APIRequestContext, access: string) {
  const result = await request.post('/api/v1/agent/sessions', {
    headers: { Authorization: `Bearer ${access}` },
    data: {},
  });
  expect(result.status()).toBe(201);
  return result.json();
}

async function connectClient(request: APIRequestContext, mcp: string) {
  const mcpHeaders = {
    Authorization: `Bearer ${mcp}`,
    Accept: 'application/json, text/event-stream',
  };
  const initialized = await request.post('/mcp', {
    headers: mcpHeaders,
    data: {
      jsonrpc: '2.0',
      id: 'initialize',
      method: 'initialize',
      params: {
        protocolVersion: '2025-11-25',
        capabilities: {},
        clientInfo: { name: '验收 AI 客户端', version: '1' },
      },
    },
  });
  expect(initialized.status()).toBe(200);
  expect(
    (
      await request.post('/mcp', {
        headers: {
          ...mcpHeaders,
          'Mcp-Session-Id': initialized.headers()['mcp-session-id']!,
          'MCP-Protocol-Version': '2025-11-25',
        },
        data: { jsonrpc: '2.0', method: 'notifications/initialized' },
      })
    ).status(),
  ).toBe(202);
}

async function adminHeaders(request: APIRequestContext, password: string) {
  const auth = await request.post('/api/v1/admin/login', {
    headers: { Origin: origin },
    data: { password: password },
  });
  expect(auth.status()).toBe(200);
  const { csrf_token } = await auth.json();
  return { Origin: origin, 'X-CSRF-Token': csrf_token as string };
}

async function seedLists(request: APIRequestContext, service: Service) {
  const ids: string[] = [];
  for (let index = 0; index < 3; index++) {
    const registration = await registerSession(request, service.access);
    ids.push(registration.session.id as string);
    if (index > 0) {
      const headers = { Authorization: `Bearer ${registration.agent_token}` };
      expect(
        (
          await request.put(`/api/v1/agent/sessions/${registration.session.id}/decision`, {
            headers,
            data: { decision: 'accept', mode: 'readonly' },
          })
        ).status(),
      ).toBe(200);
      expect(
        (
          await request.post(`/api/v1/agent/sessions/${registration.session.id}/poll`, {
            headers,
            data: { consent_revision: 1, ack_event_ids: [] },
          })
        ).status(),
      ).toBe(200);
    }
  }
  await connectClient(request, service.mcp);
  const headers = await adminHeaders(request, service.password);
  const names = ['待接入的设备', '办公室工作站', '<img src=x onerror="window.compromised=true">'];
  for (const [index, id] of ids.entries()) {
    const response = await request.patch(`/api/v1/admin/sessions/${id}`, {
      headers,
      data: { note: names[index], expected_note_revision: 0 },
    });
    expect(response.status()).toBe(200);
  }
}

test.beforeEach(async ({ page }) => {
  await page.goto('/');
});

test('真实登录、只读列表、搜索与 Cookie/CSRF', async ({
  page,
  context,
  request,
  service,
}, testInfo) => {
  await seedLists(request, service);
  await expect(page.getByRole('heading', { name: '登录管理台', exact: true })).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath('login.png'), fullPage: true });
  await page.getByLabel('管理员口令').fill('wrong-password');
  await page.getByRole('button', { name: '登录管理台', exact: true }).click();
  await expect(page.getByRole('alert')).toHaveText('管理员口令不正确。');
  await expect(page.getByLabel('管理员口令')).toHaveValue('');
  await login(page, service.password);
  await expect(page.getByText('办公室工作站', { exact: true })).toBeVisible();
  await expect(
    page.getByText('<img src=x onerror="window.compromised=true">', { exact: true }),
  ).toBeVisible();
  expect(await page.evaluate(() => document.querySelector('img'))).toBeNull();
  const cookie = (await context.cookies()).find((value) => value.name === 'remota_admin');
  expect(cookie).toMatchObject({ httpOnly: true, sameSite: 'Strict', path: '/', secure: false });
  expect(await page.evaluate(() => document.cookie)).not.toContain('remota_admin');
  expect(
    await page.evaluate(
      async () =>
        (
          await fetch('/api/v1/admin/logout', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': 'wrong' },
            body: '{}',
          })
        ).status,
    ),
  ).toBe(403);
  await page.screenshot({ path: testInfo.outputPath('desktop.png'), fullPage: true });
  await page.getByRole('searchbox').fill('办公室');
  await expect(page.getByText('匹配 1 / 全部 3')).toBeVisible();
  await page.getByRole('button', { name: /^AI 连接/ }).click();
  await expect(page.getByRole('heading', { name: 'AI 连接', exact: true })).toBeVisible();
  await expect(page.getByText('验收 AI 客户端', { exact: true })).toBeVisible();
  await page.getByRole('button', { name: /^凭据/ }).click();
  await expect(page.getByText('设备接入', { exact: true })).toBeVisible();
  await page.getByLabel('凭据状态').selectOption('revoked');
  await expect(page.getByText('没有匹配的记录')).toBeVisible();
  await page.getByRole('button', { name: '清除筛选' }).click();
  await page.setViewportSize({ width: 390, height: 844 });
  await page.screenshot({ path: testInfo.outputPath('mobile.png'), fullPage: true });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
});

test('退出网络失败清空页面，重新登录清除旧筛选和浏览器存储', async ({ page, request, service }) => {
  await registerSession(request, service.access);
  await login(page, service.password);
  await expect(page.getByText('未命名设备', { exact: true })).toBeVisible();
  await page.getByRole('searchbox').fill('旧筛选');
  await page.route('**/api/v1/admin/logout', (route) => route.abort());
  await page.getByRole('button', { name: '退出登录' }).click();
  await expect(page.getByText('本页数据已清除，但尚未取得服务端退出确认。')).toBeVisible();
  await expect(page.getByRole('searchbox')).toHaveCount(0);
  await page.unroute('**/api/v1/admin/logout');
  await login(page, service.password);
  await expect(page.getByRole('searchbox')).toHaveValue('');
  expect(
    await page.evaluate(() => ({ local: localStorage.length, session: sessionStorage.length })),
  ).toEqual({ local: 0, session: 0 });
  expect(await page.content()).not.toContain(service.password);
});

test('浏览器重载恢复 Cookie，身份失效后清除数据', async ({ page, request, service }) => {
  await registerSession(request, service.access);
  await login(page, service.password);
  await page.reload();
  await expect(page.getByRole('heading', { name: '设备会话', exact: true })).toBeVisible();
  await expect(page.getByText('未命名设备', { exact: true })).toBeVisible();
  expect(
    await page.evaluate(async () => {
      const state = await (await fetch('/api/v1/admin/state')).json();
      return (
        await fetch('/api/v1/admin/logout', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': state.csrf_token },
          body: '{}',
        })
      ).status;
    }),
  ).toBe(204);
  await page.getByRole('button', { name: '刷新', exact: true }).click();
  await expect(page.getByRole('heading', { name: '登录管理台', exact: true })).toBeVisible();
  await expect(page.getByRole('searchbox')).toHaveCount(0);
});

test('备注冲突保留草稿，显式采用修订后保存', async ({ page, request, service }, testInfo) => {
  const registration = await registerSession(request, service.access);
  const id = registration.session.id as string;
  const headers = await adminHeaders(request, service.password);
  await login(page, service.password);
  await page.getByRole('button', { name: '编辑备注', exact: true }).click();
  await page.getByLabel('备注草稿').fill('本机保留的草稿');
  expect(
    (
      await request.patch(`/api/v1/admin/sessions/${id}`, {
        headers,
        data: { note: '并行修改的备注', expected_note_revision: 0 },
      })
    ).status(),
  ).toBe(200);
  await page.getByRole('button', { name: '刷新', exact: true }).click();
  await expect(page.getByRole('form', { name: '编辑备注' })).toContainText('并行修改的备注');
  await expect(page.getByLabel('备注草稿')).toHaveValue('本机保留的草稿');
  let writes = 0;
  page.on('request', (value) => {
    if (value.method() === 'PATCH') writes++;
  });
  await page.getByRole('button', { name: '保存备注', exact: true }).click();
  await expect(page.getByRole('alert')).toContainText('草稿已保留');
  expect(writes).toBe(1);
  await page.screenshot({ path: testInfo.outputPath('note-conflict.png'), fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.screenshot({ path: testInfo.outputPath('note-mobile.png'), fullPage: true });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.getByRole('button', { name: '保留草稿并采用当前修订' }).click();
  expect(writes).toBe(1);
  await page.getByRole('button', { name: '保存备注', exact: true }).click();
  await expect(page.getByRole('status')).toContainText('备注已保存');
  expect(writes).toBe(2);
  await page.getByRole('button', { name: '关闭', exact: true }).click();
  await page.setViewportSize({ width: 1440, height: 960 });
  await expect(page.getByRole('table')).toContainText('本机保留的草稿');
});

test('确认结束客户端与设备，分别展示状态并清除设备备注', async ({ page, request, service }) => {
  const registration = await registerSession(request, service.access);
  const id = registration.session.id as string;
  expect(
    (
      await request.put(`/api/v1/agent/sessions/${id}/decision`, {
        headers: { Authorization: `Bearer ${registration.agent_token}` },
        data: { decision: 'accept', mode: 'readonly' },
      })
    ).status(),
  ).toBe(200);
  await connectClient(request, service.mcp);
  const headers = await adminHeaders(request, service.password);
  expect(
    (
      await request.patch(`/api/v1/admin/sessions/${id}`, {
        headers,
        data: { note: '结束操作验收', expected_note_revision: 0 },
      })
    ).status(),
  ).toBe(200);
  await login(page, service.password);
  await expect(page.getByRole('table')).toContainText('结束操作验收');
  await page.getByRole('button', { name: /^AI 连接/ }).click();
  await page.getByRole('button', { name: '结束客户端', exact: true }).click();
  await page.getByRole('button', { name: '取消', exact: true }).click();
  await expect(page.getByRole('button', { name: '结束客户端', exact: true })).toBeFocused();
  await expect(page.getByRole('table')).toContainText('已就绪');
  await page.getByRole('button', { name: '结束客户端', exact: true }).click();
  await page.getByRole('button', { name: '确认结束', exact: true }).click();
  await expect(page.getByRole('status')).toContainText('结束操作已确认');
  await page.getByRole('button', { name: '关闭', exact: true }).click();
  await expect(page.getByRole('table')).toContainText('已结束');
  await expect(page.getByRole('button', { name: '结束客户端', exact: true })).toHaveCount(0);

  await page.getByRole('button', { name: /^设备会话/ }).click();
  await expect(page.getByRole('table')).toContainText('协助中');
  await page.getByRole('button', { name: '结束会话', exact: true }).click();
  await page.getByRole('button', { name: '确认结束', exact: true }).click();
  await expect(page.getByRole('status')).toContainText('结束操作已确认');
  await page.getByRole('button', { name: '关闭', exact: true }).click();
  await expect(page.getByRole('table')).toContainText('已结束');
  await expect(page.getByRole('table')).toContainText('无需回收');
  await expect(page.getByRole('button', { name: '编辑备注', exact: true })).toHaveCount(0);
  await expect(page.getByText('结束操作验收', { exact: true })).toHaveCount(0);
});

test('修改已处理但应答丢失时核对真实快照，不自动重发', async ({ page, request, service }) => {
  const registration = await registerSession(request, service.access);
  const id = registration.session.id as string;
  await login(page, service.password);
  let writes = 0;
  await page.route(`**/api/v1/admin/sessions/${id}{,/end}`, async (route) => {
    writes++;
    const response = await route.fetch();
    expect(response.status()).toBe(200);
    await route.abort();
  });
  await page.getByRole('button', { name: '编辑备注', exact: true }).click();
  await page.getByLabel('备注草稿').fill('已处理但应答丢失');
  await page.getByRole('button', { name: '保存备注', exact: true }).click();
  await expect(page.getByRole('alert')).toContainText('结果未知');
  await expect(page.getByRole('alert')).toContainText('当前备注与草稿一致');
  expect(writes).toBe(1);
  await expect(page.getByRole('button', { name: '保存备注', exact: true })).toHaveCount(0);
  await page.getByRole('button', { name: '关闭', exact: true }).click();
  await page.getByRole('button', { name: '结束会话', exact: true }).click();
  await page.getByRole('button', { name: '确认结束', exact: true }).click();
  await expect(page.getByRole('alert')).toContainText('结果未知');
  await expect(page.getByRole('alert')).toContainText('最新快照显示目标已结束');
  await expect(page.getByRole('table')).toContainText('已结束');
  expect(writes).toBe(2);
  await expect(page.getByRole('button', { name: '确认结束', exact: true })).toHaveCount(0);
});
