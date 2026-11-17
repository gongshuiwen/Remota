import { act, fireEvent, render, renderHook, screen } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import messages from '../../docs/contracts/acceptance/messages.json';
import type { Snapshot } from './api';
import { App } from './App';
import { useAdmin } from './useAdmin';

const fixture = messages.valid.find((value) => value.id === 'admin.snapshot.ready')!
  .value as unknown as Snapshot;
const csrfB = 'BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB';
function state(note = '原会话', csrf = fixture.csrf_token): Snapshot {
  const value = structuredClone(fixture);
  value.sessions[0]!.note = note;
  value.csrf_token = csrf;
  return value;
}
function response(body: unknown, status = 200) {
  return new Response(status === 204 ? null : JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json', 'X-Remota-Epoch': fixture.server.server_epoch },
  });
}
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: Error) => void;
  const promise = new Promise<T>((yes, no) => {
    resolve = yes;
    reject = no;
  });
  return { promise, resolve, reject };
}
const flush = () => act(async () => {});
const noteResponse = messages.valid.find((value) => value.id === 'admin.note.response')!.value;
const change = {
  kind: 'note',
  id: fixture.sessions[0]!.id,
  note: '草稿',
  expected_note_revision: 0,
} as const;
const failure = (code: string) => ({ error: { code, message: code, trace_id: 'test' } });

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date(fixture.server_time));
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible');
});

