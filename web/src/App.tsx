import { useState } from 'react';
import type { FormEvent, ReactNode } from 'react';
import { useAdmin } from './useAdmin';
import type { ChangeResult } from './useAdmin';
import type { AdminChange, Credential, MCPClient, ResourceChange, Session, Snapshot } from './api';
import ResourceAction from './ResourceAction';
import CredentialAction from './CredentialAction';
import type { CredentialTarget } from './CredentialAction';
import { LanguageProvider, LanguageSwitch, useLanguage } from './i18n';
import type { TextKey } from './i18n';
import ConnectionGuide from './ConnectionGuide';
import s from './App.module.css';

type IconName = 'devices' | 'clients' | 'credentials' | 'refresh' | 'exit' | 'search' | 'shield';
const icons: Record<IconName, ReactNode> = {
  devices: (
    <>
      <rect x="3" y="4" width="18" height="13" rx="2" />
      <path d="M8 21h8m-4-4v4" />
    </>
  ),
  clients: (
    <>
      <path
        d="m9 15 6-6m-8 8-1 1a4.2 4.2 0 0 1-6-6l4-4a4.2 4.2 0 0 1 6 0m4 0 1-1a4.2 4.2 0 0 1 6 6l-4 4a4.2 4.2 0 0 1-6 0"
        transform="translate(1 -1)"
      />
    </>
  ),
  credentials: (
    <>
      <circle cx="8" cy="8" r="5" />
      <path d="m12 12 9 9m-4-4 3-3m-6 0 3-3" />
    </>
  ),
  refresh: (
    <>
      <path d="M20 7v5h-5M4 17v-5h5" />
      <path d="M6 6a8 8 0 0 1 13 4M5 14a8 8 0 0 0 13 4" />
    </>
  ),
  exit: (
    <>
      <path d="M9 4H4v16h5m5-12 4 4-4 4m-7-4h12" />
    </>
  ),
  search: (
    <>
      <circle cx="10.5" cy="10.5" r="6.5" />
      <path d="m16 16 5 5" />
    </>
  ),
  shield: (
    <>
      <path d="m12 3 8 3v6c0 5-8 9-8 9s-8-4-8-9V6l8-3Z" />
      <path d="m8 12 3 3 5-6" />
    </>
  ),
};

function Icon({ name }: { name: IconName }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.6"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      {icons[name]}
    </svg>
  );
}

function Brand() {
  return (
    <div className={s.brand}>
      <svg viewBox="0 0 32 32" fill="none" aria-hidden="true">
        <path d="M5 18V6h13M27 14v12H14" stroke="currentColor" strokeWidth="4" />
        <path d="m9 23 14-14" stroke="currentColor" strokeWidth="4" />
      </svg>
      <span>Remota</span>
    </div>
  );
}

function Badge({
  children,
  tone = 'muted',
}: {
  children: ReactNode;
  tone?: 'good' | 'warn' | 'muted' | 'blue';
}) {
  return <span className={`${s.badge} ${s[tone]}`}>{children}</span>;
}

function Expiry({ value, now }: { value: string; now: number }) {
  const { t, stamp } = useLanguage();
  const remaining = Date.parse(value) - now;
  const estimate =
    remaining <= 0
      ? t('预计已到期')
      : remaining < 60_000
        ? t('不足 1 分钟')
        : remaining < 3_600_000
          ? t('约 {count} 分钟后', { count: Math.ceil(remaining / 60_000) })
          : remaining < 86_400_000
            ? t('约 {count} 小时后', { count: Math.ceil(remaining / 3_600_000) })
            : t('约 {count} 天后', { count: Math.ceil(remaining / 86_400_000) });
  return (
    <div className={s.cellStack}>
      <time dateTime={value}>{stamp(value)}</time>
      <small>{estimate}</small>
    </div>
  );
}

export function App() {
  return (
    <LanguageProvider>
      <AdminApp />
    </LanguageProvider>
  );
}

