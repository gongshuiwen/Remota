import { act, fireEvent, render, screen, within } from '@testing-library/react';
import { beforeEach, expect, it, vi } from 'vitest';
import { StrictMode } from 'react';
import messages from '../../docs/contracts/acceptance/messages.json';
import type { CredentialIssue, Snapshot } from './api';
import { App } from './App';
import SecretDialog from './SecretDialog';

const fixture = messages.valid.find((value) => value.id === 'admin.snapshot.ready')!
  .value as unknown as Snapshot;
const issuedFixture = messages.valid.find((value) => value.id === 'credential.access.issue')!
  .value as unknown as CredentialIssue;
const issuedMcpFixture = messages.valid.find((value) => value.id === 'credential.issue')!
  .value as unknown as CredentialIssue;
const revokedFixture = messages.valid.find((value) => value.id === 'admin.revoke.response')!
  .value as unknown as Omit<CredentialIssue, 'secret'>;
const flush = () => act(async () => {});
function response(body: unknown, status = 200) {
  return new Response(status === 204 ? null : JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json', 'X-Remota-Epoch': fixture.server.server_epoch },
  });
}
function issueFor(
  operation: string,
  name = '新凭据',
  kind: 'access' | 'mcp' = 'access',
  allowExec = false,
) {
  const issue = structuredClone(kind === 'access' ? issuedFixture : issuedMcpFixture);
  issue.credential.id = `crd_${'c'.repeat(32)}`;
  issue.credential.creation_operation_id = operation;
  issue.credential.name = name;
  if (kind === 'mcp') issue.credential.allow_exec = allowExec;
  return issue;
}
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((value) => {
    resolve = value;
  });
  return { promise, resolve };
}
async function openCreate() {
  render(<App />);
  await flush();
  fireEvent.click(screen.getByRole('button', { name: /^凭据/ }));
  fireEvent.click(screen.getByRole('button', { name: '签发凭据' }));
  fireEvent.change(screen.getByLabelText('凭据名称'), { target: { value: '新凭据' } });
  return screen.getByRole('form', { name: '签发凭据' });
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date(fixture.server_time));
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible');
});

it('严格模式清理原生弹窗时的 close 事件不等于用户关闭秘密', async () => {
  const close = vi.fn();
  render(
    <StrictMode>
      <SecretDialog issue={issuedFixture} onClose={close} />
    </StrictMode>,
  );
  await flush();
  expect((screen.getByRole('dialog') as HTMLDialogElement).open).toBe(true);
  expect(close).not.toHaveBeenCalled();
});

it('MCP 默认不允许命令，切回 access 时不提交 allow_exec；关闭弹窗后清除秘密', async () => {
  const snapshot = structuredClone(fixture);
  let payload: Record<string, unknown> = {};
  let issued!: CredentialIssue;
  vi.stubGlobal(
    'fetch',
    vi.fn(async (path: string, options: RequestInit) => {
      if (path.endsWith('/credentials') && options.method === 'POST') {
        payload = JSON.parse(options.body as string) as Record<string, unknown>;
        issued = issueFor(payload.operation_id as string);
        snapshot.credentials.push(issued.credential);
        return response(issued, 201);
      }
      return response(snapshot);
    }),
  );
  const form = await openCreate();
  fireEvent.change(screen.getByLabelText('凭据类型'), { target: { value: 'mcp' } });
  const permission = screen.getByRole('checkbox') as HTMLInputElement;
  expect(permission.checked).toBe(false);
  fireEvent.click(permission);
  fireEvent.change(screen.getByLabelText('凭据类型'), { target: { value: 'access' } });
  fireEvent.change(screen.getByLabelText('有效期数量（留空使用服务默认值）'), {
    target: { value: '1' },
  });
  fireEvent.change(screen.getByLabelText('有效期单位'), { target: { value: '3600' } });
  await act(async () => {
    fireEvent.submit(form);
  });
  expect(payload).toMatchObject({ kind: 'access', name: '新凭据', expires_in_seconds: 3600 });
  expect(payload).not.toHaveProperty('allow_exec');
  expect(payload.operation_id).toMatch(/^[a-f0-9]{32}$/);
  const input = screen.getByLabelText('凭据秘密') as HTMLInputElement;
  expect(input.value === issued.secret).toBe(true);
  expect(input.type).toBe('password');
  fireEvent.click(screen.getByRole('button', { name: '显示秘密' }));
  expect(input.type).toBe('text');
  await act(async () => {
    (screen.getByRole('dialog') as HTMLDialogElement).close();
  });
  expect(screen.queryByRole('dialog')).toBeNull();
  expect(document.body.innerHTML.includes(issued.secret)).toBe(false);
  expect(localStorage.length + sessionStorage.length).toBe(0);
});

