import { createContext, useContext, useEffect, useState } from 'react';
import type { ReactNode } from 'react';
import s from './App.module.css';

// Chinese source text is the key; render stored messages through t() so they follow the current language.
const english = {
  预计已到期: 'Estimated expired',
  '不足 1 分钟': 'Less than 1 min left',
  '约 {count} 分钟后': 'About {count} min left',
  '约 {count} 小时后': 'About {count} h left',
  '约 {count} 天后': 'About {count} d left',
  '正在确认管理员登录…': 'Checking administrator sign-in…',
  '每一次连接，': 'Every connection',
  '从本机同意开始。': 'starts with local consent.',
  '集中查看临时设备、AI 连接和凭据，': 'Manage temporary devices, AI connections and credentials.',
  '让远程协助的权限与状态清晰可见。':
    'See the permissions and status of each remote assistance session.',
  设备本机: 'Local device',
  同意协助: 'Local consent',
  远程连接: 'Remote connection',
  '临时授权，由设备端随时收紧或结束。':
    'Temporary access that the device operator can reduce or end at any time.',
  登录管理台: 'Sign in',
  '使用服务初始化时获得的管理员口令。':
    'Use the administrator password provided during server initialization.',
  管理员口令: 'Administrator password',
  '正在确认…': 'Checking…',
  '口令仅用于本次登录，不保存在浏览器存储中。':
    'The password is used for this sign-in and is not saved in browser storage.',
  设备会话: 'Device sessions',
  '查看本机授权、连接观察与会话期限。':
    'View local consent, connection observations and session expiry.',
  'AI 连接': 'AI connections',
  '查看 MCP 客户端及其关联凭据。': 'View MCP clients and their credentials.',
  凭据: 'Credentials',
  '查看设备接入与 MCP 凭据的有效状态。': 'View the status of device access and MCP credentials.',
  远程协助管理台: 'Remote assistance console',
  管理视图: 'Management views',
  服务正常: 'Server ready',
  服务已降级: 'Server degraded',
  未配置公开地址: 'Public address not configured',
  管理快照: 'Admin snapshot',
  资源管理: 'Resource management',
  退出登录: 'Sign out',
  最近更新: 'Last updated',
  '更新中…': 'Refreshing…',
  刷新: 'Refresh',
  有效设备会话: 'Live device sessions',
  个: '',
  等待回收确认: 'Awaiting cleanup confirmation',
  '已就绪 AI 连接': 'Ready AI connections',
  有效凭据: 'Active credentials',
  '{sessions} 个设备会话将在 5 分钟内到期，{credentials} 个凭据将在 7 天内到期。统计基于完整快照。':
    'Device sessions expiring within 5 min: {sessions}; credentials expiring within 7 days: {credentials}. Counts use the full snapshot.',
  '{name}列表': '{name} list',
  '搜索{name}': 'Search {name}',
  '搜索名称、备注、设备或凭据 ID': 'Search name, note, device or credential ID',
  '搜索名称或 ID': 'Search name or ID',
  凭据状态: 'Credential status',
  全部状态: 'All statuses',
  有效: 'Active',
  '即将到期（7 天）': 'Expiring within 7 days',
  已过期: 'Expired',
  已撤销: 'Revoked',
  签发凭据: 'Issue credential',
  '匹配 {matches} / 全部 {total}': 'Matches {matches} / Total {total}',
  没有匹配的记录: 'No matching records',
  等待设备接入: 'Waiting for devices',
  暂无记录: 'No records yet',
  '试试其他名称、ID 或筛选条件。': 'Try another name, ID or filter.',
  '设备接入并完成本机确认后，会在这里显示。':
    'Devices appear here when they connect and complete local consent.',
  '新的连接或凭据会在下次快照更新时显示。':
    'New connections and credentials appear with the next snapshot.',
  清除筛选: 'Clear filters',
  '{name}数据，可横向滚动': '{name} data, scroll horizontally',
  '授权由设备本机确认，可随时收紧或结束。':
    'Access requires local consent and can be reduced or ended at any time.',
  可左右滑动查看完整信息: 'Scroll horizontally for all details',
  '页面可见时每 5 秒更新': 'Refreshes every 5 seconds while visible',
  '结束与回收分别观察，连接状态不代表执行许可。':
    'Ending and cleanup are observed separately. Connection status does not grant execution permission.',
  连接信息: 'Connection details',
  浏览器访问: 'Browser connection',
  服务公开配置: 'Configured public transport',
  '本机 HTTP': 'Loopback HTTP',
  '允许非加密 HTTP': 'Unencrypted HTTP allowed',
  未配置: 'Not configured',
  服务最后一段连接: 'Last connection hop to the server',
  '已复制 ID': 'ID copied',
  '正在复制…': 'Copying…',
  '复制会话 ID': 'Copy session ID',
  '复制失败，请选中完整会话 ID 手动复制。':
    'Copy failed. Select the full session ID and copy it manually.',
  '完整会话 ID': 'Full session ID',
  '设备 / 会话': 'Device / Session',
  状态: 'Status',
  本机授权: 'Local consent',
  连接观察: 'Connection observation',
  到期时间: 'Expires',
  操作: 'Actions',
  未命名设备: 'Unnamed device',
  尚无设备信息: 'Device info not available',
  最近可达: 'Recently reachable',
  未更新: 'Not updated',
  尚未建立: 'Not established',
  编辑备注: 'Edit note',
  结束会话: 'End session',
  客户端: 'Client',
  生命周期: 'Lifecycle',
  活动观察: 'Activity observation',
  关联凭据: 'Linked credential',
  '版本 {version}': 'Version {version}',
  初始化中: 'Initializing',
  已就绪: 'Ready',
  已结束: 'Ended',
  最近活动: 'Recently active',
  空闲: 'Idle',
  结束客户端: 'End client',
  类型: 'Type',
  关联资源: 'Linked resources',
  设备接入: 'Device access',
  允许申请命令: 'May request command execution',
  仅固定诊断: 'Fixed diagnostics only',
  '{sessions} 个会话 / {clients} 个连接': 'Sessions: {sessions} / Clients: {clients}',
  '{count} 个未结算任务': 'Unsettled tasks: {count}',
  轮换凭据: 'Rotate credential',
  撤销凭据: 'Revoke credential',
  等待确认: 'Awaiting consent',
  协助中: 'Assisting',
  无需回收: 'Cleanup not needed',
  回收已确认: 'Cleanup confirmed',
  回收状态未知: 'Cleanup unknown',
  已收回: 'Withdrawn',
  尚未授权: 'Not authorized',
  命令执行: 'Command execution',
  只读诊断: 'Read-only diagnostics',
  '本机确认 · 修订 {revision}': 'Local consent · Revision {revision}',
  '结束 MCP 客户端': 'End MCP client',
  结束设备会话: 'End device session',
  备注草稿: 'Note draft',
  '最多 512 个字符。自动刷新不会覆盖草稿。':
    'Up to 512 characters. Refreshing does not overwrite the draft.',
  '当前备注：': 'Current note: ',
  '（空）': '(empty)',
  '· 当前修订 {current} · 编辑基于修订 {revision}':
    '· Current revision {current} · Draft based on revision {revision}',
  未知: 'Unknown',
  '结束此客户端并请求停止其关联任务，设备授权不因此结束。停止请求不代表进程已退出。':
    'End this client and request its tasks to stop. Device consent remains in place. A stop request does not confirm process exit.',
  '结束此设备会话并收回授权。进程回收状态将单独显示，结束不代表已经回收。':
    'End this device session and withdraw access. Cleanup is shown separately; ending does not confirm cleanup.',
  '最新快照中目标已结束或不存在。': 'The target has ended or is absent from the latest snapshot.',
  '上次操作结果尚未核对，请先读取最新快照。':
    'The previous operation has not been reconciled. Read the latest snapshot first.',
  '尚未取得新的有效快照。': 'A new valid snapshot has not been received.',
  '已核对，当前备注与草稿一致。': 'Reconciled: the current note matches the draft.',
  '已核对，最新快照显示目标已结束；回收状态请查看列表。':
    'Reconciled: the latest snapshot shows the target has ended. See the list for cleanup status.',
  '已读取最新快照，请核对当前状态后再决定是否提交。':
    'The latest snapshot is available. Review the current state before deciding whether to submit.',
  关闭: 'Close',
  取消: 'Cancel',
  '核对中…': 'Reconciling…',
  核对最新快照: 'Reconcile latest snapshot',
  保留草稿并采用当前修订: 'Keep draft and adopt current revision',
  重新确认结束: 'Review ending again',
  '正在提交…': 'Submitting…',
  保存备注: 'Save note',
  确认结束: 'Confirm ending',
  '有效期必须能表示为整数秒。': 'The lifetime must be expressible as a whole number of seconds.',
  凭据类型: 'Credential type',
  凭据名称: 'Credential name',
  '允许申请命令执行，仍需设备本机 full 同意':
    'Allow command requests; the device must still grant full local consent',
  '有效期数量（留空使用服务默认值）': 'Lifetime amount (leave blank for the server default)',
  有效期单位: 'Lifetime unit',
  天: 'Days',
  小时: 'Hours',
  秒: 'Seconds',
  '轮换会撤销旧凭据，新凭据继承名称、类型和命令申请权限；旧授权不会转移。':
    'Rotation revokes the old credential. The new credential inherits its name, type and command request permission. Existing access does not transfer.',
  '撤销后不能重新启用。': 'Revocation cannot be undone.',
  '关联设备会话将结束。': 'Linked device sessions will end.',
  '关联 MCP 客户端将结束，并请求停止其任务。':
    'Linked MCP clients will end and their tasks will be requested to stop.',
  '进程回收另行观察。': 'Process cleanup is observed separately.',
  '当前关联：{sessions} 个设备会话，{clients} 个客户端，{tasks} 个未结算任务。':
    'Current links: device sessions {sessions}, clients {clients}, unsettled tasks {tasks}.',
  '源凭据已撤销或不在当前快照中，无法继续本次操作。':
    'The source credential is revoked or absent from the current snapshot. This operation cannot continue.',
  '关联 ID：': 'Operation ID: ',
  '已找到凭据：': 'Credential found: ',
  '秘密无法再次取回；该记录已撤销，可另建凭据。':
    'The secret cannot be retrieved again. This record is revoked; you can create another credential.',
  '秘密无法再次取回；请先撤销该记录，再另建凭据。':
    'The secret cannot be retrieved again. Revoke this record before creating another credential.',
  '尚未发现关联记录，结果仍未知。不会以新 ID 自动重新签发。':
    'No linked record has been found. The result is still unknown. Issuance will not be retried automatically with a new ID.',
  '最新快照显示凭据已撤销；停止请求不代表进程已退出。':
    'The latest snapshot shows the credential is revoked. A stop request does not confirm process exit.',
  '最新快照尚未显示撤销，请核对后再决定。':
    'The latest snapshot does not show revocation yet. Review it before deciding.',
  撤销此凭据: 'Revoke this credential',
  重新确认撤销: 'Review revocation again',
  确认轮换: 'Confirm rotation',
  确认撤销: 'Confirm revocation',
  '已复制秘密。': 'Secret copied.',
  '复制失败，请显示并选中秘密手动复制。':
    'Copy failed. Reveal and select the secret to copy it manually.',
  一次性秘密: 'One-time secret',
  '请现在保存。关闭、退出或登录失效后无法再次取回。':
    'Save it now. It cannot be retrieved after closing, signing out or losing your sign-in.',
  凭据秘密: 'Credential secret',
  隐藏秘密: 'Hide secret',
  显示秘密: 'Reveal secret',
  复制秘密: 'Copy secret',
  '我已保存，关闭': 'Saved, close',
  '管理员登录或服务已变化，请重新登录。':
    'The administrator sign-in or server has changed. Sign in again.',
  '登录已到期，请重新登录。': 'Your sign-in has expired. Sign in again.',
  '登录已失效，请重新登录。': 'Your sign-in is no longer valid. Sign in again.',
  '更新失败，以下仍为最近一次成功的快照。':
    'Refresh failed. The last successful snapshot is still shown.',
  '暂时无法读取管理快照，请检查服务后重试。':
    'The admin snapshot is unavailable. Check the server and try again.',
  '结束操作已确认。回收状态以列表为准。': 'Ending confirmed. See the list for cleanup status.',
  '凭据已撤销。关联停止请求不代表进程已回收。':
    'Credential revoked. Related stop requests do not confirm process cleanup.',
  '凭据已签发，秘密仅交付本次。': 'Credential issued. The secret is delivered only this time.',
  '备注已保存。': 'Note saved.',
  '管理员身份已失效，请重新登录。':
    'Your administrator identity is no longer valid. Sign in again.',
  '凭据参数无效，请核对名称、权限和有效期。':
    'Invalid credential parameters. Check the name, permissions and lifetime.',
  '备注最多 512 个字符，且不能包含控制字符。':
    'Notes may contain up to 512 characters and no control characters.',
  '服务拒绝了当前页面来源，请检查访问地址。':
    'The server rejected this page origin. Check the address.',
  '该签发已有记录，秘密不会再次交付，请核对关联凭据。':
    'This issuance already has a record. The secret will not be delivered again. Review the linked credential.',
  '凭据已撤销，无法轮换。': 'The credential is revoked and cannot be rotated.',
  '备注已被其他操作修改。草稿已保留，请核对当前备注。':
    'Another operation changed the note. Your draft is preserved. Review the current note.',
  '目标已结束或不存在，请查看最新快照。':
    'The target has ended or does not exist. Check the latest snapshot.',
  '未取得操作确认，结果未知。不会自动重试，请核对最新快照。':
    'The operation was not confirmed; its result is unknown. It will not be retried automatically. Review the latest snapshot.',
  '管理员口令不正确。': 'Incorrect administrator password.',
  '登录尝试过于频繁，请稍后重试。': 'Too many sign-in attempts. Try again later.',
  '登录未能确认，请检查连接后重试。':
    'Sign-in was not confirmed. Check the connection and try again.',
  '本页数据已清除，但尚未取得服务端退出确认。':
    'Page data was cleared, but server sign-out has not been confirmed.',
  接入指引: 'Connection guide',
  '请先使用 --public-url 配置服务公开地址，再刷新管理台。':
    'Configure the server public address with --public-url, then refresh the console.',
  '在设备的交互终端运行 Agent，按隐藏输入提示输入 access 凭据。回车同意只读；full 命令执行需要再次本机确认。':
    'Run the Agent in an interactive terminal on the device and enter the access credential at the hidden prompt. Press Enter for read-only consent; full command execution requires another local confirmation.',
  设备接入命令: 'Device connection command',
  '当前公开地址使用非加密 HTTP。示例显式允许明文连接，请核对地址和部署配置。':
    'The public address uses unencrypted HTTP. The example explicitly allows plaintext connections. Check the address and deployment configuration.',
  'MCP 接入': 'MCP connection',
  '在支持 Streamable HTTP 的客户端填写地址和授权头，将 MCP_TOKEN 替换为自己的 MCP 凭据。示例不会包含本页签发的秘密。':
    'In a client that supports Streamable HTTP, set the URL and authorization header. Replace MCP_TOKEN with your MCP credential. This example never includes secrets issued on this page.',
  'MCP 配置示例': 'MCP configuration example',
  '从设备列表复制完整会话 ID，用于 MCP 工具的 session_id。复制 ID 不会选择目标或授予权限。':
    'Copy the full session ID from the device list for the MCP tools’ session_id. Copying an ID does not select a target or grant permission.',
  切换到英文: 'Switch to English',
  切换到中文: 'Switch to Chinese',
} as const;

