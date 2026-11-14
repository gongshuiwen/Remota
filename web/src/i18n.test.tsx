import { act, fireEvent, render, screen, within } from '@testing-library/react';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import messages from '../../docs/contracts/acceptance/messages.json';
import type { CredentialIssue, Snapshot } from './api';
import { App } from './App';
import { languageKey } from './i18n';

const fixture = messages.valid.find((value) => value.id === 'admin.snapshot.ready')!
  .value as unknown as Snapshot;
const issuedFixture = messages.valid.find((value) => value.id === 'credential.issue')!
  .value as unknown as CredentialIssue;
const originalClipboard = Object.getOwnPropertyDescriptor(navigator, 'clipboard');
const flush = () => act(async () => {});
function response(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json', 'X-Remota-Epoch': fixture.server.server_epoch },
  });
}
function clipboard(writeText: (value: string) => Promise<void>) {
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });
}
function switchLanguage(scope: Pick<typeof screen, 'getByRole'> = screen) {
  // Playwright covers native pointer focus behavior.
  fireEvent.click(scope.getByRole('button', { name: /^(English|中文)$/ }));
}

beforeEach(() => {
  localStorage.removeItem(languageKey);
  vi.useFakeTimers();
  vi.setSystemTime(new Date(fixture.server_time));
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible');
});
afterEach(() => {
  localStorage.removeItem(languageKey);
  if (originalClipboard) Object.defineProperty(navigator, 'clipboard', originalClipboard);
  else Reflect.deleteProperty(navigator, 'clipboard');
});

it('切换保留登录输入和选择范围，只保存语言偏好，重新打开时恢复', async () => {
  const fetch = vi.fn(async () => response({}, 401));
  vi.stubGlobal('fetch', fetch);
  const view = render(<App />);
  await flush();
  const input = screen.getByLabelText('管理员口令') as HTMLInputElement;
  fireEvent.change(input, { target: { value: 'synthetic password' } });
  input.setSelectionRange(2, 6);
  switchLanguage();
  expect(screen.getByLabelText('Administrator password')).toBe(input);
  expect(input.value).toBe('synthetic password');
  expect([input.selectionStart, input.selectionEnd]).toEqual([2, 6]);
  expect(document.documentElement.lang).toBe('en');
  expect(fetch).toHaveBeenCalledTimes(1);
  expect(localStorage.getItem(languageKey)).toBe('en');
  expect(localStorage.length + sessionStorage.length).toBe(1);
  view.unmount();
  render(<App />);
  await flush();
  expect(screen.getByRole('heading', { name: 'Sign in' })).toBeDefined();
  expect((screen.getByLabelText('Administrator password') as HTMLInputElement).value).toBe('');
});

it.each(['invalid', 'blocked'] as const)(
  '语言偏好 %s 时仍可切换，不影响登录或发起修改',
  async (failure) => {
    localStorage.setItem(languageKey, 'fr');
    const storage =
      failure === 'blocked'
        ? vi.spyOn(window, 'localStorage', 'get').mockImplementation(() => {
            throw new DOMException('blocked', 'SecurityError');
          })
        : undefined;
    try {
      const fetch = vi.fn(async () => response({}, 401));
      vi.stubGlobal('fetch', fetch);
      render(<App />);
      await flush();
      expect(screen.getByRole('heading', { name: '登录管理台' })).toBeDefined();
      switchLanguage();
      expect(screen.getByRole('heading', { name: 'Sign in' })).toBeDefined();
      switchLanguage();
      expect(screen.getByRole('heading', { name: '登录管理台' })).toBeDefined();
      expect(fetch).toHaveBeenCalledTimes(1);
    } finally {
      storage?.mockRestore();
    }
  },
);