describe('管理员会话与快照', () => {
  it('最多一个刷新请求，隐藏时暂停，丢弃被取消的旧快照', async () => {
    const held = deferred<Response>();
    const next = deferred<Response>();
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(response(state()))
      .mockReturnValueOnce(held.promise)
      .mockReturnValueOnce(next.promise);
    vi.stubGlobal('fetch', fetch);
    const { result } = renderHook(useAdmin);
    await flush();
    await act(() => vi.advanceTimersByTimeAsync(15_000));
    expect(fetch).toHaveBeenCalledTimes(2);
    expect(result.current.view.kind).toBe('signedIn');
    vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden');
    fireEvent(document, new Event('visibilitychange'));
    expect(result.current.view.kind === 'signedIn' && result.current.view.refreshing).toBe(false);
    await act(() => vi.advanceTimersByTimeAsync(10_000));
    expect(fetch).toHaveBeenCalledTimes(2);
    vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible');
    fireEvent(document, new Event('visibilitychange'));
    await flush();
    await act(async () => {
      held.resolve(response(state('迟到的旧快照')));
    });
    expect(result.current.view.kind === 'signedIn' && result.current.view.refreshing).toBe(true);
    await act(async () => {
      next.resolve(response(state('新快照')));
    });
    expect(
      result.current.view.kind === 'signedIn' && result.current.view.snapshot.sessions[0]!.note,
    ).toBe('新快照');
  });

  it('失败保留旧快照，401 清理身份与数据', async () => {
    vi.stubGlobal(
      'fetch',
      vi
        .fn()
        .mockResolvedValueOnce(response(state()))
        .mockResolvedValueOnce(response({}, 503))
        .mockResolvedValueOnce(response({}, 401)),
    );
    const { result } = renderHook(useAdmin);
    await flush();
    await act(() => result.current.refresh());
    expect(
      result.current.view.kind === 'signedIn' && result.current.view.snapshot.sessions[0]!.note,
    ).toBe('原会话');
    expect(result.current.view.kind === 'signedIn' && result.current.view.error).toContain(
      '最近一次成功',
    );
    await act(() => result.current.refresh());
    expect(result.current.view).toEqual({
      kind: 'signedOut',
      pending: false,
      notice: '登录已失效，请重新登录。',
    });
  });

  it.each([200, 401])('旧代际的 %i 响应不能更新后续登录', async (status) => {
    const held = deferred<Response>();
    const logout = deferred<Response>();
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(response(state()))
      .mockReturnValueOnce(held.promise)
      .mockReturnValueOnce(logout.promise)
      .mockResolvedValueOnce(
        response({ csrf_token: csrfB, expires_at: fixture.admin_session.expires_at }),
      )
      .mockResolvedValueOnce(response(state('新登录', csrfB)));
    vi.stubGlobal('fetch', fetch);
    const { result } = renderHook(useAdmin);
    await flush();
    act(() => {
      void result.current.refresh();
    });
    act(() => {
      void result.current.logout();
    });
    expect(result.current.view).toEqual({ kind: 'signedOut', pending: true, notice: undefined });
    await act(() => result.current.login('still-pending'));
    expect(fetch).toHaveBeenCalledTimes(3);
    await act(async () => {
      logout.reject(new TypeError('offline'));
    });
    expect(result.current.view.kind === 'signedOut' && result.current.view.notice).toContain(
      '尚未取得服务端退出确认',
    );
    await act(() => result.current.login('new-password'));
    await act(async () => {
      held.resolve(response(status === 200 ? state('旧登录') : {}, status));
    });
    expect(
      result.current.view.kind === 'signedIn' && result.current.view.snapshot.sessions[0]!.note,
    ).toBe('新登录');
    expect(fetch.mock.calls[2]?.[1].headers['X-CSRF-Token']).toBe(fixture.csrf_token);
  });

  it('本机观察到登录到期时清除会话', async () => {
    const short = state();
    short.admin_session.expires_at = new Date(Date.now() + 1000).toISOString();
    vi.stubGlobal('fetch', vi.fn().mockResolvedValueOnce(response(short)));
    const { result } = renderHook(useAdmin);
    await flush();
    await act(() => vi.advanceTimersByTimeAsync(1000));
    expect(result.current.view.kind).toBe('signedOut');
  });

  it('CSRF 换代时清除会话', async () => {
    vi.stubGlobal(
      'fetch',
      vi
        .fn()
        .mockResolvedValueOnce(response(state()))
        .mockResolvedValueOnce(response(state('其他登录', csrfB))),
    );
    const { result } = renderHook(useAdmin);
    await flush();
    await act(() => result.current.refresh());
    expect(result.current.view.kind).toBe('signedOut');
  });

  it('非法 Schema 不替换最近一次成功的快照', async () => {
    vi.stubGlobal(
      'fetch',
      vi
        .fn()
        .mockResolvedValueOnce(response(state()))
        .mockResolvedValueOnce(response({ ...state('非法更新'), summary: {} })),
    );
    const { result } = renderHook(useAdmin);
    await flush();
    await act(() => result.current.refresh());
    expect(result.current.view.kind === 'signedIn' && result.current.view.error).toContain(
      '最近一次成功',
    );
    expect(
      result.current.view.kind === 'signedIn' && result.current.view.snapshot.sessions[0]!.note,
    ).toBe('原会话');
  });

  it('搜索仅过滤列表，统计仍来自完整快照', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response(state())));
    render(<App />);
    await flush();
    fireEvent.change(screen.getByRole('searchbox'), { target: { value: 'not-present' } });
    expect(screen.getByText('匹配 0 / 全部 1')).toBeDefined();
    expect(screen.getByText('有效设备会话').nextElementSibling?.textContent).toBe('1个');
  });

  it('退出清除列表与筛选，401 退出仍完成本地清理', async () => {
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(response(state()))
      .mockResolvedValueOnce(response({}, 401))
      .mockResolvedValueOnce(
        response({ csrf_token: csrfB, expires_at: fixture.admin_session.expires_at }),
      )
      .mockResolvedValueOnce(response(state('重新登录', csrfB)));
    vi.stubGlobal('fetch', fetch);
    render(<App />);
    await flush();
    fireEvent.change(screen.getByRole('searchbox'), { target: { value: '旧筛选' } });
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: '退出登录' }));
    });
    expect(screen.queryByRole('searchbox')).toBeNull();
    fireEvent.change(screen.getByLabelText('管理员口令'), { target: { value: 'password' } });
    await act(async () => {
      fireEvent.submit(screen.getByLabelText('管理员口令').closest('form')!);
    });
    expect((screen.getByRole('searchbox') as HTMLInputElement).value).toBe('');
    expect(screen.getByText('重新登录')).toBeDefined();
  });

  it('修改期间暂停刷新并拒绝重复提交，已取消的旧读不能覆盖修改后的快照', async () => {
    const oldRead = deferred<Response>();
    const write = deferred<Response>();
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(response(state()))
      .mockReturnValueOnce(oldRead.promise)
      .mockReturnValueOnce(write.promise)
      .mockResolvedValueOnce(response(state('草稿')));
    vi.stubGlobal('fetch', fetch);
    const { result } = renderHook(useAdmin);
    await flush();
    act(() => {
      void result.current.refresh();
    });
    let pending!: ReturnType<typeof result.current.mutate>;
    act(() => {
      pending = result.current.mutate(change);
    });
    await act(() => result.current.mutate(change));
    await act(() => vi.advanceTimersByTimeAsync(5000));
    expect(fetch).toHaveBeenCalledTimes(3);
    await act(async () => {
      oldRead.resolve(response(state('旧读')));
    });
    expect(
      result.current.view.kind === 'signedIn' && result.current.view.snapshot.sessions[0]!.note,
    ).toBe('原会话');
    await act(async () => {
      write.resolve(response(noteResponse));
      expect(await pending).toMatchObject({ kind: 'success' });
    });
    expect(
      result.current.view.kind === 'signedIn' && result.current.view.snapshot.sessions[0]!.note,
    ).toBe('草稿');
    expect(result.current.view.kind === 'signedIn' && result.current.view.mutating).toBe(false);
  });

  it('结果未知且核对失败时拒绝修改，显式刷新成功后才允许提交', async () => {
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(response(state()))
      .mockRejectedValueOnce(new TypeError('offline'))
      .mockResolvedValueOnce(response({}, 503))
      .mockResolvedValueOnce(response(state()))
      .mockResolvedValueOnce(response(noteResponse))
      .mockResolvedValueOnce(response(state('草稿')));
    vi.stubGlobal('fetch', fetch);
    const { result } = renderHook(useAdmin);
    await flush();
    await act(async () => {
      expect(await result.current.mutate(change)).toMatchObject({
        kind: 'unknown',
      });
    });
    expect(result.current.view.kind === 'signedIn' && result.current.view.needsCheck).toBe(true);
    await act(async () => {
      expect(await result.current.mutate(change)).toBeUndefined();
    });
    expect(fetch).toHaveBeenCalledTimes(3);
    await act(async () => {
      expect(await result.current.refresh()).toBe(true);
    });
    expect(result.current.view.kind === 'signedIn' && result.current.view.needsCheck).toBe(false);
    expect(fetch.mock.calls.map((call) => call[1].method)).toEqual(['GET', 'PATCH', 'GET', 'GET']);
    await act(async () => {
      expect(await result.current.mutate(change)).toMatchObject({ kind: 'success' });
    });
    expect(fetch.mock.calls.map((call) => call[1].method)).toEqual([
      'GET',
      'PATCH',
      'GET',
      'GET',
      'PATCH',
      'GET',
    ]);
  });

  it.each([200, 401])('旧登录的 %i 修改应答不能更新或注销新登录', async (status) => {
    const held = deferred<Response>();
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(response(state()))
      .mockReturnValueOnce(held.promise)
      .mockResolvedValueOnce(response(null, 204))
      .mockResolvedValueOnce(
        response({ csrf_token: csrfB, expires_at: fixture.admin_session.expires_at }),
      )
      .mockResolvedValueOnce(response(state('新登录', csrfB)));
    vi.stubGlobal('fetch', fetch);
    const { result } = renderHook(useAdmin);
    await flush();
    let pending!: ReturnType<typeof result.current.mutate>;
    act(() => {
      pending = result.current.mutate(change);
    });
    await act(() => result.current.logout());
    expect(fetch.mock.calls[1]![1].signal.aborted).toBe(true);
    await act(() => result.current.login('password'));
    await act(async () => {
      held.resolve(response(status === 200 ? noteResponse : {}, status));
      expect(await pending).toBeUndefined();
    });
    expect(fetch).toHaveBeenCalledTimes(5);
    expect(
      result.current.view.kind === 'signedIn' && result.current.view.snapshot.sessions[0]!.note,
    ).toBe('新登录');
  });

  it.each([
    [401, 'UNAUTHENTICATED'],
    [403, 'CSRF_INVALID'],
  ] as const)('修改遇到 %i / %s 时清除当前身份', async (status, code) => {
    vi.stubGlobal(
      'fetch',
      vi
        .fn()
        .mockResolvedValueOnce(response(state()))
        .mockResolvedValueOnce(response(failure(code), status)),
    );
    const { result } = renderHook(useAdmin);
    await flush();
    await act(() => result.current.mutate(change));
    expect(result.current.view.kind).toBe('signedOut');
  });

  it.each([
    [400, 'INVALID_ARGUMENT'],
    [403, 'ORIGIN_DENIED'],
  ] as const)('明确拒绝 %i / %s 时立即返回，不补读并释放修改锁', async (status, code) => {
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(response(state()))
      .mockResolvedValueOnce(response(failure(code), status));
    vi.stubGlobal('fetch', fetch);
    const { result } = renderHook(useAdmin);
    await flush();
    await act(async () => {
      expect(await result.current.mutate(change)).toMatchObject({ kind: 'failed' });
    });
    expect(fetch).toHaveBeenCalledTimes(2);
    expect(result.current.view.kind === 'signedIn' && result.current.view.mutating).toBe(false);
    expect(result.current.view.kind === 'signedIn' && result.current.view.needsCheck).toBe(false);
  });

  it('核对失败后自动刷新成功，面板同步显示已核对且不重发修改', async () => {
    const saved = state('草稿');
    saved.sessions[0]!.note_revision = 1;
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(response(state()))
      .mockRejectedValueOnce(new TypeError('offline'))
      .mockResolvedValueOnce(response({}, 503))
      .mockResolvedValueOnce(response(saved));
    vi.stubGlobal('fetch', fetch);
    render(<App />);
    await flush();
    fireEvent.click(screen.getByRole('button', { name: '编辑备注' }));
    fireEvent.change(screen.getByLabelText('备注草稿'), { target: { value: '草稿' } });
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: '保存备注' }));
    });
    expect(screen.getByText('尚未取得新的有效快照。')).toBeDefined();
    await act(() => vi.advanceTimersByTimeAsync(5000));
    expect(screen.getByRole('table').textContent).toContain('草稿');
    expect(screen.queryByText('尚未取得新的有效快照。')).toBeNull();
    expect(screen.getByText('已核对，当前备注与草稿一致。')).toBeDefined();
    expect(fetch.mock.calls.map((call) => call[1].method)).toEqual(['GET', 'PATCH', 'GET', 'GET']);
  });

  it('重新核对期间禁止采用修订，完成后采用最新修订并保留草稿', async () => {
    const current = state('其他备注');
    current.sessions[0]!.note_revision = 1;
    const latest = state('更新的备注');
    latest.sessions[0]!.note_revision = 2;
    const held = deferred<Response>();
    vi.stubGlobal(
      'fetch',
      vi
        .fn()
        .mockResolvedValueOnce(response(state()))
        .mockResolvedValueOnce(response(failure('REVISION_CONFLICT'), 409))
        .mockResolvedValueOnce(response(current))
        .mockReturnValueOnce(held.promise),
    );
    render(<App />);
    await flush();
    fireEvent.click(screen.getByRole('button', { name: '编辑备注' }));
    fireEvent.change(screen.getByLabelText('备注草稿'), { target: { value: '草稿' } });
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: '保存备注' }));
    });
    fireEvent.click(screen.getByRole('button', { name: '核对最新快照' }));
    const adopt = screen.getByRole('button', {
      name: '保留草稿并采用当前修订',
    }) as HTMLButtonElement;
    expect(adopt.disabled).toBe(true);
    await act(async () => {
      held.resolve(response(latest));
    });
    expect(adopt.disabled).toBe(false);
    fireEvent.click(adopt);
    expect(screen.getByText(/编辑基于修订 2/)).toBeDefined();
    expect((screen.getByLabelText('备注草稿') as HTMLInputElement).value).toBe('草稿');
  });

  it('自动刷新更新当前备注和修订，不覆盖正在编辑的草稿', async () => {
    const changed = state('其他备注');
    changed.sessions[0]!.note_revision = 1;
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValueOnce(response(state())).mockResolvedValueOnce(response(changed)),
    );
    render(<App />);
    await flush();
    fireEvent.click(screen.getByRole('button', { name: '编辑备注' }));
    fireEvent.change(screen.getByLabelText('备注草稿'), { target: { value: '草稿' } });
    await act(() => vi.advanceTimersByTimeAsync(5000));
    expect((screen.getByLabelText('备注草稿') as HTMLInputElement).value).toBe('草稿');
    expect(screen.getByRole('form', { name: '编辑备注' }).textContent).toContain(
      '当前修订 1 · 编辑基于修订 0',
    );
  });

  it('结束应答已确认时仍单独显示等待进程回收', async () => {
    const ended = structuredClone(
      messages.valid.find((value) => value.id === 'admin.end.session.response')!.value,
    ) as { session: { cleanup: 'pending' | 'confirmed' } };
    ended.session.cleanup = 'pending';
    const next = state();
    next.sessions = [{ ...next.sessions[0]!, ...ended.session, note: '', note_revision: 0 }];
    next.summary.live_sessions = 0;
    next.summary.cleanup_pending_sessions = 1;
    vi.stubGlobal(
      'fetch',
      vi
        .fn()
        .mockResolvedValueOnce(response(state()))
        .mockResolvedValueOnce(response(ended))
        .mockResolvedValueOnce(response(next)),
    );
    render(<App />);
    await flush();
    fireEvent.click(screen.getByRole('button', { name: '结束会话' }));
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: '确认结束' }));
    });
    expect(screen.getByRole('status').textContent).toContain('结束操作已确认');
    expect(screen.getByRole('row', { name: /已结束.*等待回收确认/ })).toBeDefined();
    expect(screen.queryByText('回收已确认')).toBeNull();
    expect(screen.queryByRole('button', { name: '结束会话' })).toBeNull();
  });
});
