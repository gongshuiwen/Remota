import { useCallback, useEffect, useRef, useState } from 'react';
import {
  ApiError,
  changeCredential,
  changeResource,
  isCredentialChange,
  readSnapshot,
  signIn,
  signOut,
} from './api';
import type { AdminChange, CredentialIssue, Snapshot } from './api';

export interface ChangeResult {
  kind: 'success' | 'conflict' | 'failed' | 'unknown';
  message: string;
  issue?: CredentialIssue;
}

type View =
  | { kind: 'checking' }
  | { kind: 'signedOut'; pending: boolean; error?: string; notice?: string }
  | {
      kind: 'signedIn';
      snapshot: Snapshot;
      receivedAt: number;
      deadline: number;
      refreshing: boolean;
      mutating: boolean;
      needsCheck: boolean;
      error?: string;
    };

export function useAdmin() {
  const [view, setView] = useState<View>({ kind: 'checking' });
  const generation = useRef(0);
  const identity = useRef<{ csrf: string; epoch?: string } | null>(null);
  const reading = useRef<AbortController | null>(null);
  const authenticating = useRef<AbortController | null>(null);
  const writing = useRef<AbortController | null>(null);
  const needsCheck = useRef(false);

  const clear = useCallback((notice?: string, pending = false) => {
    generation.current++;
    reading.current?.abort();
    reading.current = null;
    writing.current?.abort();
    writing.current = null;
    needsCheck.current = false;
    identity.current = null;
    setView({ kind: 'signedOut', pending, notice });
    return generation.current;
  }, []);

  const refresh = useCallback(
    async (afterWrite = false): Promise<boolean> => {
      if (reading.current || (writing.current && !afterWrite)) return false;
      const controller = new AbortController();
      reading.current = controller;
      const version = generation.current;
      const sentAt = Date.now();
      setView((current) =>
        current.kind === 'signedIn' ? { ...current, refreshing: true } : current,
      );
      try {
        const snapshot = await readSnapshot(controller.signal);
        if (version !== generation.current || controller.signal.aborted) return false;
        const previous = identity.current;
        if (
          previous &&
          (previous.csrf !== snapshot.csrf_token ||
            (previous.epoch && previous.epoch !== snapshot.server.server_epoch))
        ) {
          clear('管理员登录或服务已变化，请重新登录。');
          return false;
        }
        const deadline =
          sentAt + Date.parse(snapshot.admin_session.expires_at) - Date.parse(snapshot.server_time);
        if (deadline <= Date.now()) {
          clear('登录已到期，请重新登录。');
          return false;
        }
        identity.current = { csrf: snapshot.csrf_token, epoch: snapshot.server.server_epoch };
        needsCheck.current = false;
        setView({
          kind: 'signedIn',
          snapshot,
          deadline,
          receivedAt: performance.now(),
          refreshing: false,
          mutating: writing.current !== null,
          needsCheck: false,
        });
        return true;
      } catch (error) {
        if (version !== generation.current || controller.signal.aborted) return false;
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
      return false;
    },
    [clear],
  );

  const mutate = async (change: AdminChange): Promise<ChangeResult | undefined> => {
    const current = identity.current;
    if (!current?.epoch || authenticating.current || writing.current || needsCheck.current) return;
    const version = generation.current;
    const controller = new AbortController();
    writing.current = controller;
    reading.current?.abort();
    reading.current = null;
    setView((value) =>
      value.kind === 'signedIn' ? { ...value, refreshing: false, mutating: true } : value,
    );
    try {
      const credential = isCredentialChange(change);
      let result: ChangeResult;
      try {
        const issue = credential
          ? await changeCredential(change, current.csrf, current.epoch, controller.signal)
          : await changeResource(change, current.csrf, current.epoch, controller.signal);
        let message = '结束操作已确认。回收状态以列表为准。';
        if (credential) {
          message =
            change.kind === 'revokeCredential'
              ? '凭据已撤销。关联停止请求不代表进程已回收。'
              : '凭据已签发，秘密仅交付本次。';
        } else if (change.kind === 'note') {
          message = '备注已保存。';
        }
        result = {
          kind: 'success',
          message,
          ...(issue ? { issue } : {}),
        };
      } catch (error) {
        if (version !== generation.current || controller.signal.aborted) return;
        const code = error instanceof ApiError ? error.code : undefined;
        if ((error instanceof ApiError && error.status === 401) || code === 'CSRF_INVALID') {
          clear('管理员身份已失效，请重新登录。');
          return;
        }
        if (code === 'INVALID_ARGUMENT')
          return {
            kind: 'failed',
            message: credential
              ? '凭据参数无效，请核对名称、权限和有效期。'
              : '备注最多 512 个字符，且不能包含控制字符。',
          };
        if (code === 'HOST_DENIED' || code === 'ORIGIN_DENIED')
          return { kind: 'failed', message: '服务拒绝了当前页面来源，请检查访问地址。' };
        switch (code) {
          case 'CREDENTIAL_OPERATION_EXISTS':
            result = {
              kind: 'conflict',
              message: '该签发已有记录，秘密不会再次交付，请核对关联凭据。',
            };
            break;
          case 'CREDENTIAL_REVOKED':
            result = { kind: 'failed', message: '凭据已撤销，无法轮换。' };
            break;
          case 'REVISION_CONFLICT':
            result = {
              kind: 'conflict',
              message: '备注已被其他操作修改。草稿已保留，请核对当前备注。',
            };
            break;
          case 'SESSION_ENDED':
          case 'NOT_FOUND':
            result = { kind: 'failed', message: '目标已结束或不存在，请查看最新快照。' };
            break;
          default:
            result = {
              kind: 'unknown',
              message: '未取得操作确认，结果未知。不会自动重试，请核对最新快照。',
            };
        }
      }
      if (version !== generation.current || controller.signal.aborted) return;
      const checkRequired = result.kind === 'unknown' || result.kind === 'conflict';
      needsCheck.current = checkRequired;
      setView((value) =>
        value.kind === 'signedIn' ? { ...value, needsCheck: checkRequired } : value,
      );
      if (document.visibilityState !== 'hidden') await refresh(true);
      if (version === generation.current && !controller.signal.aborted) {
        if (result.issue && view.kind === 'signedIn' && view.deadline <= Date.now()) {
          clear('登录已到期，请重新登录。');
          return;
        }
        return result;
      }
    } finally {
      if (writing.current === controller) {
        writing.current = null;
        setView((value) => (value.kind === 'signedIn' ? { ...value, mutating: false } : value));
      }
    }
  };

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
      writing.current?.abort();
      writing.current = null;
      needsCheck.current = false;
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

  return { view, login, logout, refresh, mutate };
}
