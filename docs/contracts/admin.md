# Remota 管理员通道契约

管理台与服务端同源，使用独立管理员登录。它管理临时会话、MCP 客户端和凭据，不提供设备 full 授权、命令提交或会话延期。路径前缀为 /api/v1/admin，其他规则见 [通用规则](README.md)，页面行为见 [管理台交互规范](../product/console.md)。

本页 HTTP 绑定由[OpenAPI YAML](openapi.yaml)维护，JSON 请求与响应均在[Schema](schemas/messages.schema.json)覆盖。Cookie 属性、Origin/CSRF 的匹配语义、字节限制及跨资源条件仍按本页与通用规则校验。

## 登录与浏览器身份

POST /login 使用 {password}，成功 200 {csrf_token, expires_at}，并设置 remota_admin Cookie。password 原样比较，不修剪；长度 1 至 256 UTF-8 字节，失败统一 401 UNAUTHENTICATED，不区分账户、口令和摘要状态。

Cookie 为 HttpOnly、SameSite=Strict、Path=/，不设置 Domain；public_url 为 HTTPS 时加 Secure，不信任代理头。期限为创建后的绝对截止，不滑动，默认见 [运行默认值](README.md#运行默认值)。退出、到期、重启或本机重置使登录失效；重新登录只替换当前浏览器会话。

登录本身要求合法 Host 和同源 Origin，匹配规则见 [标识与身份](README.md#标识与身份)。其他读取要求有效 Cookie；所有修改还要求同一 Cookie 会话的 X-CSRF-Token。CSRF 为独立的 32 字节随机量，以无填充 base64url 的 43 字符字符串交付；客户端将 csrf_token 原样回送，仅保存在当前管理会话内存中。错误为 UNAUTHENTICATED、HOST_DENIED、ORIGIN_DENIED、CSRF_INVALID。

登录限流为每个直接连接源地址每分钟 5 次失败，全服务每分钟 30 次失败；超限 429 RATE_LIMITED，Retry-After 为下一可用窗口剩余整秒。不盲信代理 IP 头。管理员会话数量使用通用 [容量限制](README.md#容量与数据限制)，达到上限返回 CAPACITY_EXCEEDED，不静默踢出其他身份。

POST /logout 请求体为 {}，成功 204，撤销当前登录、清 Cookie。会话已失效时返回 401；未取得响应时按 [修改与结果核对](#修改与结果核对)保留结果未知。

## 端点目录

下表从[OpenAPI](openapi.yaml)生成，Schema 名称对应[消息目录](schemas/messages.schema.json)中的同名 $defs；Empty 表示 JSON 空对象，无请求体不等于 Empty。登录成功设置 Cookie；创建、轮换的重复签发关联 ID 为 409。字段细节见相应模型与下文语义。

<!-- BEGIN GENERATED OPENAPI: admin -->
<!-- Generated from openapi.yaml; use --refresh-openapi. -->
| 方法与路径 | 请求 Schema | 成功响应（状态、Schema） |
|---|---|---|
| POST /login | AdminLoginInput | 200 AdminLoginResponse |
| POST /logout | Empty | 204，无 JSON 正文 |
| GET /state | 无请求体 | 200 AdminSnapshot |
| POST /credentials | CredentialCreate | 201 CredentialIssue |
| POST /credentials/{cid}/rotate | CredentialRotate | 201 CredentialIssue |
| POST /credentials/{cid}/revoke | Empty | 200 CredentialRevokeResponse |
| PATCH /sessions/{sid} | NoteInput | 200 AdminSessionResponse |
| POST /sessions/{sid}/end | Empty | 200 SessionResponse |
| POST /clients/{mcl_id}/end | Empty | 200 MCPClientEndResponse |
<!-- END GENERATED OPENAPI: admin -->

创建、轮换和撤销必须在持久管理提交完成后才能报告成功。管理端点不接受 Agent、access 或 MCP token。

## 管理快照

GET /state 返回一个控制核心一致快照：

| 字段 | 类型与内容 |
|---|---|
| server | {version, server_epoch, health, management_revision, public_url?} |
| server_time | 快照生成的服务端 UTC 时间 |
| csrf_token | 当前管理登录的 CSRF |
| admin_session | {expires_at} |
| transport | {public_mode, backend_mode} |
| summary | 下述全量计数 |
| sessions | AdminSession 数组 |
| clients | MCPClient 数组 |
| credentials | Credential 数组 |

health 为 ready 或 degraded。public_url 使用 [服务地址规则](README.md#标识与身份)中的显式配置，未配置时省略。version 为程序版本字符串，server_epoch 对应 X-Remota-Epoch，management_revision 为每次确认成功的持久管理修改递增的非负整数。AdminServer、AdminTransport、AdminSummary 复用 Schema 中对应定义；截止关系、统计正确性及 ID 关联不是结构校验的结论。

summary 为 {live_sessions, cleanup_pending_sessions, ready_clients, active_access_credentials, active_mcp_credentials, expiring_credentials, expiring_sessions}，基于完整快照；cleanup_pending_sessions 统计 cleanup=pending 的 ended 会话，两个 expiring 计数使用 [运行统计窗口](README.md#运行默认值)。

sessions 包含 pending、active 和结束保留期内的 ended 元数据。AdminSession 在 [Session](agent.md#设备会话) 上增加 access_credential_id、note、note_revision，并可包含先前获准 info 调用的 cached_info。初始 note 为空，note_revision=0；结束时的投影遵循 [会话清理规则](agent.md#降权和结束)，清空 note、cached_info，保留关联撤销与清理元数据。

clients 返回 [MCPClient](mcp.md#客户端模型)，字段、长度与状态统一在 MCP 通道定义。

credentials 返回包括撤销与过期记录的全量管理元数据。三个数组分别按 created_at 升序、ID 升序稳定排列。快照不包含管理员摘要、凭据摘要、Agent 身份、命令、reason、输出或一次性秘密。

传输观察 public_mode 为 https、loopback_http、insecure_http、unconfigured；backend_mode 固定 http，描述本服务收到连接的最后一段。

## 凭据模型与一次性交付

Credential 的字段如下。

| 字段 | 约束 |
|---|---|
| id、kind、name | crd ID；kind 为 access 或 mcp；名称规则见通用规则 |
| created_at、expires_at | 服务端时间 |
| status | active、expired、revoked；撤销优先于到期，到期边界为 now >= expires_at |
| revoked_at? | 撤销后出现 |
| allow_exec | 仅 mcp 必填，access 禁止出现 |
| creation_operation_id | 签发关联 ID，随凭据元数据保存，用于查找结果 |
| rotated_from_id? | 轮换来源凭据 ID |
| effects_count | {live_sessions, live_clients, unsettled_tasks}，本快照时刻的关联数量 |

CredentialCreate 为 {operation_id, kind, name, expires_in_seconds?, allow_exec?}。expires_in_seconds 的默认值与范围使用通用 [access/MCP 有效期](README.md#时间与期限)，单位为秒。mcp 默认 allow_exec=false；access 不得出现 allow_exec，即使为 false 也拒绝。

CredentialRotate 为 {operation_id, expires_in_seconds?}。新凭据继承 kind、name 和 allow_exec，期限从签发开始重算，默认值与范围同 CredentialCreate。active 或 expired 可轮换；本次 operation_id 尚无记录而源已 revoked 时为 CREDENTIAL_REVOKED。关联失效见下节。

CredentialIssue 为 {credential, secret, effects, management_revision}。只在首次成功的 201 响应交付 secret；此后从任何接口均不可取回。

```json
{
  "operation_id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "kind": "mcp",
  "name": "诊断助手",
  "expires_in_seconds": 7776000,
  "allow_exec": false
}
```

operation_id 只作签发关联标识，create 与 rotate 共用部署内唯一空间。合法请求若已有关联记录，返回 409 CREDENTIAL_OPERATION_EXISTS，details.resource_id 指向已有凭据；不再次签发或交付秘密。

服务端只保存 creation_operation_id 与凭据元数据，不保存原请求正文、输入指纹或签发响应，也不比较重试正文。重启后可查关联记录，不提供原请求重放。

effects 为 {sessions_ended, clients_ended, tasks_stop_requested}，表示本次核心已触发的数量，不表示进程已退出。重复撤销已经撤销的记录仍成功，effects 可为零；到期不自动删除记录。

## 凭据失效与轮换

access 到期、撤销或轮换结束其全部 pending/active 设备；MCP 到期、撤销或轮换结束关联 MCP 客户端并停止其任务，不结束其他凭据的设备授权。轮换把旧撤销与新记录作为一次持久提交，旧授权不转移。提交期间先阻止旧身份取得新能力；写入失败按 [存储故障规则](README.md#重启与存储故障)进入 STATE_UNAVAILABLE 降级，不恢复内存权限。

首次初始化创建独立随机管理员口令、一枚 access 和一枚 allow_exec=true 的 MCP 凭据，仅一次性交接。后续创建使用 CredentialCreate 的默认权限。本机恢复管理员口令要求服务停止并持有管理目录独占权，产生新口令、使旧管理会话失效；没有远程重置端点。

## 备注及结束操作

备注 PATCH 只允许 pending/active，会话 ended 返回 SESSION_ENDED。expected_note_revision 必须为非负整数且匹配当前值；成功后 note_revision 加 1。相同值也可不增加修订，但响应必须返回当前值。响应中的 AdminSession 包含当前 note、note_revision 及管理投影字段；冲突返回 REVISION_CONFLICT 和 current_revision。

结束设备会话是幂等操作，返回当前 Session；状态与清理按 [设备通道](agent.md#降权和结束)执行。已被清除的旧 ID 为 NOT_FOUND。

结束 MCP 客户端是幂等操作，返回当前 client 与 effects；状态、任务失效及元数据保留按 [MCP 生命周期](mcp.md#初始化与生命周期)执行，保留期后为 NOT_FOUND。

## 修改与结果核对

凭据创建、轮换、撤销、备注修改及结束操作不自动重试；请求超时或中止先刷新快照核对，不能推断服务端没有处理。撤销和结束虽然业务幂等，也不改变这一调用规则。

签发应答丢失时，刷新快照按 creation_operation_id 查新记录；重复提交只取得上述冲突与凭据 ID。秘密不可得则明确撤销该记录后另建凭据，成功撤销的旧凭据不能重新启用。不以新 ID 自动重试未知签发。