function AdminApp() {
  const { t } = useLanguage();
  const admin = useAdmin();
  const view = admin.view;
  if (view.kind === 'checking')
    return (
      <main className={s.checking}>
        <Brand />
        <LanguageSwitch />
        <p role="status">{t('正在确认管理员登录…')}</p>
      </main>
    );
  if (view.kind === 'signedOut')
    return (
      <Login pending={view.pending} error={view.error} notice={view.notice} onLogin={admin.login} />
    );
  return (
    <Dashboard
      snapshot={view.snapshot}
      receivedAt={view.receivedAt}
      refreshing={view.refreshing}
      mutating={view.mutating}
      needsCheck={view.needsCheck}
      error={view.error}
      onRefresh={admin.refresh}
      onLogout={admin.logout}
      onChange={admin.mutate}
    />
  );
}

function Login({
  pending,
  error,
  notice,
  onLogin,
}: {
  pending: boolean;
  error?: TextKey;
  notice?: TextKey;
  onLogin: (password: string) => Promise<void>;
}) {
  const { t } = useLanguage();
  const [password, setPassword] = useState('');
  const submit = (event: FormEvent) => {
    event.preventDefault();
    const value = password;
    setPassword('');
    void onLogin(value);
  };
  return (
    <main className={s.loginLayout}>
      <section className={s.introduction}>
        <div className={s.loginHeader}>
          <Brand />
          <LanguageSwitch />
        </div>
        <div className={s.introCopy}>
          <h1>
            {t('每一次连接，')}
            <br />
            {t('从本机同意开始。')}
          </h1>
          <p>
            {t('集中查看临时设备、AI 连接和凭据，')}
            <br />
            {t('让远程协助的权限与状态清晰可见。')}
          </p>
          <div className={s.consentDiagram} aria-hidden="true">
            <span>
              <Icon name="devices" />
              {t('设备本机')}
            </span>
            <i />
            <span>
              <Icon name="shield" />
              {t('同意协助')}
            </span>
            <i />
            <span>
              <Icon name="clients" />
              {t('远程连接')}
            </span>
          </div>
        </div>
        <p className={s.introFoot}>{t('临时授权，由设备端随时收紧或结束。')}</p>
      </section>
      <section className={s.loginPanel}>
        <form className={s.loginForm} onSubmit={submit} aria-busy={pending}>
          <span className={s.loginSymbol}>
            <Icon name="shield" />
          </span>
          <h2>{t('登录管理台')}</h2>
          <p>{t('使用服务初始化时获得的管理员口令。')}</p>
          {notice && (
            <div className={s.notice} role="status">
              {t(notice)}
            </div>
          )}
          {error && (
            <div className={s.error} role="alert">
              {t(error)}
            </div>
          )}
          <label htmlFor="password">{t('管理员口令')}</label>
          <input
            id="password"
            type="password"
            autoComplete="current-password"
            required
            maxLength={256}
            value={password}
            onChange={(event) => setPassword(event.target.value)}
            disabled={pending}
          />
          <button className={s.primaryButton} disabled={pending || !password} type="submit">
            {pending ? t('正在确认…') : t('登录管理台')}
          </button>
          <p className={s.loginFoot}>{t('口令仅用于本次登录，不保存在浏览器存储中。')}</p>
        </form>
      </section>
    </main>
  );
}

const tabs = {
  devices: { name: '设备会话', description: '查看本机授权、连接观察与会话期限。' },
  clients: { name: 'AI 连接', description: '查看 MCP 客户端及其关联凭据。' },
  credentials: { name: '凭据', description: '查看设备接入与 MCP 凭据的有效状态。' },
} as const;
type Tab = keyof typeof tabs;