it('切换保留搜索和备注草稿，已有操作提示随当前语言展示，远端文本不翻译', async () => {
  const snapshot = structuredClone(fixture);
  snapshot.sessions[0]!.note = '远端备注：有效凭据';
  const fetch = vi.fn(async (_path: string, options: RequestInit) =>
    options.method === 'PATCH'
      ? response(
          { error: { code: 'INVALID_ARGUMENT', message: 'INVALID_ARGUMENT', trace_id: 'test' } },
          400,
        )
      : response(snapshot),
  );
  vi.stubGlobal('fetch', fetch);
  render(<App />);
  await flush();
  const search = screen.getByRole('searchbox');
  fireEvent.change(search, { target: { value: '远端备注' } });
  fireEvent.click(screen.getByRole('button', { name: '编辑备注' }));
  const draft = screen.getByLabelText('备注草稿') as HTMLInputElement;
  fireEvent.change(draft, { target: { value: '未提交的草稿' } });
  draft.setSelectionRange(1, 3);
  switchLanguage();
  expect(screen.getByLabelText('Note draft')).toBe(draft);
  expect([draft.selectionStart, draft.selectionEnd]).toEqual([1, 3]);
  expect(draft.value).toBe('未提交的草稿');
  expect((search as HTMLInputElement).value).toBe('远端备注');
  expect(screen.getAllByText('远端备注：有效凭据').length).toBeGreaterThan(0);
  expect(fetch).toHaveBeenCalledTimes(1);
  await act(async () => {
    fireEvent.click(screen.getByRole('button', { name: 'Save note' }));
  });
  expect(screen.getByRole('alert').textContent).toContain('Notes may contain up to 512 characters');
  switchLanguage();
  expect(screen.getByRole('alert').textContent).toContain('备注最多 512 个字符');
  expect(draft.value).toBe('未提交的草稿');
  expect(fetch).toHaveBeenCalledTimes(2);
});

it('切换保留凭据草稿、一次性秘密和原生弹窗，旧语言发起的复制回调使用当前语言', async () => {
  const snapshot = structuredClone(fixture);
  let rejectCopy!: (reason: Error) => void;
  const copying = new Promise<void>((_resolve, reject) => {
    rejectCopy = reject;
  });
  clipboard(() => copying);
  const fetch = vi.fn(async (path: string, options: RequestInit) => {
    if (path.endsWith('/credentials') && options.method === 'POST') {
      const input = JSON.parse(options.body as string) as {
        operation_id: string;
        name: string;
        allow_exec: boolean;
      };
      const issue = structuredClone(issuedFixture);
      Object.assign(issue.credential, {
        id: `crd_${'c'.repeat(32)}`,
        creation_operation_id: input.operation_id,
        name: input.name,
        allow_exec: input.allow_exec,
      });
      snapshot.credentials.push(issue.credential);
      return response(issue, 201);
    }
    return response(snapshot);
  });
  vi.stubGlobal('fetch', fetch);
  render(<App />);
  await flush();
  fireEvent.click(screen.getByRole('button', { name: /^凭据/ }));
  const filter = screen.getByLabelText('凭据状态') as HTMLSelectElement;
  fireEvent.change(filter, { target: { value: 'active' } });
  fireEvent.click(screen.getByRole('button', { name: '签发凭据' }));
  fireEvent.change(screen.getByLabelText('凭据类型'), { target: { value: 'mcp' } });
  const name = screen.getByLabelText('凭据名称') as HTMLInputElement;
  fireEvent.change(name, { target: { value: '原始名称：设备接入' } });
  fireEvent.click(screen.getByRole('checkbox'));
  fireEvent.change(screen.getByLabelText('有效期数量（留空使用服务默认值）'), {
    target: { value: '2' },
  });
  fireEvent.change(screen.getByLabelText('有效期单位'), { target: { value: '3600' } });
  switchLanguage();
  expect(screen.getByLabelText('Credential name')).toBe(name);
  expect(screen.getByLabelText('Credential status')).toBe(filter);
  expect(filter.value).toBe('active');
  expect((screen.getByLabelText('Credential type') as HTMLSelectElement).value).toBe('mcp');
  expect((screen.getByRole('checkbox') as HTMLInputElement).checked).toBe(true);
  expect(
    (
      screen.getByLabelText(
        'Lifetime amount (leave blank for the server default)',
      ) as HTMLInputElement
    ).value,
  ).toBe('2');
  expect((screen.getByLabelText('Lifetime unit') as HTMLSelectElement).value).toBe('3600');
  expect(fetch).toHaveBeenCalledTimes(1);
  await act(async () => {
    fireEvent.submit(screen.getByRole('form', { name: 'Issue credential' }));
  });
  const dialog = screen.getByRole('dialog', { name: 'One-time secret' });
  const input = within(dialog).getByLabelText('Credential secret') as HTMLInputElement;
  fireEvent.click(within(dialog).getByRole('button', { name: 'Reveal secret' }));
  fireEvent.click(within(dialog).getByRole('button', { name: 'Copy secret' }));
  const requests = fetch.mock.calls.length;
  switchLanguage(within(dialog));
  expect(screen.getByRole('dialog', { name: '一次性秘密' })).toBe(dialog);
  expect((dialog as HTMLDialogElement).open).toBe(true);
  expect(screen.getByLabelText('凭据秘密')).toBe(input);
  expect(input.value === issuedFixture.secret).toBe(true);
  expect(input.type).toBe('text');
  expect((screen.getByRole('button', { name: '正在复制…' }) as HTMLButtonElement).disabled).toBe(
    true,
  );
  await act(async () => {
    rejectCopy(new Error('denied'));
  });
  expect(within(dialog).getByRole('status').textContent).toContain('复制失败');
  expect(fetch).toHaveBeenCalledTimes(requests);
});