export type TextKey = keyof typeof english;
export const languageKey = 'remota.language';
type Language = 'zh' | 'en';
type Translator = (key: TextKey, values?: Record<string, string | number>) => string;
const dateOptions: Intl.DateTimeFormatOptions = {
  month: '2-digit',
  day: '2-digit',
  hour: '2-digit',
  minute: '2-digit',
  hour12: false,
};
const dates = {
  zh: new Intl.DateTimeFormat('zh-CN', dateOptions),
  en: new Intl.DateTimeFormat('en-GB', dateOptions),
};
const LanguageContext = createContext<{
  language: Language;
  toggle: () => void;
  t: Translator;
  stamp: (value: string) => string;
} | null>(null);

export function LanguageProvider({ children }: { children: ReactNode }) {
  const [language, setLanguage] = useState<Language>(() => {
    try {
      return localStorage.getItem(languageKey) === 'en' ? 'en' : 'zh';
    } catch {
      return 'zh';
    }
  });
  useEffect(() => {
    document.documentElement.lang = language === 'zh' ? 'zh-CN' : 'en';
  }, [language]);
  const toggle = () => {
    const next = language === 'zh' ? 'en' : 'zh';
    setLanguage(next);
    try {
      localStorage.setItem(languageKey, next);
    } catch {
      /* Storage is optional. */
    }
  };
  const t: Translator = (key, values) => {
    const text = language === 'en' ? english[key] : key;
    return values
      ? text.replace(/\{(\w+)\}/g, (match, name: string) =>
          values[name] === undefined ? match : String(values[name]),
        )
      : text;
  };
  return (
    <LanguageContext
      value={{ language, toggle, t, stamp: (value) => dates[language].format(new Date(value)) }}
    >
      {children}
    </LanguageContext>
  );
}

export function useLanguage() {
  const value = useContext(LanguageContext);
  if (!value) throw new Error('LanguageProvider is required');
  return value;
}

export function LanguageSwitch() {
  const { language, toggle, t } = useLanguage();
  return (
    <button
      type="button"
      className={s.languageButton}
      onPointerDown={(event) => event.preventDefault()}
      onClick={toggle}
      title={t(language === 'zh' ? '切换到英文' : '切换到中文')}
      lang={language === 'zh' ? 'en' : 'zh-CN'}
    >
      {language === 'zh' ? 'English' : '中文'}
    </button>
  );
}