function Dashboard({
  snapshot,
  receivedAt,
  refreshing,
  mutating,
  needsCheck,
  error,
  onRefresh,
  onLogout,
  onChange,
}: {
  snapshot: Snapshot;
  receivedAt: number;
  refreshing: boolean;
  mutating: boolean;
  needsCheck: boolean;
  error?: TextKey;
  onRefresh: () => Promise<boolean>;
  onLogout: () => Promise<void>;
  onChange: (change: AdminChange) => Promise<ChangeResult | undefined>;
}) {
  const { t, stamp } = useLanguage();
  const [tab, setTab] = useState<Tab>('devices');
  const [query, setQuery] = useState('');
  const [status, setStatus] = useState('all');
  const [transportOpen, setTransportOpen] = useState(false);
  const [action, setAction] = useState<ResourceChange | CredentialTarget>();
  const now = Date.parse(snapshot.server_time) + Math.max(0, performance.now() - receivedAt);
  const needle = query.toLowerCase();
  const match = (...fields: (string | undefined)[]) =>
    fields.some((field) => field?.toLowerCase().includes(needle));
  const credentialNames = new Map(snapshot.credentials.map((value) => [value.id, value.name]));
  const sessions =
    tab === 'devices'
      ? snapshot.sessions.filter((value) =>
          match(
            value.id,
            value.note,
            value.cached_info?.hostname,
            value.cached_info?.os,
            value.cached_info?.arch,
            value.access_credential_id,
            credentialNames.get(value.access_credential_id),
          ),
        )
      : [];
  const clients =
    tab === 'clients'
      ? snapshot.clients.filter((value) =>
          match(
            value.id,
            value.client_info.name,
            value.credential_id,
            credentialNames.get(value.credential_id),
          ),
        )
      : [];
  const credentials =
    tab === 'credentials'
      ? snapshot.credentials.filter(
          (value) =>
            match(value.id, value.name) &&
            (status === 'all' ||
              value.status === status ||
              (status === 'expiring' &&
                value.status === 'active' &&
                Date.parse(value.expires_at) > now &&
                Date.parse(value.expires_at) - now <= 7 * 86_400_000)),
        )
      : [];
  const allCounts = {
    devices: snapshot.sessions.length,
    clients: snapshot.clients.length,
    credentials: snapshot.credentials.length,
  };
  const visibleCount = { devices: sessions, clients, credentials }[tab].length;
  const title = tabs[tab];
  const summary = snapshot.summary;
  return (
    <div className={s.layout}>
      <aside className={s.sidebar}>
        <Brand />
        <div className={s.workspaceLabel}>{t('远程协助管理台')}</div>
        <nav aria-label={t('管理视图')}>
          {(Object.keys(tabs) as Tab[]).map((key) => (
            <button
              key={key}
              aria-current={tab === key ? 'page' : undefined}
              className={tab === key ? s.selectedNav : undefined}
              onClick={() => {
                setTab(key);
                setQuery('');
                setStatus('all');
              }}
            >
              <Icon name={key} />
              <span>{t(tabs[key].name)}</span>
              <span className={s.navCount}>{allCounts[key]}</span>
            </button>
          ))}
        </nav>
        <div className={s.sidebarBottom}>
          <div className={s.serverLabel}>
            <span
              className={`${s.statusDot} ${snapshot.server.health === 'ready' ? s.liveDot : s.warnDot}`}
            />
            {snapshot.server.health === 'ready' ? t('服务正常') : t('服务已降级')}
          </div>
          <p>Remota {snapshot.server.version}</p>
          <p title={snapshot.server.public_url}>
            {snapshot.server.public_url ?? t('未配置公开地址')}
          </p>
        </div>
      </aside>
      <div className={s.workspace}>
        <header className={s.topbar}>
          <div>
            <span className={s.statusDot} />
            {t('管理快照')}
            <span className={s.topbarDivider} />
            {t('资源管理')}
          </div>
          <div className={s.headerActions}>
            <LanguageSwitch />
            <button className={s.textButton} onClick={() => void onLogout()}>
              <Icon name="exit" />
              {t('退出登录')}
            </button>
          </div>
        </header>
        <main className={s.main}>
          <div className={s.pageHeading}>
            <div>
              <h1>{t(title.name)}</h1>
              <p>{t(title.description)}</p>
            </div>
            <div className={s.updateActions}>
              <span>
                {t('最近更新')} {stamp(snapshot.server_time)}
              </span>
              <button
                className={s.secondaryButton}
                disabled={refreshing || mutating}
                onClick={() => void onRefresh()}
              >
                <span className={refreshing ? s.spinning : undefined}>
                  <Icon name="refresh" />
                </span>
                {refreshing ? t('更新中…') : t('刷新')}
              </button>
            </div>
          </div>
          {error && (
            <div className={s.error} role="alert">
              {t(error)}
            </div>
          )}
          {action &&
            (action.kind === 'note' ||
            action.kind === 'endSession' ||
            action.kind === 'endClient' ? (
              <ResourceAction
                key={`${action.kind}:${action.id}`}
                initial={action}
                snapshot={snapshot}
                pending={mutating}
                needsCheck={needsCheck}
                onSubmit={onChange}
                onRefresh={onRefresh}
                onClose={() => setAction(undefined)}
              />
            ) : (
              <CredentialAction
                key={
                  action.kind === 'createCredential' ? action.kind : `${action.kind}:${action.id}`
                }
                initial={action}
                snapshot={snapshot}
                pending={mutating}
                needsCheck={needsCheck}
                onSubmit={onChange}
                onRefresh={onRefresh}
                onClose={() => setAction(undefined)}
                onAction={setAction}
              />
            ))}
          <ConnectionGuide
            publicURL={snapshot.server.public_url}
            publicMode={snapshot.transport.public_mode}
          />
          <dl className={s.metrics}>
            <div>
              <dt>{t('有效设备会话')}</dt>
              <dd>
                {summary.live_sessions}
                <span>{t('个')}</span>
              </dd>
            </div>
            <div>
              <dt>{t('等待回收确认')}</dt>
              <dd className={summary.cleanup_pending_sessions ? s.attention : undefined}>
                {summary.cleanup_pending_sessions}
                <span>{t('个')}</span>
              </dd>
            </div>
            <div>
              <dt>{t('已就绪 AI 连接')}</dt>
              <dd>
                {summary.ready_clients}
                <span>{t('个')}</span>
              </dd>
            </div>
            <div>
              <dt>{t('有效凭据')}</dt>
              <dd>
                {summary.active_access_credentials + summary.active_mcp_credentials}
                <span>{t('个')}</span>
              </dd>
            </div>
          </dl>
          {(summary.expiring_credentials > 0 || summary.expiring_sessions > 0) && (
            <p className={s.expiringNotice}>
              {t(
                '{sessions} 个设备会话将在 5 分钟内到期，{credentials} 个凭据将在 7 天内到期。统计基于完整快照。',
                { sessions: summary.expiring_sessions, credentials: summary.expiring_credentials },
              )}
            </p>
          )}
          <section className={s.records} aria-label={t('{name}列表', { name: t(title.name) })}>
            <div className={s.toolbar}>
              <div className={s.search}>
                <Icon name="search" />
                <input
                  type="search"
                  aria-label={t('搜索{name}', { name: t(title.name) })}
                  placeholder={
                    tab === 'devices' ? t('搜索名称、备注、设备或凭据 ID') : t('搜索名称或 ID')
                  }
                  value={query}
                  onChange={(event) => setQuery(event.target.value)}
                />
              </div>
              {tab === 'credentials' && (
                <select
                  aria-label={t('凭据状态')}
                  value={status}
                  onChange={(event) => setStatus(event.target.value)}
                >
                  <option value="all">{t('全部状态')}</option>
                  <option value="active">{t('有效')}</option>
                  <option value="expiring">{t('即将到期（7 天）')}</option>
                  <option value="expired">{t('已过期')}</option>
                  <option value="revoked">{t('已撤销')}</option>
                </select>
              )}
              {tab === 'credentials' && (
                <button
                  className={s.secondaryButton}
                  disabled={mutating || action?.kind === 'createCredential'}
                  onClick={() => setAction({ kind: 'createCredential' })}
                >
                  {t('签发凭据')}
                </button>
              )}
              <span className={s.matchCount}>
                {t('匹配 {matches} / 全部 {total}', {
                  matches: visibleCount,
                  total: allCounts[tab],
                })}
              </span>
            </div>
            {visibleCount === 0 ? (
              <div className={s.empty}>
                <Icon name={tab} />
                <h2>
                  {query || status !== 'all'
                    ? t('没有匹配的记录')
                    : tab === 'devices'
                      ? t('等待设备接入')
                      : t('暂无记录')}
                </h2>
                <p>
                  {query || status !== 'all'
                    ? t('试试其他名称、ID 或筛选条件。')
                    : tab === 'devices'
                      ? t('设备接入并完成本机确认后，会在这里显示。')
                      : t('新的连接或凭据会在下次快照更新时显示。')}
                </p>
                {(query || status !== 'all') && (
                  <button
                    className={s.textButton}
                    onClick={() => {
                      setQuery('');
                      setStatus('all');
                    }}
                  >
                    {t('清除筛选')}
                  </button>
                )}
              </div>
            ) : (
              <div
                className={s.tableScroll}
                tabIndex={0}
                role="region"
                aria-label={t('{name}数据，可横向滚动', { name: t(title.name) })}
              >
                {tab === 'devices' ? (
                  <SessionsTable
                    values={sessions}
                    now={now}
                    pending={mutating}
                    onAction={setAction}
                  />
                ) : tab === 'clients' ? (
                  <ClientsTable
                    values={clients}
                    now={now}
                    credentialNames={credentialNames}
                    pending={mutating}
                    onAction={setAction}
                  />
                ) : (
                  <CredentialsTable
                    values={credentials}
                    now={now}
                    pending={mutating}
                    onAction={setAction}
                  />
                )}
              </div>
            )}
            <div className={s.recordsFoot}>
              <span>
                <Icon name="shield" />
                {t('授权由设备本机确认，可随时收紧或结束。')}
              </span>
              <span className={s.tableHint}>{t('可左右滑动查看完整信息')}</span>
              <span>{t('页面可见时每 5 秒更新')}</span>
            </div>
          </section>
          <footer className={s.pageFoot}>
            <span>{t('结束与回收分别观察，连接状态不代表执行许可。')}</span>
            <button
              className={s.textButton}
              onClick={() => setTransportOpen(!transportOpen)}
              aria-expanded={transportOpen}
            >
              {t('连接信息')}
            </button>
          </footer>
          {transportOpen && (
            <dl className={s.transport}>
              <div>
                <dt>{t('浏览器访问')}</dt>
                <dd>{location.protocol === 'https:' ? 'HTTPS' : 'HTTP'}</dd>
              </div>
              <div>
                <dt>{t('服务公开配置')}</dt>
                <dd>
                  {
                    {
                      https: 'HTTPS',
                      loopback_http: t('本机 HTTP'),
                      insecure_http: t('允许非加密 HTTP'),
                      unconfigured: t('未配置'),
                    }[snapshot.transport.public_mode]
                  }
                </dd>
              </div>
              <div>
                <dt>{t('服务最后一段连接')}</dt>
                <dd>{snapshot.transport.backend_mode.toUpperCase()}</dd>
              </div>
            </dl>
          )}
        </main>
      </div>
    </div>
  );
}

