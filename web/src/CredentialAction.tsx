import { useEffect, useRef, useState } from 'react';
import type { FormEvent } from 'react';
import type { CredentialChange, CredentialIssue, Snapshot } from './api';
import type { ChangeResult } from './useAdmin';
import SecretDialog from './SecretDialog';
import s from './App.module.css';

export type CredentialTarget =
  | { kind: 'createCredential' }
  | { kind: 'rotateCredential'; id: string }
  | { kind: 'revokeCredential'; id: string };

const operationID = () =>
  Array.from(crypto.getRandomValues(new Uint8Array(16)), (value) =>
    value.toString(16).padStart(2, '0'),
  ).join('');

export default function CredentialAction({
  initial,
  snapshot,
  pending,
  needsCheck,
  onSubmit,
  onRefresh,
  onClose,
  onAction,
}: {
  initial: CredentialTarget;
  snapshot: Snapshot;
  pending: boolean;
  needsCheck: boolean;
  onSubmit: (change: CredentialChange) => Promise<ChangeResult | undefined>;
  onRefresh: () => Promise<boolean>;
  onClose: () => void;
  onAction: (target: CredentialTarget) => void;
}) {
  const creating = initial.kind === 'createCredential';
  const issuing = initial.kind !== 'revokeCredential';
  const [operation] = useState(() => (issuing ? operationID() : ''));
  const [kind, setKind] = useState<'access' | 'mcp'>('access');
  const [name, setName] = useState('');
  const [allowExec, setAllowExec] = useState(false);
  const [duration, setDuration] = useState('');
  const [unit, setUnit] = useState('86400');
  const [result, setResult] = useState<Omit<ChangeResult, 'issue'>>();
  const [issue, setIssue] = useState<CredentialIssue>();
  const [checking, setChecking] = useState(false);
  const heading = useRef<HTMLHeadingElement>(null);
  useEffect(() => {
    const previous = document.activeElement;
    heading.current?.focus();
    return () => {
      if (previous instanceof HTMLElement && previous.isConnected) previous.focus();
    };
  }, []);
  const current = !creating
    ? snapshot.credentials.find((value) => value.id === initial.id)
    : undefined;
  const found = issuing
    ? snapshot.credentials.find((value) => value.creation_operation_id === operation)
    : undefined;
  const busy = pending || checking;
  const succeeded = result?.kind === 'success';
  const blocked = needsCheck || result?.kind === 'unknown' || result?.kind === 'conflict';
  const checked = blocked && !needsCheck;
  const available = creating || (current && current.status !== 'revoked');
  const title = creating ? '签发凭据' : issuing ? '轮换凭据' : '撤销凭据';
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (busy || blocked || !available || succeeded) return;
    const seconds = Number(duration) * Number(unit);
    if (duration && !Number.isSafeInteger(seconds)) {
      setResult({ kind: 'failed', message: '有效期必须能表示为整数秒。' });
      return;
    }
    const expiry = duration ? { expires_in_seconds: seconds } : {};
    const change: CredentialChange = creating
      ? {
          kind: 'createCredential',
          input: {
            operation_id: operation,
            kind,
            name,
            ...expiry,
            ...(kind === 'mcp' ? { allow_exec: allowExec } : {}),
          },
        }
      : initial.kind === 'rotateCredential'
        ? { ...initial, input: { operation_id: operation, ...expiry } }
        : initial;
    const value = await onSubmit(change);
    if (!value) return;
    const { issue: secretIssue, ...status } = value;
    setResult(status);
    if (secretIssue) setIssue(secretIssue);
  };
  const check = async () => {
    setChecking(true);
    await onRefresh();
    setChecking(false);
  };
  return (
    <>
      <form
        className={s.actionPanel}
        aria-labelledby="credential-action-title"
        aria-busy={busy}
        onSubmit={(event) => void submit(event)}
      >
        <h2 id="credential-action-title" ref={heading} tabIndex={-1}>
          {title}
        </h2>
        {current && (
          <>
            <p>
              {current.name} · {current.kind === 'access' ? '设备接入' : 'MCP'}
            </p>
            <code className={s.targetId}>{current.id}</code>
          </>
        )}
        <fieldset className={s.credentialFields} disabled={pending || succeeded || blocked}>
          {creating && (
            <>
              <label htmlFor="credential-kind">凭据类型</label>
              <select
                id="credential-kind"
                value={kind}
                onChange={(event) => {
                  setKind(event.target.value as 'access' | 'mcp');
                  setAllowExec(false);
                }}
              >
                <option value="access">设备接入</option>
                <option value="mcp">MCP</option>
              </select>
              <label htmlFor="credential-name">凭据名称</label>
              <input
                id="credential-name"
                value={name}
                required
                onChange={(event) => setName(event.target.value)}
              />
              {kind === 'mcp' && (
                <label className={s.checkLabel}>
                  <input
                    type="checkbox"
                    checked={allowExec}
                    onChange={(event) => setAllowExec(event.target.checked)}
                  />
                  允许申请命令执行，仍需设备本机 full 同意
                </label>
              )}
            </>
          )}
          {issuing && (
            <>
              <label htmlFor="credential-duration">有效期数量（留空使用服务默认值）</label>
              <div className={s.durationFields}>
                <input
                  id="credential-duration"
                  type="number"
                  min="1"
                  step="1"
                  value={duration}
                  onChange={(event) => setDuration(event.target.value)}
                />
                <select
                  aria-label="有效期单位"
                  value={unit}
                  onChange={(event) => setUnit(event.target.value)}
                >
                  <option value="86400">天</option>
                  <option value="3600">小时</option>
                  <option value="1">秒</option>
                </select>
              </div>
            </>
          )}
        </fieldset>
        {!creating && (
          <p>
            {issuing
              ? '轮换会撤销旧凭据，新凭据继承名称、类型和命令申请权限；旧授权不会转移。'
              : '撤销后不能重新启用。'}
            {current?.kind === 'access'
              ? '关联设备会话将结束。'
              : '关联 MCP 客户端将结束，并请求停止其任务。'}
            进程回收另行观察。
          </p>
        )}
        {current && (
          <p>
            当前关联：{current.effects_count.live_sessions} 个设备会话，
            {current.effects_count.live_clients} 个客户端，{current.effects_count.unsettled_tasks}{' '}
            个未结算任务。
          </p>
        )}
        {!available && !succeeded && <p>源凭据已撤销或不在当前快照中，无法继续本次操作。</p>}
        {(result || needsCheck) && (
          <div className={succeeded ? s.notice : s.error} role={succeeded ? 'status' : 'alert'}>
            {result?.message ?? '上次操作尚未核对，请先读取最新快照。'}
            {needsCheck && <p>尚未取得新的有效快照。</p>}
            {checked && issuing && (
              <>
                <p>
                  关联 ID：<code>{operation}</code>
                </p>
                {found ? (
                  <p>
                    已找到凭据 <code>{found.id}</code>
                    。秘密无法再次取回；
                    {found.status === 'revoked'
                      ? '该记录已撤销，可另建凭据。'
                      : '请先撤销该记录，再另建凭据。'}
                  </p>
                ) : (
                  <p>尚未发现关联记录，结果仍未知。不会以新 ID 自动重新签发。</p>
                )}
              </>
            )}
            {checked && !issuing && (
              <p>
                {current?.status === 'revoked'
                  ? '最新快照显示凭据已撤销；停止请求不代表进程已退出。'
                  : '最新快照尚未显示撤销，请核对后再决定。'}
              </p>
            )}
          </div>
        )}
        <div className={s.actionButtons}>
          <button type="button" className={s.secondaryButton} disabled={busy} onClick={onClose}>
            {result ? '关闭' : '取消'}
          </button>
          {blocked ? (
            <>
              <button
                type="button"
                className={s.secondaryButton}
                disabled={busy}
                onClick={() => void check()}
              >
                {checking ? '核对中…' : '核对最新快照'}
              </button>
              {!needsCheck && found && found.status !== 'revoked' && (
                <button
                  type="button"
                  className={s.dangerButton}
                  disabled={busy}
                  onClick={() => onAction({ kind: 'revokeCredential', id: found.id })}
                >
                  撤销此凭据
                </button>
              )}
              {!issuing && !needsCheck && available && (
                <button
                  type="button"
                  className={s.secondaryButton}
                  disabled={busy}
                  onClick={() => setResult(undefined)}
                >
                  重新确认撤销
                </button>
              )}
            </>
          ) : (
            !succeeded &&
            available && (
              <button
                type="submit"
                className={creating ? s.primaryButton : s.dangerButton}
                disabled={busy}
              >
                {pending ? '正在提交…' : creating ? '签发凭据' : issuing ? '确认轮换' : '确认撤销'}
              </button>
            )
          )}
        </div>
      </form>
      {issue && <SecretDialog issue={issue} onClose={() => setIssue(undefined)} />}
    </>
  );
}
