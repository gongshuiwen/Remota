import { useState } from 'react';
import type { FormEvent, ReactNode } from 'react';
import { useAdmin } from './useAdmin';
import type { ChangeResult } from './useAdmin';
import type { AdminChange, Credential, MCPClient, ResourceChange, Session, Snapshot } from './api';
import ResourceAction from './ResourceAction';
import CredentialAction from './CredentialAction';
import type { CredentialTarget } from './CredentialAction';
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

const dateFormat = new Intl.DateTimeFormat('zh-CN', {
  month: '2-digit',
  day: '2-digit',
  hour: '2-digit',
  minute: '2-digit',
  hour12: false,
});
const stamp = (value: string) => dateFormat.format(new Date(value));
function Expiry({ value, now }: { value: string; now: number }) {
  const remaining = Date.parse(value) - now;
  const estimate =
    remaining <= 0
      ? '预计已到期'
      : remaining < 60_000
        ? '不足 1 分钟'
        : remaining < 3_600_000
          ? `约 ${Math.ceil(remaining / 60_000)} 分钟后`
          : remaining < 86_400_000
            ? `约 ${Math.ceil(remaining / 3_600_000)} 小时后`
            : `约 ${Math.ceil(remaining / 86_400_000)} 天后`;
  return (
    <div className={s.cellStack}>
      <time dateTime={value}>{stamp(value)}</time>
      <small>{estimate}</small>
    </div>
  );
}

export function App() {
  const admin = useAdmin();
  const view = admin.view;
  if (view.kind === 'checking')
    return (
      <main className={s.checking}>
        <Brand />
        <p role="status">正在确认管理员登录…</p>
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
  error?: string;
  notice?: string;
  onLogin: (password: string) => Promise<void>;
}) {
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
        <Brand />
        <div className={s.introCopy}>
          <h1>
            每一次连接，
            <br />
            从本机同意开始。
          </h1>
          <p>
            集中查看临时设备、AI 连接和凭据，
            <br />
            让远程协助的权限与状态清晰可见。
          </p>
          <div className={s.consentDiagram} aria-hidden="true">
            <span>
              <Icon name="devices" />
              设备本机
            </span>
            <i />
            <span>
              <Icon name="shield" />
              同意协助
            </span>
            <i />
            <span>
              <Icon name="clients" />
              远程连接
            </span>
          </div>
        </div>
        <p className={s.introFoot}>临时授权，由设备端随时收紧或结束。</p>
      </section>
      <section className={s.loginPanel}>
        <form className={s.loginForm} onSubmit={submit} aria-busy={pending}>
          <span className={s.loginSymbol}>
            <Icon name="shield" />
          </span>
          <h2>登录管理台</h2>
          <p>使用服务初始化时获得的管理员口令。</p>
          {notice && (
            <div className={s.notice} role="status">
              {notice}
            </div>
          )}
          {error && (
            <div className={s.error} role="alert">
              {error}
            </div>
          )}
          <label htmlFor="password">管理员口令</label>
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
            {pending ? '正在确认…' : '登录管理台'}
          </button>
          <p className={s.loginFoot}>口令仅用于本次登录，不保存在浏览器存储中。</p>
        </form>
      </section>
    </main>
  );
}