function RecordTable({ headers, children }: { headers: string[]; children: ReactNode }) {
  return (
    <table>
      <thead>
        <tr>
          {headers.map((label) => (
            <th key={label} scope="col">
              {label}
            </th>
          ))}
        </tr>
      </thead>
      <tbody>{children}</tbody>
    </table>
  );
}

function SessionID({ value }: { value: string }) {
  const { t } = useLanguage();
  const [state, setState] = useState<'copying' | 'copied' | 'failed'>();
  const copy = async () => {
    if (state === 'copying') return;
    setState('copying');
    try {
      await navigator.clipboard.writeText(value);
      setState('copied');
    } catch {
      setState('failed');
    }
  };
  return (
    <div className={s.sessionID}>
      <code title={value}>{value}</code>
      <button
        type="button"
        className={s.textButton}
        disabled={state === 'copying'}
        onClick={() => void copy()}
      >
        {t(state === 'copied' ? '已复制 ID' : state === 'copying' ? '正在复制…' : '复制会话 ID')}
      </button>
      {state === 'failed' && (
        <label className={s.idFallback}>
          {t('复制失败，请选中完整会话 ID 手动复制。')}
          <input
            aria-label={t('完整会话 ID')}
            value={value}
            readOnly
            onFocus={(event) => event.currentTarget.select()}
          />
        </label>
      )}
    </div>
  );
}