it.each([false, true])('MCP 命令申请权限严格采用本次选择：%s', async (allowExec) => {
  const snapshot = structuredClone(fixture);
  let submitted: boolean | undefined;
  vi.stubGlobal(
    'fetch',
    vi.fn(async (path: string, options: RequestInit) => {
      if (path.endsWith('/credentials') && options.method === 'POST') {
        const payload = JSON.parse(options.body as string) as {
          operation_id: string;
          allow_exec: boolean;
        };
        submitted = payload.allow_exec;
        const issue = issueFor(payload.operation_id, '新凭据', 'mcp', payload.allow_exec);
        snapshot.credentials.push(issue.credential);
        return response(issue, 201);
      }
      return response(snapshot);
    }),
  );
  const form = await openCreate();
  fireEvent.change(screen.getByLabelText('凭据类型'), { target: { value: 'mcp' } });
  if (allowExec) fireEvent.click(screen.getByRole('checkbox'));
  await act(async () => {
    fireEvent.submit(form);
  });
  expect(submitted).toBe(allowExec);
  expect(screen.getByRole('dialog')).toBeDefined();
});

it.each(['active', 'revoked'] as const)(
  '已有签发记录为 %s 时按关联 ID 核对并给出下一步，不重发或显示秘密',
  async (status) => {
    const snapshot = structuredClone(fixture);
    let writes = 0;
    vi.stubGlobal(
      'fetch',
      vi.fn(async (path: string, options: RequestInit) => {
        if (path.endsWith('/credentials') && options.method === 'POST') {
          writes++;
          const payload = JSON.parse(options.body as string) as { operation_id: string };
          const credential = issueFor(payload.operation_id).credential;
          if (status === 'revoked')
            Object.assign(credential, { status, revoked_at: fixture.server_time });
          snapshot.credentials.push(credential);
          return response(
            {
              error: {
                code: 'CREDENTIAL_OPERATION_EXISTS',
                message: 'CREDENTIAL_OPERATION_EXISTS',
                trace_id: 'test',
              },
            },
            409,
          );
        }
        return response(snapshot);
      }),
    );
    const form = await openCreate();
    await act(async () => {
      fireEvent.submit(form);
    });
    expect(screen.getByRole('alert').textContent).toContain('秘密无法再次取回');
    expect(screen.queryByRole('dialog')).toBeNull();
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: '核对最新快照' }));
    });
    expect(writes).toBe(1);
    if (status === 'revoked') {
      expect(screen.getByRole('alert').textContent).toContain('该记录已撤销');
      expect(screen.getByRole('alert').textContent).not.toContain('请先撤销');
      expect(screen.queryByRole('button', { name: '撤销此凭据' })).toBeNull();
    } else {
      fireEvent.click(screen.getByRole('button', { name: '撤销此凭据' }));
      expect(screen.getByRole('form', { name: '撤销凭据' })).toBeDefined();
      expect(screen.getByRole('button', { name: '确认撤销' })).toBeDefined();
    }
    expect(writes).toBe(1);
  },
);

it.each([false, true])('撤销应答丢失后核对实际提交状态：%s，不自动重发', async (committed) => {
  const snapshot = structuredClone(fixture);
  const credential = snapshot.credentials[0]!;
  const revoked = {
    ...revokedFixture,
    credential: { ...credential, status: 'revoked' as const, revoked_at: fixture.server_time },
  };
  let writes = 0;
  vi.stubGlobal(
    'fetch',
    vi.fn(async (path: string) => {
      if (path.endsWith(`/credentials/${credential.id}/revoke`)) {
        writes++;
        if (committed || writes > 1) Object.assign(credential, revoked.credential);
        if (writes === 1) throw new TypeError('offline');
        return response(revoked);
      }
      return response(snapshot);
    }),
  );
  render(<App />);
  await flush();
  fireEvent.click(screen.getByRole('button', { name: /^凭据/ }));
  fireEvent.click(
    within(screen.getByTitle(credential.id).closest('tr')!).getByRole('button', {
      name: '撤销凭据',
    }),
  );
  const form = screen.getByRole('form', { name: '撤销凭据' });
  await act(async () => {
    fireEvent.submit(form);
  });
  expect(screen.getByRole('alert').textContent).toContain('结果未知');
  await act(async () => {
    fireEvent.click(screen.getByRole('button', { name: '核对最新快照' }));
  });
  expect(writes).toBe(1);
  expect(screen.queryByRole('dialog')).toBeNull();
  if (committed) {
    expect(screen.getByRole('alert').textContent).toContain('最新快照显示凭据已撤销');
    expect(screen.queryByRole('button', { name: '重新确认撤销' })).toBeNull();
    expect(screen.queryByRole('button', { name: '确认撤销' })).toBeNull();
  } else {
    expect(screen.getByRole('alert').textContent).toContain('最新快照尚未显示撤销');
    fireEvent.click(screen.getByRole('button', { name: '重新确认撤销' }));
    expect(writes).toBe(1);
    await act(async () => {
      fireEvent.submit(form);
    });
    expect(screen.getByRole('status').textContent).toContain('凭据已撤销');
    expect(writes).toBe(2);
  }
});

