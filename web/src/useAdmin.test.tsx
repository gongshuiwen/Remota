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
});