function SessionsTable({
  values,
  now,
  pending,
  onAction,
}: {
  values: Session[];
  now: number;
  pending: boolean;
  onAction: (change: ResourceChange) => void;
}) {
  const { t, stamp } = useLanguage();
  return (
    <RecordTable
      headers={[
        t('设备 / 会话'),
        t('状态'),
        t('本机授权'),
        t('连接观察'),
        t('到期时间'),
        t('操作'),
      ]}
    >
      {values.map((value) => (
        <tr key={value.id}>
          <td>
            <div className={s.deviceName}>
              <span className={s.deviceIcon}>
                <Icon name="devices" />
              </span>
              <div className={s.cellStack}>
                <strong>{value.note || value.cached_info?.hostname || t('未命名设备')}</strong>
                <small>
                  {value.cached_info
                    ? `${value.cached_info.os} / ${value.cached_info.arch}`
                    : t('尚无设备信息')}
                </small>
                <SessionID value={value.id} />
              </div>
            </div>
          </td>
          <td>
            <SessionStatus value={value} />
          </td>
          <td>
            <Permission value={value} />
          </td>
          <td>
            <div className={s.cellStack}>
              <span className={s.connection}>
                <i
                  className={`${s.statusDot} ${value.connection.activity === 'recent' ? s.liveDot : ''}`}
                />
                {
                  { recent: t('最近可达'), stale: t('未更新'), never_seen: t('尚未建立') }[
                    value.connection.activity
                  ]
                }
              </span>
              {value.connection.last_seen_at && (
                <small>{stamp(value.connection.last_seen_at)}</small>
              )}
            </div>
          </td>
          <td>
            <Expiry value={value.expires_at} now={now} />
          </td>
          <td>
            {value.state !== 'ended' && (
              <div className={s.rowActions}>
                <button
                  className={s.textButton}
                  disabled={pending}
                  onClick={() =>
                    onAction({
                      kind: 'note',
                      id: value.id,
                      note: value.note,
                      expected_note_revision: value.note_revision,
                    })
                  }
                >
                  {t('编辑备注')}
                </button>
                <button
                  className={s.dangerText}
                  disabled={pending}
                  onClick={() => onAction({ kind: 'endSession', id: value.id })}
                >
                  {t('结束会话')}
                </button>
              </div>
            )}
          </td>
        </tr>
      ))}
    </RecordTable>
  );
}

