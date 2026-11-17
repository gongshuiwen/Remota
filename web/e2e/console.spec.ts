import { expect, test as base } from '@playwright/test';
import type { APIRequestContext } from '@playwright/test';
import { origin, startServer } from './server';
import { login, openCredential, receiveSecret, snapshot } from './console';

// Failure snapshots include input values even when password-masked.
process.env.PLAYWRIGHT_NO_COPY_PROMPT = '1';

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

test.afterEach(({}, testInfo) => {
  // Locator assertions attach their own snapshots, separately from the fallback above.
  for (const error of testInfo.errors) delete error.errorContext;
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
  await expect(page.getByRole('table').getByText('设备接入', { exact: true })).toBeVisible();
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

test('access 签发、轮换和撤销形成真实鉴权闭环，秘密关闭后不保留', async ({
  page,
  request,
  service,
}, testInfo) => {
  const unrelated = await registerSession(request, service.access);
  await login(page, service.password);
  const form = await openCredential(page, '设备凭据验收');
  await form.getByLabel('有效期数量（留空使用服务默认值）').fill('1');
  await form.getByLabel('有效期单位').selectOption('3600');
  await form.getByRole('button', { name: '签发凭据', exact: true }).click();
  const dialog = page.getByRole('dialog', { name: '一次性秘密' });
  await expect(dialog).toBeVisible();
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.screenshot({
    path: testInfo.outputPath('credential-secret-mobile.png'),
    fullPage: true,
  });
  const original = await receiveSecret(page, true);
  await page.setViewportSize({ width: 1440, height: 960 });
  const first = await registerSession(request, original.secret);
  await page.reload();
  await expect(page.getByRole('heading', { name: '设备会话', exact: true })).toBeVisible();
  await expect(page.getByRole('dialog')).toHaveCount(0);
  await page.getByRole('button', { name: /^凭据/ }).click();
  await page
    .getByRole('row')
    .filter({ hasText: original.id })
    .getByRole('button', { name: '轮换凭据', exact: true })
    .click();
  await page.getByRole('button', { name: '确认轮换', exact: true }).click();
  const rotated = await receiveSecret(page);
  expect(rotated.secret !== original.secret).toBe(true);
  const next = await registerSession(request, rotated.secret);
  expect(
    (
      await request.post('/api/v1/agent/sessions', {
        headers: { Authorization: `Bearer ${original.secret}` },
        data: {},
      })
    ).status(),
  ).toBe(401);
  let state = await snapshot(page);
  expect(state.sessions.find((value) => value.id === first.session.id)?.state).toBe('ended');
  expect(state.sessions.find((value) => value.id === unrelated.session.id)?.state).toBe('pending');
  expect(state.credentials.find((value) => value.id === rotated.id)?.rotated_from_id).toBe(
    original.id,
  );
  await page
    .getByRole('row')
    .filter({ hasText: rotated.id })
    .getByRole('button', { name: '撤销凭据', exact: true })
    .click();
  await page.getByRole('button', { name: '确认撤销', exact: true }).click();
  await expect(page.getByRole('status')).toContainText('凭据已撤销');
  state = await snapshot(page);
  expect(state.sessions.find((value) => value.id === next.session.id)?.state).toBe('ended');
  expect(state.sessions.find((value) => value.id === unrelated.session.id)?.state).toBe('pending');
  expect(state.credentials.find((value) => value.id === rotated.id)?.status).toBe('revoked');
  expect(
    (
      await request.post('/api/v1/agent/sessions', {
        headers: { Authorization: `Bearer ${rotated.secret}` },
        data: {},
      })
    ).status(),
  ).toBe(401);
  expect(await page.evaluate(() => localStorage.length + sessionStorage.length)).toBe(0);
  expect(page.url().includes(rotated.secret)).toBe(false);
});

test('MCP 权限按明确选择签发并由轮换继承，撤销只影响关联客户端', async ({
  page,
  request,
  service,
}) => {
  const device = await registerSession(request, service.access);
  await login(page, service.password);
  const form = await openCredential(page, '命令申请凭据', 'mcp');
  await form.getByRole('checkbox').check();
  await form.getByRole('button', { name: '签发凭据', exact: true }).click();
  const original = await receiveSecret(page);
  await connectClient(request, original.secret);
  await page
    .getByRole('row')
    .filter({ hasText: original.id })
    .getByRole('button', { name: '轮换凭据', exact: true })
    .click();
  await page.getByRole('button', { name: '确认轮换', exact: true }).click();
  const rotated = await receiveSecret(page);
  let state = await snapshot(page);
  const meta = state.credentials.find((value) => value.id === rotated.id)!;
  expect(meta.kind).toBe('mcp');
  expect(meta.name).toBe('命令申请凭据');
  expect(meta.allow_exec).toBe(true);
  const originalClients = state.clients.filter((value) => value.credential_id === original.id);
  expect(originalClients).toHaveLength(1);
  expect(originalClients[0]!.lifecycle).toBe('ended');
  await connectClient(request, rotated.secret);
  await page
    .getByRole('row')
    .filter({ hasText: rotated.id })
    .getByRole('button', { name: '撤销凭据', exact: true })
    .click();
  await page.getByRole('button', { name: '确认撤销', exact: true }).click();
  await expect(page.getByRole('status')).toContainText('凭据已撤销');
  state = await snapshot(page);
  const rotatedClients = state.clients.filter((value) => value.credential_id === rotated.id);
  expect(rotatedClients).toHaveLength(1);
  expect(rotatedClients[0]!.lifecycle).toBe('ended');
  expect(state.sessions.find((value) => value.id === device.session.id)?.state).toBe('pending');
});

for (const action of ['create', 'rotate'] as const) {
  test(`${action} 签发已提交但应答丢失，按关联 ID 核对后显式撤销`, async ({ page, service }) => {
    await login(page, service.password);
    const initial = await snapshot(page);
    const source = initial.credentials.find((value) => value.kind === 'access')!;
    await page.getByRole('button', { name: /^凭据/ }).click();
    let path: string;
    if (action === 'create') {
      const form = await openCredential(page, '丢失秘密的凭据');
      path = '**/api/v1/admin/credentials';
      await expect(form).toBeVisible();
    } else {
      await page
        .getByRole('row')
        .filter({ hasText: source.id })
        .getByRole('button', { name: '轮换凭据', exact: true })
        .click();
      path = `**/api/v1/admin/credentials/${source.id}/rotate`;
    }
    let writes = 0;
    let operation = '';
    await page.route(path, async (route) => {
      writes++;
      operation = route.request().postDataJSON().operation_id as string;
      const response = await route.fetch();
      expect(response.status()).toBe(201);
      await route.abort();
    });
    const form = page.getByRole('form', { name: action === 'create' ? '签发凭据' : '轮换凭据' });
    await form
      .getByRole('button', { name: action === 'create' ? '签发凭据' : '确认轮换', exact: true })
      .click();
    await expect(page.getByRole('alert')).toContainText('秘密无法再次取回');
    await expect(page.getByRole('dialog')).toHaveCount(0);
    const state = await snapshot(page);
    const found = state.credentials.find((value) => value.creation_operation_id === operation)!;
    expect(found).toBeDefined();
    expect('secret' in found).toBe(false);
    expect(writes).toBe(1);
    if (action === 'rotate')
      expect(state.credentials.find((value) => value.id === source.id)?.status).toBe('revoked');
    await page.getByRole('button', { name: '撤销此凭据', exact: true }).click();
    await page.getByRole('button', { name: '确认撤销', exact: true }).click();
    await expect(page.getByRole('status')).toContainText('凭据已撤销');
    expect((await snapshot(page)).credentials.find((value) => value.id === found.id)?.status).toBe(
      'revoked',
    );
    expect(writes).toBe(1);
  });
}

test('中英文切换保留焦点、指引和一次性秘密，只持久保存语言偏好', async ({
  page,
  request,
  service,
}, testInfo) => {
  const registration = await registerSession(request, service.access);
  await login(page, service.password);
  const writes: string[] = [];
  page.on('request', (value) => {
    if (value.url().includes('/api/v1/admin/') && value.method() !== 'GET')
      writes.push(value.method());
  });
  await page.getByText('接入指引', { exact: true }).click();
  const guide = page.locator('details');
  await expect(guide.getByLabel('设备接入命令')).toHaveValue(`remota-agent --server '${origin}'`);
  expect(JSON.parse((await guide.getByLabel('MCP 配置示例').textContent())!)).toEqual({
    url: `${origin}/mcp`,
    headers: { Authorization: 'Bearer MCP_TOKEN' },
  });
  expect((await guide.textContent())!.includes(service.mcp)).toBe(false);
  await page.evaluate(() =>
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText: () => Promise.reject(new Error('denied')) },
    }),
  );
  const row = page.getByRole('row').filter({ hasText: registration.session.id });
  await row.getByRole('button', { name: '复制会话 ID' }).click();
  const fullID = row.getByLabel('完整会话 ID');
  await expect(fullID).toHaveValue(registration.session.id);
  await fullID.focus();
  await page.getByRole('button', { name: 'English', exact: true }).click();
  await expect(row.getByLabel('Full session ID')).toBeFocused();
  await expect(guide).toHaveAttribute('open', '');
  await expect(guide.getByLabel('Device connection command')).toHaveValue(
    `remota-agent --server '${origin}'`,
  );
  expect(writes).toHaveLength(0);
  await page.screenshot({ path: testInfo.outputPath('english-console.png'), fullPage: true });

  await page.getByRole('button', { name: /^Credentials/ }).click();
  await page.getByRole('button', { name: 'Issue credential', exact: true }).click();
  const form = page.getByRole('form');
  const name = form.getByLabel('Credential name');
  await name.fill('原始名称 / Credential');
  await page.getByRole('button', { name: '中文', exact: true }).click();
  await expect(form.getByLabel('凭据名称')).toBeFocused();
  await expect(form.getByLabel('凭据名称')).toHaveValue('原始名称 / Credential');
  await form.getByRole('button', { name: '签发凭据', exact: true }).click();
  const dialog = page.getByRole('dialog');
  await expect(dialog).toHaveAccessibleName('一次性秘密');
  const input = dialog.locator('#issued-secret');
  const secret = await input.inputValue();
  await input.focus();
  await input.evaluate((element: HTMLInputElement) => element.setSelectionRange(3, 7));
  await dialog.getByRole('button', { name: 'English', exact: true }).click();
  await expect(dialog).toHaveAccessibleName('One-time secret');
  await expect(input).toBeFocused();
  expect((await input.inputValue()) === secret).toBe(true);
  expect(
    await input.evaluate((element: HTMLInputElement) => [
      element.selectionStart,
      element.selectionEnd,
    ]),
  ).toEqual([3, 7]);
  await expect(input).toHaveAttribute('type', 'password');
  expect(writes).toHaveLength(1);
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.screenshot({ path: testInfo.outputPath('english-secret-mobile.png'), fullPage: true });
  await dialog.getByRole('button', { name: 'Saved, close', exact: true }).click();
  await expect(dialog).toHaveCount(0);
  expect((await page.content()).includes(secret)).toBe(false);
  expect(await page.evaluate(() => Object.keys(localStorage))).toEqual(['remota.language']);
  expect(await page.evaluate(() => sessionStorage.length)).toBe(0);
  await page.getByRole('button', { name: 'Sign out', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Sign in', exact: true })).toBeVisible();
  expect(await page.evaluate(() => localStorage.getItem('remota.language'))).toBe('en');
});
