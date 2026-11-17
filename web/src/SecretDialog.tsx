import { useEffect, useRef, useState } from 'react';
import type { CredentialIssue } from './api';
import { LanguageSwitch, useLanguage } from './i18n';
import type { TextKey } from './i18n';
import s from './App.module.css';

export default function SecretDialog({
  issue,
  onClose,
}: {
  issue: CredentialIssue;
  onClose: () => void;
}) {
  const { t } = useLanguage();
  const dialog = useRef<HTMLDialogElement>(null);
  const active = useRef(false);
  const [revealed, setRevealed] = useState(false);
  const [copying, setCopying] = useState(false);
  const [notice, setNotice] = useState<TextKey>();
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
      <div className={s.dialogHeading}>
        <h2 id="secret-title">{t('一次性秘密')}</h2>
        <LanguageSwitch />
      </div>
      <p>{issue.credential.name}</p>
      <code className={s.targetId}>{issue.credential.id}</code>
      <p>{t('请现在保存。关闭、退出或登录失效后无法再次取回。')}</p>
      <label htmlFor="issued-secret">{t('凭据秘密')}</label>
      <input
        id="issued-secret"
        type={revealed ? 'text' : 'password'}
        value={issue.secret}
        autoFocus
        readOnly
        autoComplete="off"
        spellCheck={false}
      />
      <div className={s.actionButtons}>
        <button type="button" className={s.secondaryButton} onClick={() => setRevealed(!revealed)}>
          {revealed ? t('隐藏秘密') : t('显示秘密')}
        </button>
        <button
          type="button"
          className={s.secondaryButton}
          disabled={copying}
          onClick={() => void copy()}
        >
          {copying ? t('正在复制…') : t('复制秘密')}
        </button>
        <button type="button" className={s.primaryButton} onClick={onClose}>
          {t('我已保存，关闭')}
        </button>
      </div>
      {notice && <p role="status">{t(notice)}</p>}
    </dialog>
  );
}