function ClientsTable({
  values,
  now,
  credentialNames,
  pending,
  onAction,
}: {
  values: MCPClient[];
  now: number;
  credentialNames: Map<string, string>;
  pending: boolean;
  onAction: (change: ResourceChange) => void;
}) {
  const { t } = useLanguage();
  return (
    <RecordTable
      headers={[t('客户端'), t('生命周期'), t('活动观察'), t('关联凭据'), t('到期时间'), t('操作')]}
    >
      {values.map((value) => (
        <tr key={value.id}>
          <td>
            <div className={s.cellStack}>
              <strong>{value.client_info.name}</strong>
              <small>{t('版本 {version}', { version: value.client_info.version })}</small>
              <code title={value.id}>{value.id}</code>
            </div>
          </td>
          <td>
            <Badge
              tone={
                value.lifecycle === 'ready'
                  ? 'good'
                  : value.lifecycle === 'initializing'
                    ? 'warn'
                    : 'muted'
              }
            >
              {
                { initializing: t('初始化中'), ready: t('已就绪'), ended: t('已结束') }[
                  value.lifecycle
                ]
              }
            </Badge>
          </td>
          <td>
            {{ recent: t('最近活动'), idle: t('空闲'), expired: t('已过期') }[value.activity]}
          </td>
          <td>
            <div className={s.cellStack}>
              <span>{credentialNames.get(value.credential_id) ?? t('关联凭据')}</span>
              <code>{value.credential_id}</code>
            </div>
          </td>
          <td>
            <Expiry value={value.expires_at} now={now} />
          </td>
          <td>
            {value.lifecycle !== 'ended' && (
              <button
                className={s.dangerText}
                disabled={pending}
                onClick={() => onAction({ kind: 'endClient', id: value.id })}
              >
                {t('结束客户端')}
              </button>
            )}
          </td>
        </tr>
      ))}
    </RecordTable>
  );
}

