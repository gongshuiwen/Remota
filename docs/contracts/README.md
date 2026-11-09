# Remota 契约总则

版本 Draft 2，日期 2026年11月8日。本文定义客户端、服务端、管理台和 MCP 的当前契约，不要求兼容旧草案，只交付规范。

系统由单实例服务端、主动出站连接的设备 Agent、内嵌管理台和远程 HTTP MCP 入口组成。控制核心统一裁决凭据、会话、授权与任务。设备端独立裁决本机同意、租约和进程执行；任何服务端响应都不能扩大本机授权。

项目总导航见[根 README](../../README.md)，组件、状态归属与部署关系见[整体架构设计](../architecture.md)。本文及领域契约负责协议规则，架构文档只作概述和引用。

## 阅读入口与规则归属

正常流程：设备接入并本机授权 → AI 选择会话并提交命令 → Agent 核对开始许可后执行 → 回报、查询或取消 → 结束会话。

| 文档                                          | 权威定义                                |
| ------------------------------------------- | ----------------------------------- |
| 本文                                          | HTTP、身份格式、时间算法、公共边界、运行默认值和错误        |
| [设备通道](agent.md)                            | Session、本机授权、Lease、Event、设备请求与回执    |
| [任务语义](tasks.md)                            | Task、StartGrant、命令幂等、执行证据和结算        |
| [管理员通道](admin.md)                           | 管理登录、AdminSnapshot、Credential、签发与撤销 |
| [MCP 通道](mcp.md)                            | MCPClient、初始化、工具映射及协议错误             |
| [关键消息 Schema](schemas/messages.schema.json) | 已覆盖消息的字段、类型、必填项与可表达的消息内部约束          |
| [验收规格与样例](acceptance/README.md) | 总体验收清单、正常与异常场景、共用样例和校验入口 |
| [管理台交互](../product/console.md)              | 刷新、草稿、身份隔离、搜索、提醒、复制和语言              |

“必须”和“不得”是约束，“可”表示允许。字段表是阅读摘要，已覆盖消息的结构统一由 Schema 定义；授权、状态转换、UTF-8 字节、跨消息关联、时间算法及未覆盖接口仍在所属领域文档定义。Schema 仅形式化 Draft 2 已有结构，不改变协议行为；调用处通过链接引用完整规则。

消息示例均为独立合成数据，重复 ID 仅用于说明关联，不代表同一条真实时间线；秘密示例不能作为实际凭据使用。

## HTTP 与 JSON

- API 根路径为 `/api/v1/agent`、`/api/v1/admin`，MCP 为 `/mcp`。服务地址必须是无 userinfo、查询串、片段和子路径的根地址。
- 公网入口使用 HTTPS，设备必须验证证书，不跟随携带身份的重定向。字面回环 IP 可用 HTTP；其他明文地址仅在部署者与调用端均显式允许时使用。DNS 名称不自动获得回环例外。
- 设备只发起出站 HTTP 请求。服务监听回环 HTTP，HTTPS 由外部代理终止。
- 请求与响应采用 UTF-8 JSON；有 JSON 请求体时使用 `Content-Type: application/json`，可附 `charset=utf-8`。不接受其他字符集、压缩请求体、重复 JSON 成员、尾随 JSON 值及非有限数字。
- 设备与管理 API 拒绝未定义字段，必须字段不得缺失。除明确标注外，不接受 null；可选字段缺省时省略。MCP 外层遵守标准扩展规则，Remota 工具参数仍拒绝未知字段。
- 整数限定在 JSON 安全整数范围内；字节限制按 JSON 解码后的 UTF-8 字节计算。字符串不隐式做 Unicode 规范化。
- 成功响应直接返回端点声明的对象；无内容响应用 204，不发送 JSON null。错误使用本页的 Error。
- 动态响应含 `Cache-Control: no-store`、`X-Content-Type-Options: nosniff`、`Referrer-Policy: no-referrer`。
- 服务生成 `X-Request-ID` 供定位请求，并在所有应用响应中发送 `X-Remota-Epoch`。后者是每次进程启动重新生成的 32 位小写十六进制字符串，不是凭据。
- 业务 API 不接受未声明的查询参数。未知路由 404，已知路由不支持的方法 405，并发送 Allow。反向代理产生的错误不保证使用 Error，调用方必须先核对 HTTP 状态和 Content-Type。