it.each([
  ['https://assist.example:8443', 'https', "remota-agent --server 'https://assist.example:8443'"],
  ['http://[::1]:8080', 'loopback_http', "remota-agent --server 'http://[::1]:8080'"],
  [
    'http://remote.example:8080',
    'insecure_http',
    "remota-agent --server 'http://remote.example:8080' --allow-insecure-http",
  ],
] as const)(
  '管理台接入示例使用快照中的公开地址和占位符：%s',
  async (publicURL, publicMode, command) => {
    const snapshot = structuredClone(fixture);
    snapshot.server.public_url = publicURL;
    snapshot.transport.public_mode = publicMode;
    expect(publicURL).not.toBe(window.location.origin);
    const fetch = vi.fn(async () => response(snapshot));
    vi.stubGlobal('fetch', fetch);
    render(<App />);
    await flush();
    const summary = screen.getByText('接入指引');
    fireEvent.click(summary);
    expect((summary.closest('details') as HTMLDetailsElement).open).toBe(true);
    expect((screen.getByLabelText('设备接入命令') as HTMLTextAreaElement).value).toBe(command);
    expect(JSON.parse(screen.getByLabelText('MCP 配置示例').textContent!)).toEqual({
      url: `${publicURL}/mcp`,
      headers: { Authorization: 'Bearer MCP_TOKEN' },
    });
    expect(fetch).toHaveBeenCalledTimes(1);
  },
);

it.each([
  undefined,
  'javascript:alert(1)',
  'https://user@assist.example',
  'https://assist.example/path',
  "https://assist'.example",
])('管理台公开地址缺失或不可用时提示配置，不回退浏览器地址：%s', async (publicURL) => {
  const snapshot = structuredClone(fixture);
  if (publicURL === undefined) delete snapshot.server.public_url;
  else snapshot.server.public_url = publicURL;
  snapshot.transport.public_mode = 'unconfigured';
  const fetch = vi.fn(async () => response(snapshot));
  vi.stubGlobal('fetch', fetch);
  render(<App />);
  await flush();
  fireEvent.click(screen.getByText('接入指引'));
  expect(screen.getByText(/请先使用 --public-url/)).toBeDefined();
  expect(screen.queryByLabelText('设备接入命令')).toBeNull();
  expect(screen.queryByLabelText('MCP 配置示例')).toBeNull();
  expect(fetch).toHaveBeenCalledTimes(1);
});

it.each([false, true])('复制完整会话 ID 只影响本行展示，失败时提供可选文本：%s', async (failed) => {
  const write = vi.fn(async () => {
    if (failed) throw new Error('denied');
  });
  clipboard(write);
  const fetch = vi.fn(async () => response(fixture));
  vi.stubGlobal('fetch', fetch);
  render(<App />);
  await flush();
  const id = fixture.sessions[0]!.id;
  const row = screen.getByTitle(id).closest('tr')!;
  await act(async () => {
    fireEvent.click(within(row).getByRole('button', { name: '复制会话 ID' }));
  });
  expect(write).toHaveBeenCalledExactlyOnceWith(id);
  if (failed) {
    const fallback = within(row).getByLabelText('完整会话 ID') as HTMLInputElement;
    fallback.focus();
    expect(fallback.value).toBe(id);
    expect(fallback.readOnly).toBe(true);
    expect([fallback.selectionStart, fallback.selectionEnd]).toEqual([0, id.length]);
  } else {
    expect(within(row).getByRole('button', { name: '已复制 ID' })).toBeDefined();
  }
  expect(screen.queryByRole('form')).toBeNull();
  expect(fetch).toHaveBeenCalledTimes(1);
});
