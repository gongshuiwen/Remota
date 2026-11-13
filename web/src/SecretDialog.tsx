import { useEffect, useRef, useState } from 'react';
import type { CredentialIssue } from './api';
import s from './App.module.css';

export default function SecretDialog({
  issue,
  onClose,
}: {
  issue: CredentialIssue;
  onClose: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const active = useRef(false);
  const [revealed, setRevealed] = useState(false);
  const [copying, setCopying] = useState(false);
  const [notice, setNotice] = useState('');
  useEffect(() => {
    active.current = true;
    const element = dialog.current!;
    element.showModal();
    return () => {
      active.current = false;
      element.close();
    };
  }, []);
  const copy = async () => {
    if (copying) return;
    setCopying(true);
    try {
      await navigator.clipboard.writeText(issue.secret);
      if (active.current) setNotice('已复制秘密。');
    } catch {
      if (active.current) setNotice('复制失败，请显示并选中秘密手动复制。');
    } finally {
      if (active.current) setCopying(false);
    }
  };
  return (
    <dialog
      ref={dialog}
      className={s.secretDialog}
      aria-labelledby="secret-title"
      onClose={() => {
        // StrictMode cleanup can queue close before the same dialog is reopened.
        if (active.current && !dialog.current?.open) onClose();
      }}
      onCancel={(event) => {
        event.preventDefault();
        onClose();
      }}
    >
      <h2 id="secret-title">一次性秘密</h2>
      <p>{issue.credential.name}</p>
      <code className={s.targetId}>{issue.credential.id}</code>
      <p>请现在保存。关闭、退出或登录失效后无法再次取回。</p>
      <label htmlFor="issued-secret">凭据秘密</label>
      <input
        id="issued-secret"
        type={revealed ? 'text' : 'password'}
        value={issue.secret}
        readOnly
        autoComplete="off"
        spellCheck={false}
      />
      <div className={s.actionButtons}>
        <button type="button" className={s.secondaryButton} onClick={() => setRevealed(!revealed)}>
          {revealed ? '隐藏秘密' : '显示秘密'}
        </button>
        <button
          type="button"
          className={s.secondaryButton}
          disabled={copying}
          onClick={() => void copy()}
        >
          {copying ? '正在复制…' : '复制秘密'}
        </button>
        <button type="button" className={s.primaryButton} onClick={onClose}>
          我已保存，关闭
        </button>
      </div>
      {notice && <p role="status">{notice}</p>}
    </dialog>
  );
}