function CredentialsTable({
  values,
  now,
  pending,
  onAction,
}: {
  values: Credential[];
  now: number;
  pending: boolean;
  onAction: (target: CredentialTarget) => void;
}) {
  const { t } = useLanguage();
  return (
    <RecordTable
      headers={[t('凭据'), t('类型'), t('状态'), t('关联资源'), t('到期时间'), t('操作')]}
    >
      {values.map((value) => (
        <tr key={value.id}>
          <td>
            <div className={s.cellStack}>
              <strong>{value.name}</strong>
              <code title={value.id}>{value.id}</code>
            </div>
          </td>
          <td>
            <div className={s.cellStack}>
              <span>{value.kind === 'access' ? t('设备接入') : 'MCP'}</span>
              {value.kind === 'mcp' && (
                <small>{value.allow_exec ? t('允许申请命令') : t('仅固定诊断')}</small>
              )}
            </div>
          </td>
          <td>
            <CredentialStatus value={value} />
          </td>
          <td>
            <div className={s.cellStack}>
              <span>
                {t('{sessions} 个会话 / {clients} 个连接', {
                  sessions: value.effects_count.live_sessions,
                  clients: value.effects_count.live_clients,
                })}
              </span>
              <small>
                {t('{count} 个未结算任务', { count: value.effects_count.unsettled_tasks })}
              </small>
            </div>
          </td>
          <td>
            <Expiry value={value.expires_at} now={now} />
          </td>
          <td>
            {value.status !== 'revoked' && (
              <div className={s.rowActions}>
                <button
                  className={s.textButton}
                  disabled={pending}
                  onClick={() => onAction({ kind: 'rotateCredential', id: value.id })}
                >
                  {t('轮换凭据')}
                </button>
                <button
                  className={s.dangerText}
                  disabled={pending}
                  onClick={() => onAction({ kind: 'revokeCredential', id: value.id })}
                >
                  {t('撤销凭据')}
                </button>
              </div>
            )}
          </td>
        </tr>
      ))}
    </RecordTable>
  );
}

function SessionStatus({ value }: { value: Session }) {
  const { t } = useLanguage();
  if (value.state === 'pending') return <Badge tone="warn">{t('等待确认')}</Badge>;
  if (value.state === 'active') return <Badge tone="good">{t('协助中')}</Badge>;
  return (
    <div className={s.cellStack}>
      <Badge>{t('已结束')}</Badge>
      <small>
        {
          {
            not_needed: t('无需回收'),
            pending: t('等待回收确认'),
            confirmed: t('回收已确认'),
            unknown: t('回收状态未知'),
          }[value.cleanup ?? 'unknown']
        }
      </small>
    </div>
  );
}

function Permission({ value }: { value: Session }) {
  const { t } = useLanguage();
  if (value.state === 'ended') return <Badge>{t('已收回')}</Badge>;
  if (!value.consent) return <span className={s.muted}>{t('尚未授权')}</span>;
  return (
    <div className={s.cellStack}>
      <Badge tone={value.consent.mode === 'full' ? 'blue' : 'muted'}>
        {value.consent.mode === 'full' ? t('命令执行') : t('只读诊断')}
      </Badge>
      <small>{t('本机确认 · 修订 {revision}', { revision: value.consent.revision })}</small>
    </div>
  );
}

function CredentialStatus({ value }: { value: Credential }) {
  const { t } = useLanguage();
  return (
    <Badge tone={value.status === 'active' ? 'good' : 'muted'}>
      {{ active: t('有效'), expired: t('已过期'), revoked: t('已撤销') }[value.status]}
    </Badge>
  );
}