it('旧登录的 201 签发应答不得显示秘密或改变新登录', async () => {
  const held = deferred<Response>();
  const csrf = 'B'.repeat(43);
  const next = structuredClone(fixture);
  next.csrf_token = csrf;
  let operation = '';
  const fetch = vi
    .fn()
    .mockResolvedValueOnce(response(fixture))
    .mockImplementationOnce((_path: string, options: RequestInit) => {
      operation = (JSON.parse(options.body as string) as { operation_id: string }).operation_id;
      return held.promise;
    })
    .mockResolvedValueOnce(response(null, 204))
    .mockResolvedValueOnce(
      response({ csrf_token: csrf, expires_at: next.admin_session.expires_at }),
    )
    .mockResolvedValueOnce(response(next));
  vi.stubGlobal('fetch', fetch);
  const form = await openCreate();
  fireEvent.submit(form);
  await act(async () => {
    fireEvent.click(screen.getByRole('button', { name: '退出登录' }));
  });
  fireEvent.change(screen.getByLabelText('管理员口令'), { target: { value: 'password' } });
  await act(async () => {
    fireEvent.submit(screen.getByLabelText('管理员口令').closest('form')!);
  });
  await act(async () => {
    held.resolve(response(issueFor(operation), 201));
  });
  expect(screen.getByRole('heading', { name: '设备会话' })).toBeDefined();
  expect(screen.queryByRole('dialog')).toBeNull();
  expect(fetch).toHaveBeenCalledTimes(5);
});

it('签发应答越过登录期限时，即使到期计时器尚未运行也不交付秘密', async () => {
  const snapshot = structuredClone(fixture);
  snapshot.admin_session.expires_at = new Date(Date.now() + 1000).toISOString();
  const held = deferred<Response>();
  let operation = '';
  const fetch = vi
    .fn()
    .mockResolvedValueOnce(response(snapshot))
    .mockImplementationOnce((_path: string, options: RequestInit) => {
      operation = (JSON.parse(options.body as string) as { operation_id: string }).operation_id;
      return held.promise;
    });
  vi.stubGlobal('fetch', fetch);
  const form = await openCreate();
  fireEvent.submit(form);
  vi.setSystemTime(Date.now() + 2000);
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden');
  await act(async () => {
    held.resolve(response(issueFor(operation), 201));
  });
  expect(screen.getByRole('heading', { name: '登录管理台' })).toBeDefined();
  expect(screen.queryByRole('dialog')).toBeNull();
  expect(fetch).toHaveBeenCalledTimes(2);
});

it('登录到期清除弹窗，旧复制回调不能更新后续登录', async () => {
  const snapshot = structuredClone(fixture);
  snapshot.admin_session.expires_at = new Date(Date.now() + 1000).toISOString();
  const copied = deferred<void>();
  const previous = Object.getOwnPropertyDescriptor(navigator, 'clipboard');
  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: { writeText: () => copied.promise },
  });
  try {
    let issued!: CredentialIssue;
    vi.stubGlobal(
      'fetch',
      vi.fn(async (path: string, options: RequestInit) => {
        if (path.endsWith('/credentials') && options.method === 'POST') {
          issued = issueFor(
            (JSON.parse(options.body as string) as { operation_id: string }).operation_id,
          );
          snapshot.credentials.push(issued.credential);
          return response(issued, 201);
        }
        if (path.endsWith('/login'))
          return response({
            csrf_token: fixture.csrf_token,
            expires_at: fixture.admin_session.expires_at,
          });
        return response(snapshot);
      }),
    );
    const form = await openCreate();
    await act(async () => {
      fireEvent.submit(form);
    });
    fireEvent.click(screen.getByRole('button', { name: '复制秘密' }));
    await act(() => vi.advanceTimersByTimeAsync(1000));
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(document.body.innerHTML.includes(issued.secret)).toBe(false);
    snapshot.admin_session.expires_at = fixture.admin_session.expires_at;
    fireEvent.change(screen.getByLabelText('管理员口令'), { target: { value: 'password' } });
    await act(async () => {
      fireEvent.submit(screen.getByLabelText('管理员口令').closest('form')!);
    });
    await act(async () => {
      copied.resolve();
    });
    expect(screen.getByRole('heading', { name: '设备会话' })).toBeDefined();
    expect(screen.queryByText('已复制秘密。')).toBeNull();
  } finally {
    if (previous) Object.defineProperty(navigator, 'clipboard', previous);
    else Reflect.deleteProperty(navigator, 'clipboard');
  }
});
