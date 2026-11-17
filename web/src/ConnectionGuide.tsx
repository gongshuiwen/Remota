import type { Snapshot } from './api';
import { useLanguage } from './i18n';
import s from './App.module.css';

function publicAddress(value?: string) {
  try {
    const url = new URL(value ?? '');
    if (
      !['http:', 'https:'].includes(url.protocol) ||
      url.username ||
      url.password ||
      url.pathname !== '/' ||
      url.search ||
      url.hash ||
      url.origin.includes("'")
    )
      return;
    return url.origin;
  } catch {
    return;
  }
}

export default function ConnectionGuide({
  publicURL,
  publicMode,
}: {
  publicURL?: string;
  publicMode: Snapshot['transport']['public_mode'];
}) {
  const { t } = useLanguage();
  const address = publicAddress(publicURL);
  const command = address
    ? `remota-agent --server '${address}'${publicMode === 'insecure_http' ? ' --allow-insecure-http' : ''}`
    : '';
  return (
    <details className={s.guide}>
      <summary>{t('接入指引')}</summary>
      <div className={s.guideContent}>
        {!address ? (
          <p>{t('请先使用 --public-url 配置服务公开地址，再刷新管理台。')}</p>
        ) : (
          <>
            <h2>{t('设备接入')}</h2>
            <p>
              {t(
                '在设备的交互终端运行 Agent，按隐藏输入提示输入 access 凭据。回车同意只读；full 命令执行需要再次本机确认。',
              )}
            </p>
            <label htmlFor="agent-command">{t('设备接入命令')}</label>
            <textarea
              id="agent-command"
              readOnly
              value={command}
              rows={2}
              spellCheck={false}
              onFocus={(event) => event.currentTarget.select()}
            />
            {publicMode === 'insecure_http' && (
              <p className={s.expiringNotice}>
                {t('当前公开地址使用非加密 HTTP。示例显式允许明文连接，请核对地址和部署配置。')}
              </p>
            )}
            <h2>{t('MCP 接入')}</h2>
            <p>
              {t(
                '在支持 Streamable HTTP 的客户端填写地址和授权头，将 MCP_TOKEN 替换为自己的 MCP 凭据。示例不会包含本页签发的秘密。',
              )}
            </p>
            <pre tabIndex={0} aria-label={t('MCP 配置示例')}>
              <code>
                {JSON.stringify(
                  {
                    url: new URL('/mcp', address).href,
                    headers: { Authorization: 'Bearer MCP_TOKEN' },
                  },
                  null,
                  2,
                )}
              </code>
            </pre>
            <p>
              {t(
                '从设备列表复制完整会话 ID，用于 MCP 工具的 session_id。复制 ID 不会选择目标或授予权限。',
              )}
            </p>
          </>
        )}
      </div>
    </details>
  );
}