const tabs = {
  devices: { name: '设备会话', description: '查看本机授权、连接观察与会话期限。' },
  clients: { name: 'AI 连接', description: '查看 MCP 客户端及其关联凭据。' },
  credentials: { name: '凭据', description: '查看设备接入与 MCP 凭据的有效状态。' },
};
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
  error?: string;
  onRefresh: () => Promise<boolean>;
  onLogout: () => Promise<void>;
  onChange: (change: AdminChange) => Promise<ChangeResult | undefined>;
}) {
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
        <div className={s.workspaceLabel}>远程协助管理台</div>
        <nav aria-label="管理视图">
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
              <span>{tabs[key].name}</span>
              <span className={s.navCount}>{allCounts[key]}</span>
            </button>
          ))}
        </nav>
        <div className={s.sidebarBottom}>
          <div className={s.serverLabel}>
            <span
              className={`${s.statusDot} ${snapshot.server.health === 'ready' ? s.liveDot : s.warnDot}`}
            />
            {snapshot.server.health === 'ready' ? '服务正常' : '服务已降级'}
          </div>
          <p>Remota {snapshot.server.version}</p>
          <p title={snapshot.server.public_url}>{snapshot.server.public_url ?? '未配置公开地址'}</p>
        </div>
      </aside>
      <div className={s.workspace}>
        <header className={s.topbar}>
          <div>
            <span className={s.statusDot} />
            管理快照
            <span className={s.topbarDivider} />
            资源管理
          </div>
          <button className={s.textButton} onClick={() => void onLogout()}>
            <Icon name="exit" />
            退出登录
          </button>
        </header>
        <main className={s.main}>
          <div className={s.pageHeading}>
            <div>
              <h1>{title.name}</h1>
              <p>{title.description}</p>
            </div>
            <div className={s.updateActions}>
              <span>最近更新 {stamp(snapshot.server_time)}</span>
              <button
                className={s.secondaryButton}
                disabled={refreshing || mutating}
                onClick={() => void onRefresh()}
              >
                <span className={refreshing ? s.spinning : undefined}>
                  <Icon name="refresh" />
                </span>
                {refreshing ? '更新中…' : '刷新'}
              </button>
            </div>
          </div>
          {error && (
            <div className={s.error} role="alert">
              {error}
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
          <dl className={s.metrics}>
            <div>
              <dt>有效设备会话</dt>
              <dd>
                {summary.live_sessions}
                <span>个</span>
              </dd>
            </div>
            <div>
              <dt>等待回收确认</dt>
              <dd className={summary.cleanup_pending_sessions ? s.attention : undefined}>
                {summary.cleanup_pending_sessions}
                <span>个</span>
              </dd>
            </div>
            <div>
              <dt>已就绪 AI 连接</dt>
              <dd>
                {summary.ready_clients}
                <span>个</span>
              </dd>
            </div>
            <div>
              <dt>有效凭据</dt>
              <dd>
                {summary.active_access_credentials + summary.active_mcp_credentials}
                <span>个</span>
              </dd>
            </div>
          </dl>
          {(summary.expiring_credentials > 0 || summary.expiring_sessions > 0) && (
            <p className={s.expiringNotice}>
              {summary.expiring_sessions} 个设备会话将在 5 分钟内到期，
              {summary.expiring_credentials} 个凭据将在 7 天内到期。统计基于完整快照。
            </p>
          )}
          <section className={s.records} aria-label={`${title.name}列表`}>
            <div className={s.toolbar}>
              <div className={s.search}>
                <Icon name="search" />
                <input
                  type="search"
                  aria-label={`搜索${title.name}`}
                  placeholder={
                    tab === 'devices' ? '搜索名称、备注、设备或凭据 ID' : '搜索名称或 ID'
                  }
                  value={query}
                  onChange={(event) => setQuery(event.target.value)}
                />
              </div>
              {tab === 'credentials' && (
                <select
                  aria-label="凭据状态"
                  value={status}
                  onChange={(event) => setStatus(event.target.value)}
                >
                  <option value="all">全部状态</option>
                  <option value="active">有效</option>
                  <option value="expiring">即将到期（7 天）</option>
                  <option value="expired">已过期</option>
                  <option value="revoked">已撤销</option>
                </select>
              )}
              {tab === 'credentials' && (
                <button
                  className={s.secondaryButton}
                  disabled={mutating || action?.kind === 'createCredential'}
                  onClick={() => setAction({ kind: 'createCredential' })}
                >
                  签发凭据
                </button>
              )}
              <span className={s.matchCount}>
                匹配 {visibleCount} / 全部 {allCounts[tab]}
              </span>
            </div>
            {visibleCount === 0 ? (
              <div className={s.empty}>
                <Icon name={tab} />
                <h2>
                  {query || status !== 'all'
                    ? '没有匹配的记录'
                    : tab === 'devices'
                      ? '等待设备接入'
                      : '暂无记录'}
                </h2>
                <p>
                  {query || status !== 'all'
                    ? '试试其他名称、ID 或筛选条件。'
                    : tab === 'devices'
                      ? '设备接入并完成本机确认后，会在这里显示。'
                      : '新的连接或凭据会在下次快照更新时显示。'}
                </p>
                {(query || status !== 'all') && (
                  <button
                    className={s.textButton}
                    onClick={() => {
                      setQuery('');
                      setStatus('all');
                    }}
                  >
                    清除筛选
                  </button>
                )}
              </div>
            ) : (
              <div
                className={s.tableScroll}
                tabIndex={0}
                role="region"
                aria-label={`${title.name}数据，可横向滚动`}
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
                授权由设备本机确认，可随时收紧或结束。
              </span>
              <span className={s.tableHint}>可左右滑动查看完整信息</span>
              <span>页面可见时每 5 秒更新</span>
            </div>
          </section>
          <footer className={s.pageFoot}>
            <span>结束与回收分别观察，连接状态不代表执行许可。</span>
            <button
              className={s.textButton}
              onClick={() => setTransportOpen(!transportOpen)}
              aria-expanded={transportOpen}
            >
              连接信息
            </button>
          </footer>
          {transportOpen && (
            <dl className={s.transport}>
              <div>
                <dt>浏览器访问</dt>
                <dd>{location.protocol === 'https:' ? 'HTTPS' : 'HTTP'}</dd>
              </div>
              <div>
                <dt>服务公开配置</dt>
                <dd>
                  {
                    {
                      https: 'HTTPS',
                      loopback_http: '本机 HTTP',
                      insecure_http: '允许非加密 HTTP',
                      unconfigured: '未配置',
                    }[snapshot.transport.public_mode]
                  }
                </dd>
              </div>
              <div>
                <dt>服务最后一段连接</dt>
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
  return (
    <RecordTable headers={['设备 / 会话', '状态', '本机授权', '连接观察', '到期时间', '操作']}>
      {values.map((value) => (
        <tr key={value.id}>
          <td>
            <div className={s.deviceName}>
              <span className={s.deviceIcon}>
                <Icon name="devices" />
              </span>
              <div className={s.cellStack}>
                <strong>{value.note || value.cached_info?.hostname || '未命名设备'}</strong>
                <small>
                  {value.cached_info
                    ? `${value.cached_info.os} / ${value.cached_info.arch}`
                    : '尚无设备信息'}
                </small>
                <code title={value.id}>{value.id}</code>
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
                  { recent: '最近可达', stale: '未更新', never_seen: '尚未建立' }[
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
                  编辑备注
                </button>
                <button
                  className={s.dangerText}
                  disabled={pending}
                  onClick={() => onAction({ kind: 'endSession', id: value.id })}
                >
                  结束会话
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
  return (
    <RecordTable headers={['客户端', '生命周期', '活动观察', '关联凭据', '到期时间', '操作']}>
      {values.map((value) => (
        <tr key={value.id}>
          <td>
            <div className={s.cellStack}>
              <strong>{value.client_info.name}</strong>
              <small>版本 {value.client_info.version}</small>
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
              {{ initializing: '初始化中', ready: '已就绪', ended: '已结束' }[value.lifecycle]}
            </Badge>
          </td>
          <td>{{ recent: '最近活动', idle: '空闲', expired: '已过期' }[value.activity]}</td>
          <td>
            <div className={s.cellStack}>
              <span>{credentialNames.get(value.credential_id) ?? '关联凭据'}</span>
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
                结束客户端
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
  return (
    <RecordTable headers={['凭据', '类型', '状态', '关联资源', '到期时间', '操作']}>
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
              <span>{value.kind === 'access' ? '设备接入' : 'MCP'}</span>
              {value.kind === 'mcp' && (
                <small>{value.allow_exec ? '允许申请命令' : '仅固定诊断'}</small>
              )}
            </div>
          </td>
          <td>
            <CredentialStatus value={value} />
          </td>
          <td>
            <div className={s.cellStack}>
              <span>
                {value.effects_count.live_sessions} 个会话 / {value.effects_count.live_clients}{' '}
                个连接
              </span>
              <small>{value.effects_count.unsettled_tasks} 个未结算任务</small>
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
                  轮换凭据
                </button>
                <button
                  className={s.dangerText}
                  disabled={pending}
                  onClick={() => onAction({ kind: 'revokeCredential', id: value.id })}
                >
                  撤销凭据
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
  if (value.state === 'pending') return <Badge tone="warn">等待确认</Badge>;
  if (value.state === 'active') return <Badge tone="good">协助中</Badge>;
  return (
    <div className={s.cellStack}>
      <Badge>已结束</Badge>
      <small>
        {
          {
            not_needed: '无需回收',
            pending: '等待回收确认',
            confirmed: '回收已确认',
            unknown: '回收状态未知',
          }[value.cleanup ?? 'unknown']
        }
      </small>
    </div>
  );
}

function Permission({ value }: { value: Session }) {
  if (value.state === 'ended') return <Badge>已收回</Badge>;
  if (!value.consent) return <span className={s.muted}>尚未授权</span>;
  return (
    <div className={s.cellStack}>
      <Badge tone={value.consent.mode === 'full' ? 'blue' : 'muted'}>
        {value.consent.mode === 'full' ? '命令执行' : '只读诊断'}
      </Badge>
      <small>本机确认 · 修订 {value.consent.revision}</small>
    </div>
  );
}

function CredentialStatus({ value }: { value: Credential }) {
  return (
    <Badge tone={value.status === 'active' ? 'good' : 'muted'}>
      {{ active: '有效', expired: '已过期', revoked: '已撤销' }[value.status]}
    </Badge>
  );
}
