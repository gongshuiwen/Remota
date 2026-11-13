import { useEffect, useRef, useState } from 'react';
import type { FormEvent } from 'react';
import type { ResourceChange, Snapshot } from './api';
import type { ChangeResult } from './useAdmin';
import s from './App.module.css';

export default function ResourceAction({
  initial,
  snapshot,
  pending,
  needsCheck,
  onSubmit,
  onRefresh,
  onClose,
}: {
  initial: ResourceChange;
  snapshot: Snapshot;
  pending: boolean;
  needsCheck: boolean;
  onSubmit: (change: ResourceChange) => Promise<ChangeResult | undefined>;
  onRefresh: () => Promise<boolean>;
  onClose: () => void;
}) {
  const editing = initial.kind === 'note';
  const [draft, setDraft] = useState(editing ? initial.note : '');
  const [revision, setRevision] = useState(editing ? initial.expected_note_revision : 0);
  const [result, setResult] = useState<ChangeResult>();
  const [checking, setChecking] = useState(false);
  const heading = useRef<HTMLHeadingElement>(null);
  useEffect(() => {
    const previous = document.activeElement;
    heading.current?.focus();
    return () => {
      if (previous instanceof HTMLElement && previous.isConnected) previous.focus();
    };
  }, []);
  const session =
    initial.kind !== 'endClient'
      ? snapshot.sessions.find((value) => value.id === initial.id)
      : undefined;
  const client =
    initial.kind === 'endClient'
      ? snapshot.clients.find((value) => value.id === initial.id)
      : undefined;
  const available =
    initial.kind === 'endClient'
      ? client && client.lifecycle !== 'ended'
      : session && session.state !== 'ended';
  const blocked = needsCheck || result?.kind === 'unknown' || result?.kind === 'conflict';
  const matched =
    !needsCheck &&
    (editing
      ? session?.note === draft && session.note_revision >= revision
      : initial.kind === 'endClient'
        ? client?.lifecycle === 'ended'
        : session?.state === 'ended');
  const title = editing
    ? '编辑备注'
    : initial.kind === 'endClient'
      ? '结束 MCP 客户端'
      : '结束设备会话';
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (pending || checking || blocked || !available || result?.kind === 'success') return;
    const value = await onSubmit(
      editing ? { ...initial, note: draft, expected_note_revision: revision } : initial,
    );
    if (value) setResult(value);
  };
  const check = async () => {
    setChecking(true);
    await onRefresh();
    setChecking(false);
  };
  return (
    <form
      className={s.actionPanel}
      onSubmit={(event) => void submit(event)}
      aria-labelledby="action-title"
      aria-busy={pending || checking}
    >
      <h2 id="action-title" tabIndex={-1} ref={heading}>
        {title}
      </h2>
      <code className={s.targetId}>{initial.id}</code>
      {editing ? (
        <>
          <label htmlFor="note">备注草稿</label>
          <input
            id="note"
            value={draft}
            disabled={pending || result?.kind === 'success'}
            onChange={(event) => {
              setDraft(event.target.value);
              if (result?.kind === 'failed') setResult(undefined);
            }}
          />
          <p>最多 512 个字符。自动刷新不会覆盖草稿。</p>
          <p>
            当前备注：<span>{session?.note || '（空）'}</span> · 当前修订{' '}
            {session?.note_revision ?? '未知'} · 编辑基于修订 {revision}
          </p>
        </>
      ) : (
        <p>
          {initial.kind === 'endClient'
            ? '结束此客户端并请求停止其关联任务，设备授权不因此结束。停止请求不代表进程已退出。'
            : '结束此设备会话并收回授权。进程回收状态将单独显示，结束不代表已经回收。'}
        </p>
      )}
      {!available && <p>最新快照中目标已结束或不存在。</p>}
      {(result || needsCheck) && (
        <div
          className={result?.kind === 'success' ? s.notice : s.error}
          role={result?.kind === 'success' ? 'status' : 'alert'}
        >
          {result?.message ?? '上次操作结果尚未核对，请先读取最新快照。'}
          {blocked && (
            <p>
              {needsCheck
                ? '尚未取得新的有效快照。'
                : matched
                  ? editing
                    ? '已核对，当前备注与草稿一致。'
                    : '已核对，最新快照显示目标已结束；回收状态请查看列表。'
                  : '已读取最新快照，请核对当前状态后再决定是否提交。'}
            </p>
          )}
        </div>
      )}
      <div className={s.actionButtons}>
        <button
          type="button"
          className={s.secondaryButton}
          disabled={pending || checking}
          onClick={onClose}
        >
          {result ? '关闭' : '取消'}
        </button>
        {blocked ? (
          <>
            <button
              type="button"
              className={s.secondaryButton}
              disabled={pending || checking}
              onClick={() => void check()}
            >
              {checking ? '核对中…' : '核对最新快照'}
            </button>
            {!needsCheck && available && !matched && (
              <button
                type="button"
                className={s.secondaryButton}
                disabled={pending || checking}
                onClick={() => {
                  if (session) setRevision(session.note_revision);
                  setResult(undefined);
                }}
              >
                {editing ? '保留草稿并采用当前修订' : '重新确认结束'}
              </button>
            )}
          </>
        ) : (
          result?.kind !== 'success' &&
          available && (
            <button
              type="submit"
              className={editing ? s.primaryButton : s.dangerButton}
              disabled={pending || checking}
            >
              {pending ? '正在提交…' : editing ? '保存备注' : '确认结束'}
            </button>
          )
        )}
      </div>
    </form>
  );
}