HTTP 状态表示本次接口处理结果。200、201、202、事件确认和开始许可都不等于命令成功或进程已经退出。

公共入口 GET /healthz 返回 HealthResponse={status, version}：ready 为 200，degraded 或 shutting_down 为 503，不暴露配置或内部错误；结构见[消息 Schema](schemas/messages.schema.json)。GET / 与已打包静态资源路径只提供内嵌页面，不创建任何身份或设备会话。

## 标识与身份

资源 ID 为指定前缀加下划线及 32 位随机小写十六进制，例如 `ses_11111111111111111111111111111111`。资源种类前缀为 ses、tsk、call、evt、grt、crd、mcl。客户端生成的 request_id 和凭据签发 operation_id 为无前缀的 32 位随机小写十六进制；不得从名称、主机名或时间推导。

request_id 标识一次命令意图，task_id 标识服务端任务，grant_id 标识该任务唯一的开始许可。设备操作按会话、任务或调用资源处理重复请求，不另设操作身份。

| 身份                | 形式与用途                                         | 生命周期来源                        |
| ----------------- | --------------------------------------------- | ----------------------------- |
| Access credential | `rm_access_` 加 32 随机字节的无填充 base64url；只申请新设备会话 | [凭据失效与轮换](admin.md#凭据失效与轮换)   |
| MCP credential    | `rm_mcp_` 加同等随机量；只访问 /mcp                     | [凭据失效与轮换](admin.md#凭据失效与轮换)   |
| Agent credential  | `rm_agent_` 加同等随机量；只操作绑定设备会话                  | [结束后的有界清理](agent.md#结束后的有界清理) |
| 管理员会话             | 独立随机口令换取 HttpOnly Cookie                      | [登录与浏览器身份](admin.md#登录与浏览器身份) |
| MCP 传输会话          | MCP-Session-Id 中的独立随机不透明值                     | [初始化与生命周期](mcp.md#初始化与生命周期)   |

管理页面展示的 mcl ID 与 MCP-Session-Id 不同。传输会话标识和 Agent credential 不进入管理快照。持久状态仅保存管理认证摘要、access/MCP 摘要及其元数据；不保存秘密原文。

设备和 MCP 请求只允许一个 Authorization Bearer 头，不接受 Cookie、URL、请求体中的替代身份或重复认证头。管理员 API 不接受 Bearer。秘密前缀仅用于分类，实际鉴权必须验证摘要、用途、有效期及撤销状态。

有效 MCP 凭据可以发现本部署的临时设备，读取获准诊断和活动会话任务详情，也可以取消任务；allow_exec 只限制新命令申请。此版本没有按设备或按任务的读取 ACL。任务发起身份的开始门禁与级联停止按 [任务语义](tasks.md#受理与幂等)执行。

设备与 MCP 接口拒绝携带 Origin 的浏览器请求，返回 403 ORIGIN_DENIED；包括 Origin: null。管理修改请求严格检查同源 Origin、配置 authority 与 CSRF，不提供 CORS。服务不从任意 Forwarded 或 X-Forwarded-* 头推导信任。

服务启动时确定允许的 authority 与管理 Origin：配置 public_url 时采用其 scheme、host、有效端口；无 public_url 时只允许显式回环监听地址形成的 Origin。非回环部署必须配置 public_url。代理必须保持对应 Host。Origin 按 scheme、规范化 host、有效端口精确比较，不允许通配或路径；请求 Host 不在允许集合为 HOST_DENIED。无论 DNS 如何解析，都不以请求中自报的 Host 或 Origin 扩展集合。

## 时间与期限

对外时间统一为带毫秒的 UTC RFC3339 字符串，例如 `2026-11-08T04:00:00.000Z`。到期判断使用 now >= deadline，等于截止时即失效。

服务端裁决绝对截止。Agent 的执行许可使用本机经过时间，并取服务端剩余期限、本机会话期限、执行期限和租约期限的最早值。绝对时间用于显示，不能仅比较两台机器的墙上时钟获得执行权。

服务端返回的 remaining_ms、valid_for_ms 和预算，都由 Agent 绑定到对应请求的本机发送时刻计算。使用“发送时刻 + 剩余预算”，而非“收到响应时刻 received_at + 预算”；响应延迟只会缩短本机窗口。预算必须为正，旧响应、请求取消、权限修订变化或进程 epoch 变化均不能续期。

任务固定执行截止与当前租约的关系由 [开始许可](tasks.md#开始许可)定义。

经过时间必须覆盖系统休眠。不能保证这一点的平台在系统恢复、检测到计时异常或无法证明租约仍有效时结束会话。运行中的期限不得因墙上时钟回拨延长。

服务端同样为运行中的期限保留经过时间上界；墙上时钟提前到达绝对截止或经过时间预算耗尽，任一发生即到期。墙上时钟回拨不重建、延长已确定的上界。Agent 保存的是当前运行上下文的经过时间锚点；同一 grant 的首次锚点规则见 [开始许可](tasks.md#开始许可)。

下表是双方必须遵守的边界；运行默认值在后文单独列出。

| 项目 | 协议边界 |
|---|---|
| 设备会话配置 TTL | 60 至 28800 秒；实际截止取创建时刻加配置 TTL 与 access 到期的较早者，实际剩余量可不足 60 秒 |
| 设备授权租约 | 最多 30000 毫秒，且不晚于会话截止 |
| 开始许可窗口 | 最多 5000 毫秒，且不晚于执行、会话和租约截止 |
| 命令 timeout_seconds | 默认 30，范围 1 至 120 |
| 凭据创建 expires_in_seconds | 默认 7776000 秒，范围 60 至 31536000 秒 |

决定、首次 poll、诊断、offer、开始许可窗口和执行截止不晚于会话截止。停止确认与结束保留属于无授权清理观察，可以晚于 expires_at，不提供执行或续租资格。MCP 空闲期限不超过凭据截止；管理登录使用绝对期限。结束保留期从 ended_at 起算，不因流量延长，也不恢复执行授权。

会话配置 TTL 的下限不延长父 access。有效 access 即使仅剩不足 60 秒，仍可注册被截短的会话；access 已到期则按身份规则拒绝，不创建会话。decision_deadline=min(创建时刻加决定等待, expires_at)，首次有效 poll 截止=min(接受决定时刻加激活窗口, expires_at)。返回预算按实际剩余量计算，响应到达时已无有效预算的 Agent 不进入执行状态。

### 多个截止的裁决顺序

每次核心处理先采样时间、找出当前阶段适用且已经到期的条件，再处理请求。下表裁决设备、MCP 客户端与任务生命周期；管理员登录、诊断回报和保留期清理仍先检查各自期限。候选按已确定的服务端绝对截止升序处理；同一截止使用下表顺序，同一行按列出条件顺序、同种资源按 ID 升序。经过时间上界可以使条件提前成立，排序只决定本轮裁决顺序，不能等待墙钟、重算预算或延后停止。此前已接受的 stop.reason、end_reason、outcome 和 settled_at 不追溯改写。

| 同截止优先级 | 条件及适用范围 | 本轮作用 |
|---|---|---|
| 1 | access 到期，随后为原发起 MCP 凭据到期 | access 结束设备，end_reason=access_invalid；MCP 凭据结束关联客户端；可能启动任务使用 credential_invalid |
| 2 | Session.expires_at，随后为 pending 的决定截止 | 分别以 session_expired、decision_timeout 结束设备；可能启动任务使用 session_ended |
| 3 | MCP 初始化或空闲截止 | 结束对应客户端；其可能启动任务使用 mcp_client_ended |
| 4 | 首次有效 poll 截止或当前租约截止 | 结束旧设备会话，end_reason=lease_expired；可能启动任务使用 session_ended |
| 5 | queued 的 offer_deadline；尚无启动/未启动证据的开始许可窗口 | 前者按等待超时结算；后者请求停止，reason=start_unconfirmed |
| 6 | 已签发许可任务的 execution_deadline | 请求停止，reason=execution_deadline |
| 7 | 已在 stopping 的停止确认截止 | 无回收证据则结算 unknown，按任务规则结束旧会话 |

各条件仍分别执行必要的失效和清理动作；排序只决定首次原因，不跳过父授权检查。本轮才首次接受的停止或结束使用本轮核心采样时刻，不把 requested_at、ended_at 回填为早先截止；处理后重新检查本阶段新增的到期条件。任务一旦有停止原因，后续执行截止不覆盖它；会话已结束，后续执行未知也不覆盖首次结束原因。已有停止确认截止不重置。撤销、降权、主动取消和关闭等非定时操作沿用核心接受顺序，处理前同样先应用本轮已到期条件。确认退出后 cancelled/timed_out 的映射见[任务结果裁决](tasks.md#结果与输出)。

## 运行默认值

这些值用于首版部署和客户端策略，可在满足协议边界的前提下调整，不要求把每一项都做成配置项。设备使用响应中的实际剩余预算，不能据本表自行续期。客户端 HTTP 超时必须覆盖所采用的 poll 等待。

| 参数                         | 建议默认值                             |
| -------------------------- | --------------------------------- |
| 设备会话 TTL                   | 3600 秒                            |
| pending 决定期限、首次有效 poll 窗口  | 120 秒、15 秒                        |
| 普通 HTTP、start、回执请求超时       | 10 秒、5 秒、5 秒                      |
| poll 等待、Agent poll HTTP 超时 | 10 秒、15 秒                         |
| 开始等待、固定诊断等待                | 60 秒、10 秒                         |
| 请求停止后的确认等待                 | 5 秒；结算见 [取消与竞态](tasks.md#取消与竞态)   |
| 结束保留期                      | 300 秒；服务端在 ended_at 确定，流量不续期      |
| MCP 初始化完成、空闲期限             | 30 秒、30 分钟                        |
| 管理登录期限                     | 8 小时，不滑动                          |
| expiring_credentials 统计窗口  | 未撤销凭据剩余大于 0 且不超过 7 天              |
| expiring_sessions 统计窗口     | pending/active 会话剩余大于 0 且不超过 5 分钟 |

## 容量与数据限制

| 项目              | 上限                              |
| --------------- | ------------------------------- |
| 设备、管理员单个请求体     | 256 KiB                         |
| MCP 单个请求或响应     | 1 MiB                           |
| 每个设备会话未决命令      | 1；占用判定见 [受理与幂等](tasks.md#受理与幂等) |
| 每个设备会话并发 poll   | 1                               |
| 命令 command      | 非空白、允许换行、无 NUL，最多 4096 UTF-8 字节 |
| 命令 reason       | 非空白、最多 512 UTF-8 字节             |
| stdout 与 stderr | 各截取最多 8192 原始字节                 |

部署容量的建议默认值如下，调用方通过容量错误处理过载，不将这些值作为消息结构或执行权限。

| 参数 | 建议默认值 |
|---|---|
| 管理快照响应 | 8 MiB |
| pending/active 设备、含保留记录的设备 | 256、512 |
| 可用及初始化中 MCP 客户端、每客户端并发 RPC | 256、16 |
| 管理登录、持久凭据记录 | 8、4096（凭据含撤销记录） |
| 每会话累计任务、固定诊断记录 | 32、16（诊断含结果确认保留） |
| 每次 poll 的事件与 ack ID、普通待交付事件 | 各 16、64 |

容量不足时不丢弃取消、不淘汰仍有幂等意义的记录，也不启动额外进程绕过限制。具体过载按 Error 表返回。

名称先修剪两端 Unicode 空白，保留内部字符；凭据名称为 1 至 80 UTF-8 字节。备注最多 512 字节，可为空清除。名称与备注拒绝 C0 控制字符、DEL；命令约束见上表。设备和第三方值不作为指令、身份或同意依据；终端转义控制字符，网页展示要求见 [管理台交互规范](../product/console.md#身份与秘密)。

空白集合固定为 [Unicode White_Space](https://www.unicode.org/Public/17.0.0/ucd/PropList.txt)：U+0009–U+000D、U+0020、U+0085、U+00A0、U+1680、U+2000–U+200A、U+2028、U+2029、U+202F、U+205F、U+3000，不随宿主语言的 trim 或正则空白类变化。字符串必须为 Unicode 标量序列，孤立代理项拒绝；名称先修剪再检查控制字符，command/reason 不修剪。

## Error

```json
{
  "error": {
    "code": "REVISION_CONFLICT",
    "message": "权限修订已变化",
    "trace_id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "details": {
      "current_revision": 2
    }
  }
}
```

code 为稳定的大写标识，message 仅供显示，不作为分支依据。details 可选，只允许 field、current_revision、resource_id、existing_task_id、limit；resource_id 可用于定位已签发凭据。错误不回显秘密、摘要、命令、输出、文件路径或底层异常。

| HTTP | code | 调用方动作 |
|---|---|---|
| 400 | INVALID_JSON、INVALID_ARGUMENT、UNKNOWN_FIELD、PROTOCOL_VERSION_REQUIRED、PROTOCOL_VERSION_UNSUPPORTED | 修正输入，不自动换协议 |
| 401 | UNAUTHENTICATED | 停止使用当前身份；Agent 本地立即取消工作 |
| 403 | ORIGIN_DENIED、HOST_DENIED、CSRF_INVALID、EXEC_NOT_ALLOWED、LOCAL_CONSENT_REQUIRED、LOCAL_RECONSENT_REQUIRED | 停止相关操作，不通过重试提升权限 |
| 404 | NOT_FOUND、TASK_NOT_FOUND、MCP_SESSION_NOT_FOUND | 查询来源或显式建立新 MCP 会话；禁止重放原命令 |
| 409 | IDEMPOTENCY_CONFLICT、REVISION_CONFLICT、INVALID_STATE、TASK_BUSY、ALREADY_POLLING、RESULT_CONFLICT、CREDENTIAL_REVOKED、CREDENTIAL_OPERATION_EXISTS | 核对当前资源；不能换请求 ID 绕过冲突 |
| 410 | SESSION_ENDED、GRANT_EXPIRED、CALL_EXPIRED | 停止旧工作；新设备授权须重新接入 |
| 413 | PAYLOAD_TOO_LARGE | 缩小请求 |
| 415 | UNSUPPORTED_MEDIA_TYPE | 修正请求类型 |
| 429 | RATE_LIMITED、TASK_LIMIT、CREDENTIAL_LIMIT | 遵守 Retry-After；永久会话配额不靠等待恢复 |
| 500 | INTERNAL_ERROR | 视修改结果为未知，按原资源核对 |
| 503 | CAPACITY_EXCEEDED、AGENT_UNAVAILABLE、STATE_UNAVAILABLE、SERVER_STOPPING | 有界等待或本机修复；不能盲重试修改 |
| 504 | DIAGNOSTIC_TIMEOUT | 固定诊断未获结果；不表示命令超时或退出 |

先检查传输、身份、资源绑定和当前权限，再按业务键判断重复请求；状态前置条件与变更在同一核心一致性边界完成。重复处理不跳过鉴权、不恢复旧授权或重交秘密；未认证请求不泄漏资源存在性。

## 重试与结果未知

只读查询可在当前身份有效时有界重试。未取得成功响应的修改操作必须保留结果未知；幂等表示重复请求的业务效果受约束，不自动授予重试、重新授权或重放权限。

| 操作 | 重试与核对规则来源 |
|---|---|
| 注册、决定、降权、关闭 | [注册和本机决定](agent.md#注册和本机决定)、[降权和结束](agent.md#降权和结束) |
| poll 与设备回执 | [poll 与租约](agent.md#poll-与租约)、[任务请求与回执](agent.md#任务请求与回执) |
| 命令提交、开始许可、完整结果 | [受理与幂等](tasks.md#受理与幂等)、[开始许可](tasks.md#开始许可)、[结果与输出](tasks.md#结果与输出) |
| 管理修改与一次性秘密 | [修改与结果核对](admin.md#修改与结果核对) |
| MCP 初始化与传输失效 | [异常后的调用方行为](mcp.md#异常后的调用方行为) |

命令幂等仅覆盖本次服务进程和会话保留期，规则见任务文档。凭据的 operation_id 只用于关联与拒绝重复签发，见管理员通道；它不是通用请求重放键。

## 重启与存储故障

服务正常重启保留已成功提交的管理状态和撤销记录，清空设备、任务、Agent 身份、管理员登录和 MCP 会话。若管理写入失败，立即收紧相关内存权限并进入降级，结束活动授权，禁止新接入、新任务和续租；继续接受停止、清理回执、已有管理员的只读快照和退出。失败撤销不得报告持久成功，也不得声称其跨重启已生效；本机恢复须核对最后成功提交的凭据状态并完成必要轮换。损坏、未知版本或初始化后丢失状态必须拒绝启动，不能重建默认身份。

持久提交区分确认成功、确认未提交、结果未知。只有确认成功才能报告持久成功；超时、响应丢失或存储错误本身不能证明事务未提交。确认失败和结果未知都按上述降级规则收紧权限；结果未知不得据此重做签发、恢复权限或宣称已经回滚。恢复时核实实际持久状态，签发结果继续按原 operation_id 核对。该分类属于内部提交语义，不新增 HTTP 字段或远程恢复接口。

### 初始化与恢复

以下为本机管理操作的行为约定，具体命令名称和状态文件格式由实现选择，不增加远程接口。

| 操作 | 必须满足的条件与结果 |
|---|---|
| 显式首次初始化 | 服务未运行、取得管理目录独占权、目标为新空目录且操作者明确初始化。一次原子提交认证摘要与初始凭据元数据，再一次性交接秘密；不覆盖已有、损坏或未知版本的状态 |
| 普通启动 | 只加载完整、受支持且可核实的管理状态并取得目录独占权；空目录、缺文件、校验失败均拒绝启动，不能转为首次初始化 |
| 初始化提交成功但秘密交付失败 | 已有状态仍按已初始化处理。通过本机管理员口令恢复进入管理，核对并撤销不可得的凭据，再显式签发；不重新初始化或重交旧秘密 |
| degraded 后恢复 | 停止服务并取得目录独占权，核实最后成功提交状态及未完成撤销；完成必要的持久撤销或轮换、确认存储可用后重新启动。一次读写成功或新 poll 不自动恢复 ready |
| 状态无法核实 | 保持拒绝启动，恢复可信的管理状态后核对期间需要收紧的凭据；明确建立新部署必须由操作者另行执行初始化，不复用旧运行上下文或宣称旧撤销已落盘 |

上述恢复均产生新进程 epoch，不恢复设备授权、任务或登录。既有状态的检查、备份或恢复材料按管理身份保护，不包含可重新交付的秘密原文。

## 验收入口

全量清单、正常与异常的具体场景统一见[验收规格](acceptance/README.md#总体验收清单)；静态检查使用其[最小校验入口](acceptance/README.md#最小校验入口)。协议规则仍以本文及所属领域契约为准，验收资料不表示产品场景已经执行。

## 参考来源与设计差异

功能范围参考 [Remote Assist 产品](https://github.com/gongshuiwen/remote-assist-docs/blob/94baab0208f1de7f21768acf5b56ea99c30b9590/content/docs/product/positioning.md)以及固定 [API 修订](https://github.com/gongshuiwen/remote-assist-api/tree/23d12fec5d3bd639c3db937f160af11275d7d42f/api)，代码观察基于 remote-assist 提交 f0d3c478241187038f6aee5027d5f9740de36967。

Draft 2 将任务收敛为一次许可和一次执行，设备操作按资源处理重复消息，清理状态由任务证据派生，凭据仅保留签发关联信息。任务结果与退出证据、本机授权、有限租约及禁止重放仍是独立约束。

MCP 外层依据固定 [Streamable HTTP](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports)、[生命周期](https://modelcontextprotocol.io/specification/2025-11-25/basic/lifecycle)、[工具](https://modelcontextprotocol.io/specification/2025-11-25/server/tools)和[取消](https://modelcontextprotocol.io/specification/2025-11-25/basic/utilities/cancellation)规范。静态 Bearer 为本产品部署接入方式，区别于标准中的 OAuth 授权流程，见 [MCP 授权](https://modelcontextprotocol.io/specification/2025-11-25/basic/authorization)。
