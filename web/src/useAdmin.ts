import { useCallback, useEffect, useRef, useState } from 'react';
import { ApiError, readSnapshot, signIn, signOut } from './api';
import type { Snapshot } from './api';

type View =
  | { kind: 'checking' }
  | { kind: 'signedOut'; pending: boolean; error?: string; notice?: string }
  | {
      kind: 'signedIn';
      snapshot: Snapshot;
      receivedAt: number;
      deadline: number;
      refreshing: boolean;
      error?: string;
    };

export function useAdmin() {
  const [view, setView] = useState<View>({ kind: 'checking' });
  const generation = useRef(0);
  const identity = useRef<{ csrf: string; epoch?: string } | null>(null);
  const reading = useRef<AbortController | null>(null);
  const authenticating = useRef<AbortController | null>(null);

  const clear = useCallback((notice?: string, pending = false) => {
    generation.current++;
    reading.current?.abort();
    reading.current = null;
    identity.current = null;
    setView({ kind: 'signedOut', pending, notice });
    return generation.current;
  }, []);

  const refresh = useCallback(async () => {
    if (reading.current) return;
    const controller = new AbortController();
    reading.current = controller;
    const version = generation.current;
    const sentAt = Date.now();
    setView((current) =>
      current.kind === 'signedIn' ? { ...current, refreshing: true } : current,
    );
    try {
      const snapshot = await readSnapshot(controller.signal);
      if (version !== generation.current || controller.signal.aborted) return;
      const previous = identity.current;
      if (
        previous &&
        (previous.csrf !== snapshot.csrf_token ||
          (previous.epoch && previous.epoch !== snapshot.server.server_epoch))
      ) {
        clear('管理员登录或服务已变化，请重新登录。');
        return;
      }
      const deadline =
        sentAt + Date.parse(snapshot.admin_session.expires_at) - Date.parse(snapshot.server_time);
      if (deadline <= Date.now()) {
        clear('登录已到期，请重新登录。');
        return;
      }
      identity.current = { csrf: snapshot.csrf_token, epoch: snapshot.server.server_epoch };
      setView({
        kind: 'signedIn',
        snapshot,
        deadline,
        receivedAt: performance.now(),
        refreshing: false,
      });
    } catch (error) {
      if (version !== generation.current || controller.signal.aborted) return;
      if (error instanceof ApiError && error.status === 401) {
        clear(identity.current ? '登录已失效，请重新登录。' : undefined);
      } else {
        setView((current) =>
          current.kind === 'signedIn'
            ? { ...current, refreshing: false, error: '更新失败，以下仍为最近一次成功的快照。' }
            : {
                kind: 'signedOut',
                pending: false,
                error: '暂时无法读取管理快照，请检查服务后重试。',
              },
        );
      }
    } finally {
      if (reading.current === controller) reading.current = null;
    }
  }, [clear]);

  const login = async (password: string) => {
    if (authenticating.current) return;
    const version = clear(undefined, true);
    const controller = new AbortController();
    authenticating.current = controller;
    try {
      const csrf = await signIn(password, controller.signal);
      if (version !== generation.current || controller.signal.aborted) return;
      identity.current = { csrf };
      await refresh();
    } catch (error) {
      if (version !== generation.current || controller.signal.aborted) return;
      const status = error instanceof ApiError ? error.status : 0;
      setView({
        kind: 'signedOut',
        pending: false,
        error:
          status === 401
            ? '管理员口令不正确。'
            : status === 429
              ? '登录尝试过于频繁，请稍后重试。'
              : '登录未能确认，请检查连接后重试。',
      });
    } finally {
      if (authenticating.current === controller) authenticating.current = null;
    }
  };

  const logout = async () => {
    if (authenticating.current || !identity.current) return;
    const csrf = identity.current.csrf;
    const version = clear(undefined, true);
    const controller = new AbortController();
    authenticating.current = controller;
    let notice: string | undefined;
    try {
      await signOut(csrf, controller.signal);
    } catch (error) {
      if (!(error instanceof ApiError && error.status === 401))
        notice = '本页数据已清除，但尚未取得服务端退出确认。';
    } finally {
      if (authenticating.current === controller) authenticating.current = null;
      if (version === generation.current && !controller.signal.aborted)
        setView({ kind: 'signedOut', pending: false, notice });
    }
  };

  useEffect(() => {
    void refresh();
    return () => {
      generation.current++;
      reading.current?.abort();
      reading.current = null;
      authenticating.current?.abort();
      authenticating.current = null;
      identity.current = null;
    };
  }, [refresh]);

  useEffect(() => {
    if (view.kind !== 'signedIn') return;
    const version = generation.current;
    const timeout = setTimeout(
      () => {
        if (version === generation.current) clear('登录已到期，请重新登录。');
      },
      Math.max(0, view.deadline - Date.now()),
    );
    return () => clearTimeout(timeout);
  }, [view.kind, view.kind === 'signedIn' ? view.deadline : null, clear]);

  useEffect(() => {
    if (view.kind !== 'signedIn') return;
    const onVisibility = () => {
      if (document.visibilityState === 'hidden') {
        reading.current?.abort();
        reading.current = null;
        setView((current) =>
          current.kind === 'signedIn' && current.refreshing
            ? { ...current, refreshing: false }
            : current,
        );
      } else {
        void refresh();
      }
    };
    const interval = setInterval(() => {
      if (document.visibilityState !== 'hidden') void refresh();
    }, 5000);
    document.addEventListener('visibilitychange', onVisibility);
    return () => {
      clearInterval(interval);
      document.removeEventListener('visibilitychange', onVisibility);
    };
  }, [view.kind, refresh]);

  return { view, login, logout, refresh };
}
