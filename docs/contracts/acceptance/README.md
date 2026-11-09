# Remota Draft 2 契约验收规格

本规格用于第一阶段的消息校验与后续客户端、服务端联合验收。当前没有产品实现；下列运行场景是验收要求，尚未执行。协议规则以[契约入口](../README.md)及其领域文档为准，本页只组织输入、故障注入与预期观察。

## Schema 与样例使用

[messages.schema.json](../schemas/messages.schema.json)采用 JSON Schema 2020-12，根对象是定义目录。校验消息时必须选择具体的 `$defs`，不能仅用目录根对象校验。例如 Registration 的引用为 `urn:remota:draft2:messages#/$defs/Registration`；解析器需先把该 URN 注册为本地 Schema。

[messages.json](messages.json)包含 `valid`、`invalid` 两组独立样例。每项的 `model` 指向 Schema 定义，`value` 是实际消息；`invalid` 另含预期拒绝原因。工具响应样例的 `tool` 是校验元数据，不进入实际消息，用于选取原工具的 outputSchema。它们是校验数据，不是实际网络记录；合成秘密不得用于部署。场景中的样例 ID 都指向该文件，表示对应时刻的消息快照。

同文件的 raw_json 组供 Python 与 Go 共同验证解析边界。每项包含 id、json 或 hex（二选一，后者表示原始字节）、expected，以及可选 model。expected=invalid_json 要求在严格解析时拒绝；invalid_model 要求解析成功后由指定 Schema 拒绝；valid 要求解析成功并通过指定模型（若有）。json/hex、expected 和 model 均为测试元数据，不是协议字段。

[tools-list.json](tools-list.json)是完整的独立 tools/list 应答，每个工具的 Schema 均可单独解析。嵌入定义由最小校验入口从权威目录刷新，不手工维护第二份规则。

| 范围 | 主要 Schema 定义 |
|---|---|
| 设备接入与授权 | Session、Registration、DecisionInput、DecisionResponse、ConsentReduction、CloseInput、SessionResponse |
| 租约与事件 | PollInput、PollResult、Lease、Event 及四种具体事件 |
| 命令与执行事实 | TaskSpec、TaskMeta、TaskDetail、StartInput、StartGrant、StartResponse、ReceiptInput、ResultInput、TaskResponse |
| 固定诊断 | PingResult、AgentInfo、CallResultInput |
| MCP | MCPRequest、MCPToolRequest、MCPToolCall 分层；初始化、工具返回、tools/list 和各工具的结构化正文 |
| 管理 JSON | AdminLoginInput/Response、AdminSnapshot、AdminServer、AdminTransport、AdminSummary、CredentialCreate/Rotate/Issue、CredentialRevokeResponse、AdminSessionResponse、MCPClientEndResponse；空请求复用 Empty、结束设备响应复用 SessionResponse |
| 公共健康消息 | HealthResponse；HTTP 状态仍按通用规则检查 |
| 公共错误 | Error、ErrorBody |

Schema 覆盖设备、任务、管理 JSON、健康消息及 Remota 使用的 MCP 消息，不替代全部 HTTP/MCP 标准、静态资源或浏览器交互。端点、Cookie/CSRF、HTTP 状态与成功状态仍查各通道目录。

校验器必须支持 Draft 2020-12、组合 Schema 的 `unevaluatedProperties`，并启用 `date-time` 格式断言。JSON 解析阶段先拒绝非法 UTF-8、重复成员、尾随值和非有限数字；Schema 无法从解析后的对象恢复这些事实。数字必须保留精度再判断数学值，1.0、1e0 可作为整数，1.00000000000000001 与 9007199254740990.9 不能舍入成合法 RPC ID。1e400 是有限 JSON 数字，但不满足 RPC ID 的安全整数范围。

## 最小校验入口

在已激活的独立 Python 3.11+ 环境中，从仓库根目录执行：

```text
python -m pip install -r tools/requirements-contracts.txt
python tools/validate_contracts.py
```

[校验脚本](../../../tools/validate_contracts.py)只检查静态契约、样例与本地文档链接，不启动服务、不执行 command，也不发起网络请求；首次安装依赖需要包下载。[依赖文件](../../../tools/requirements-contracts.txt)固定校验依赖及传递依赖版本；Python 仅是校验工具的运行环境，不限定产品实现语言。

检查包括：Schema 自身与本地引用、每个正反样例与 raw_json 解析/模型预期、文档 JSON 示例、MCP 文本与结构化正文一致性、原工具输入输出形态、八工具发现结果、自包含 Schema 与权威定义一致性，以及根 README 和 docs 下文档的链接与锚点。输出 PASS/FAIL 和计数；退出码 0 表示全部通过，1 表示文件或校验失败，2 表示缺少依赖或命令用法错误。Go 验证模块复用同一批数据，入口见[实施规划](../../implementation.md#复跑入口与收口条件)。

修改 Schema 后，用下列命令刷新 tools/list 中的嵌入定义并执行同一校验；默认命令只读，刷新选项只改该样例的 inputSchema/outputSchema：

```text
python tools/validate_contracts.py --refresh-tools-list
```

静态通过不代表时序、鉴权、真实执行或进程回收已经通过运行验收。

## 必须另行检查的业务条件

| 检查 | 验收要求 |
|---|---|
| UTF-8 字节与名称规范化 | `maxLength` 只作必要的字符上限，字节限制按通用规则检查；凭据名称先修剪两端空白，再检查字节数与控制字符。命令不修剪 |
| 同一资源的关联 | 核对路径、Session、Task、grant、事件、发起凭据、MCP 客户端、完整 spec 与权限修订；格式相同不代表绑定正确 |
| 时间与请求上下文 | 核对所有截止的先后与上界、lease seq 单调性、epoch、本机请求代际、请求发送时刻以及取消状态；到期边界按 `now >= deadline` |
| 授权与状态转换 | 同一核心边界内检查当前权限、重复键、状态及停止顺序；Agent 在 spawn 前再次核验本机条件 |
| 输出与事实 | 核对原始捕获字节、替换后的 UTF-8 文本上限、持续排空行为和真实回收证据；字段合法不证明进程已退出 |
| 派生值与保留 | capabilities、执行证据、cleanup 和详情可见性必须符合上下文；结束后的元数据不得泄漏正文、输出、备注或缓存信息 |
| 管理快照与 HTTP | 核对 summary、effects_count、排序、ID 关联、server_epoch 与响应头、公网根地址及 CSRF 的有效编码；状态码、Cookie 属性与 Origin/CSRF 失败不能由 JSON Schema 代替 |
| MCP 返回 | 按原工具选择对应正文 Schema；text 解析后必须与 structuredContent 相等。通用响应联合类型只证明属于某种工具结果 |
| 一次性秘密与容量 | 校验凭据秘密的有效编码和角色、首次交付及持久提交；部署默认容量不是固定消息数组上限 |

例如 command 为“中”重复 1366 次时，字符数可通过 TaskSpec 的结构检查，但 UTF-8 为 4098 字节，业务校验必须拒绝为 INVALID_ARGUMENT。过期、撤权及幂等冲突请求也可能完全通过 Schema，不能移入 `invalid` 组冒充结构错误。JSON Schema 的字符长度与格式机制见[验证规范](https://json-schema.org/draft/2020-12/json-schema-validation)。

## 场景约定与覆盖

每个场景独立清空临时状态；所用 ID 可跨场景重复。同一场景内保持资源绑定。`t+N` 为从 `2026-11-08T04:00:00.000Z` 起的秒数，设备与服务端经过时间由验收环境分别控制，不依赖真实等待或同步墙上时钟。

样例中的 RPC id 为占位值，同一 MCP 传输会话内每次新 RPC 使用新 id，并让响应回显当前值；重复命令意图的 request_id 与完整 TaskSpec 保持不变。RPC id 不作为任务幂等键。

除另述外，access 与 allow_exec=true 的 MCP 凭据有效，MCP 客户端 ready，服务存储健康。full 来自模拟本机交互的二次确认；该模拟是验收环境能力，不能成为产品远程授权端点或 CLI 绕过。

基础短命令在 t+3 受理，t+4 发许可，启动窗口到 t+9，固定执行截止 t+14，timeout_seconds=10。长命令改为 120 秒、固定截止 t+124。poll.offer 在 t+3 签发 seq=2、截止 t+33 的租约。HTTP 与 MCP 的错误分层按[MCP 错误规则](../mcp.md#错误分层)观察。

| 类别 | 用例 |
|---|---|
| 正常 | N01 只读接入与诊断；N02 完整命令；N03 长命令续租 |
| 重复提交 | D01 相同意图；D02 冲突意图或发起凭据 |
| 开始许可丢失 | G01 应答丢失、超时与迟到 |
| 结果丢失 | R01 结果重传与冲突 |
| 撤权竞态 | V01 许可前远端撤权；V02 许可后本机降权；V03 运行中远端撤权 |
| 租约到期 | L01 断联且租约届满；L02 迟到续租响应 |
| 服务端重启 | S01 临时上下文失效与显式重新接入 |
| 补充边界 | B01–B08 覆盖 MCP 分层、许可、裁决、乱序、到期、时钟、执行器与初始化恢复 |

每个用例都记录：HTTP/RPC 结果、任务与会话状态、本机授权、累计 spawn 次数、执行证据和敏感字段可见性。许可、事件 ack、请求停止成功都不能算退出确认。网络注入允许分别丢弃请求或应答，不能将两者混为一个故障。

## 总体验收清单

本节是验收覆盖的统一入口；下文的正常流程、六类异常和补充边界提供详细步骤与故障注入。协议规则仍查各领域契约，页面身份隔离依据[管理台交互](../../product/console.md#身份与秘密)。下表保留跨通道要求与凭据签发等独有条目，不能只完成六类异常而忽略其他条目。

| 场景                      | 必须观察到的结果                           |
| ----------------------- | ---------------------------------- |
| pending 或 readonly 提交命令 | 拒绝受理，无许可和命令进程                      |
| 管理员或 MCP 请求 full        | 没有对应授权接口；设备只能本机重新接入提升              |
| 同 request_id 同 spec 重试  | 原 task_id 与原期限，不新增执行               |
| 同 request_id 不同 spec    | IDEMPOTENCY_CONFLICT               |
| 同时撤销凭据与申请开始             | 控制核心确定顺序；撤销先发生则不发许可                |
| 许可已发后发生撤销               | 新能力失效，停止指令与租约收紧；不声称跨机瞬时退出          |
| 开始许可响应丢失                | Agent 不启动，补交 not_started；不重领许可后执行  |
| 事件重复或重复回报               | 按 task_id/call_id 和固定事实去重，不新增操作身份  |
| poll 超时、旧响应晚到           | 先取消本机工作；旧响应不续租或恢复权限                |
| full 降权后旧许可到达           | 修订不匹配，拒绝启动                         |
| 最大执行时长的命令持续正常续租         | 租约窗口不误截断固定任务期限                     |
| 任务取消但没有退出回执             | stopping 后 unknown，旧会话结束，清理状态仍明确未知 |
| 结束后收到含输出的结果             | 拒绝完整结果；仅接受无输出退出证据                  |
| 服务端重启                   | 原 Agent 和 MCP 上下文失效，不重放或恢复授权       |
| 凭据签发应答丢失                | 可按 operation_id 核对元数据，不能取回旧秘密      |
| 重复凭据签发关联 ID             | 返回已有凭据 ID 的冲突，不重放签发响应              |
| 撤销落盘失败                  | 不报成功，立即收紧内存权限，健康检查 503             |
| 管理台退出、身份失效或重新登录 | 退出按页面规则清除敏感状态；旧请求、旧 CSRF、旧创建结果和剪贴板回调不能更新后续登录的界面 |

## 正常流程

### N01 只读接入与固定诊断

前置：无设备会话，累计 spawn=0。

| 步骤 | 输入或动作 | 预期观察 |
|---|---|---|
| 1 | access 注册 `{}` | HTTP 201，registration；Session pending，尚无系统探测和命令执行 |
| 2 | 本机选择 readonly，提交 decision.readonly | HTTP 200，Session 为 session.readonly；确认前仍无能力 |
| 3 | poll.input，取得 poll.readonly | HTTP 200，有当前修订的 lease，能力仅 ping/info |
| 4 | MCP initialize、initialized 后调用 ping_agent/get_agent_info | 初始化 200、通知 202；每次诊断使用独立 call_id；设备接受匹配的 call.request，提交 call.ping/call.info 得 204；工具返回 mcp.ping/mcp.info |
| 5 | 提交结构合法的 mcp.request_command | HTTP 200，业务失败 mcp.consent，isError=true；无 Task、grant 或 spawn |
| 6 | Agent 先结束本机授权，再提交 close.local | HTTP 200，ended 且 cleanup=not_needed；无输出或缓存设备信息可读 |

结论：只读诊断正常；累计 spawn 始终为 0。pending 状态提前进行第 4、5 步同样不得取得诊断信息或命令权限，返回 LOCAL_CONSENT_REQUIRED。

### N02 full 授权到命令成功

前置：本机二次确认 full，decision.response 与首个有效 poll.full 已建立；短命令。

| 步骤 | 输入或动作 | 预期观察 |
|---|---|---|
| 1 | t+3 提交 mcp.request_command | HTTP 200，mcp.accepted；Task queued/not_started，accepted 不表示命令完成 |
| 2 | Agent poll 得到 poll.offer | 先记 task_id；只获得正文尚不执行，spawn=0 |
| 3 | t+4 提交 start.input | HTTP 200，start.response；单份 grant，Task start_granted/may_have_started |
| 4 | 再核验本机 full、修订、spec、租约和预算，创建进程 | 累计 spawn=1；提交 receipt.started 得 200 TaskResponse，Task 为 task.running |
| 5 | t+4.2 确认退出回收，提交 result.succeeded | HTTP 200，task.result.response；settled/succeeded/exited，退出码 0 |
| 6 | 按原 task_id 或 request_id 查询 | HTTP 200，mcp.task.succeeded；TaskDetail 含原 spec 和有限输出，isError=false |

结论：同一任务只启动一次，settled_at=t+4.2。各请求自身不续租；会话保持 active/full，只有成功 poll 更新租约。

### N03 长命令持续续租

前置：与 N02 相同，改用 mcp.command.long、poll.offer.long、start.response.long；t+4.1 报告 started。

| 步骤 | 输入或动作 | 预期观察 |
|---|---|---|
| 1 | t+12、22、32、42、52、62、72、82、92 成功 poll | lease seq 从 3 递增到 11；代表快照为 poll.renew.12、poll.renew.22、poll.renew.92，其他按实际时刻生成同结构消息 |
| 2 | 持续观察到 t+100 | 每时刻均有当前有效租约；原 t+33 租约不永久截断任务；固定 execution_deadline 始终为 t+124 |
| 3 | t+100 确认退出，提交与 result.succeeded 相同结构的成功结果 | HTTP 200，Task 为 task.long.succeeded；累计 spawn=1，grant_id 不变 |

结论：续租不创建新许可、不重启命令、不延长固定执行期限。

## 六类异常

### D01 相同意图重复提交

前置：N02 已完成受理，Task queued；当前原发起凭据及申请权限仍有效。

| 步骤 | 输入或故障 | 预期观察 |
|---|---|---|
| 1 | 丢弃首次 mcp.accepted 的应答 | 客户端保留原 request_id，不判断未受理 |
| 2 | 用 mcp.query.request_id 核对 | 查询成功取得原 Task；不重放执行 |
| 3 | 单独验证同一凭据重复提交完全相同请求 | HTTP 200，mcp.reused；原 task_id、TaskSpec、initiator、created_at、offer_deadline 均不变 |
| 4 | 交付重复 offer；在原窗口仍有效且尚为 start_granted 时模拟重复 start 交付 | Agent 忽略已领取 offer；服务端只返回原 grant 和原预算，不新增事件或许可；Agent 沿用首次锚点 |
| 5 | 继续正常执行 | 累计 spawn=1；重复查询、事件或请求不产生第二次启动 |

若第 2 步查不到且查询本身成功，只有当前权限仍有效时才能用原 ID、原完整 spec 再提交；查询失败则继续保持未知。

### D02 同一意图键发生冲突

前置：原 Task 已受理，相关调用身份仍有申请权限。

| 变体 | 输入 | 预期观察 |
|---|---|---|
| A | 保持 session_id/request_id，分别修改 command、reason、shell 或 timeout_seconds | 每次均为 HTTP 200 的 mcp.conflict，code=IDEMPOTENCY_CONFLICT，isError=true |
| B | 使用另一个有效且 allow_exec=true 的 MCP 凭据提交同 ID、同 spec | 同上；不转移原任务所有权 |
| C | 权限先失效，再提交相同 ID/spec | 先按当前身份及权限失败，不能命中缓存取得旧许可 |

结论：原 Task、grant、期限与发起身份不变，不新增进程。变体 A、B 的请求结构本身可以合法。

### G01 开始许可应答丢失

前置：短命令已领取，spawn=0。

| 步骤 | 输入或故障 | 预期观察 |
|---|---|---|
| 1 | t+4 start 已在服务端提交并生成 grant；丢弃应答，t+4.05 连接中断 | 服务端可能处于 start_granted；Agent 将该 task_id 标为放弃启动 |
| 2 | t+4.2 提交 receipt.not_started，reason=GRANT_UNAVAILABLE，无未核验的 grant_id | HTTP 200，Task 为 task.not_started：settled/cancelled/not_started |
| 3 | t+4.3 将原 start.response 交给 Agent，许可表面上尚未过期 | 忽略旧请求应答；不得启动、重新领取许可或清除放弃记录 |
| 4 | MCP 按原 ID 查询 | mcp.task.not_started；累计 spawn=0，不能误报命令成功 |

超时变体：不提前断开，保持应答至 start HTTP 超时后再交付；相同放弃规则适用。未取得确认的 not_started 可以按原事实重传，Agent 始终不执行该任务。

### R01 完整结果的应答丢失

前置：N02 已启动且退出回收，累计 spawn=1，身份及 full 修订仍有效。

| 步骤 | 输入或故障 | 预期观察 |
|---|---|---|
| 1 | t+4.2 服务端接受 result.succeeded，丢弃 200 应答 | 服务端已为 task.succeeded；Agent 不能重新执行来补结果 |
| 2 | t+4.3 重传完全相同 ResultInput | HTTP 200，返回既有 TaskMeta；settled_at、exit_observed_at 仍为 t+4.2 |
| 3 | 只改变 stdout 后重传 | HTTP 409，error.result_conflict；保留第一份结果 |
| 4 | 查询原 task_id/request_id | mcp.task.succeeded；文本与结构化内容一致，累计 spawn=1 |

请求丢失变体：首次结果未到服务端时，相同正文重传才首次结算。结束或身份收紧变体：不再重交输出，只能按设备契约补交无输出事实。

### V01 远端撤权先于开始许可

前置：Task queued，尚无 grant，spawn=0。

| 步骤 | 输入或故障 | 预期观察 |
|---|---|---|
| 1 | t+4 核心先提交 access 撤销 | Session 为 session.revoked.no_grant；Task 为 task.revoked_before_grant，cleanup=not_needed |
| 2 | t+4.1 原 Agent 提交 start.input | HTTP 410，error.ended；结束保留期内原 Agent 身份仍只允许有界清理 |
| 3 | Agent 收到结束结果 | 本机 ended；无 grant、无 spawn；命令正文等敏感内容已清理 |

结论：核心顺序明确，撤权先发生则不发许可；不能用原 offer 或幂等缓存绕过。

### V02 发许可后、spawn 前本机降权

前置：t+4 已取得 start.response，尚未 spawn。

| 步骤 | 输入或故障 | 预期观察 |
|---|---|---|
| 1 | t+4.05 本机选择降为 readonly | 立即收紧本机并取消工作，不等待服务端确认 |
| 2 | t+4.06 发送 consent.reduce | HTTP 200，consent.response；Session 为 session.reduced，修订变为 2，核心接受 local_revoked 停止条件 |
| 3 | 将修订 1 的旧许可或旧 poll 应答再次交付 | 不恢复 full、不更新本机租约，不创建命令进程 |
| 4 | t+4.2 提交 receipt.local_revoked | HTTP 200，Task 为 task.local_cancelled，cancelled/not_started |
| 5 | 重复原 decision.full | 返回当前 readonly Session，不恢复 full；累计 spawn=0 |

结论：服务端可以保持 active/readonly；提升执行权需要重新接入并取得新的本机同意。

### V03 命令运行中发生远端撤权

前置：短命令 t+4.1 已报告 started，累计 spawn=1。

| 步骤 | 输入或故障 | 预期观察 |
|---|---|---|
| 1 | t+4.3 撤销 access；丢弃 event.race.end/event.race.cancel 的交付 | Session 为 session.race.pending；Task 为 task.race.stopping；新能力立即失效，现有进程退出尚未确认 |
| 2 | 到 t+9.3 仍无退出证据 | Task 为 task.race.unknown；Session 为 session.race.unknown，首次 end_reason 仍为 access_invalid |
| 3 | t+9.4 Agent 的 poll 失败 | 立即取消本机工作；停止指令丢失不能使 Agent 延长租约或重启任务 |
| 4 | t+9.5 执行器确认退出；t+10 网络恢复，提交 receipt.late_exit | 有界清理返回 200；Task 为 task.race.late_exit，业务 outcome 仍 unknown、settled_at 仍 t+9.3，execution 改为 exited |
| 5 | 查询 Session/Task | cleanup=confirmed，details_available=false；无命令正文、reason、stdout/stderr、备注或缓存 info |

完整 result 在 ended 返回 HTTP 410 SESSION_ENDED；新 poll 不续租。累计 spawn=1；撤权成功响应不被验收为 OS 瞬时停止。

### L01 断联后租约届满

前置：长命令运行中，固定截止 t+124，最后成功租约到 t+33。

| 步骤 | 输入或故障 | 预期观察 |
|---|---|---|
| 1 | t+5 poll 在到达服务端前失败；后续双向请求均失败 | Agent 立即进入 reconnecting、请求停止当前进程并暂停新执行；服务端未续租 |
| 2 | 不向服务端交付任何退出证据，到 t+33 | 独立看门狗使本机 ended；服务端以 lease_expired 结束 Session，对可能启动的任务请求停止 |
| 3 | 到 t+38 停止确认等待届满 | Task 为 task.lease.unknown；Session 为 session.lease.unknown，cleanup=unknown |
| 4 | 网络恢复，查询并交付旧 offer | 不恢复授权、不重启任务；有保留的退出事实时只能无输出补报 |

结论：累计 spawn=1。即使本机已发停止请求，服务端仍不能伪造 exited；固定任务截止较晚也不能跨越失效租约继续执行。

### L02 续租应答迟到且请求已取消

前置：长命令运行中，Agent 已接受的租约到 t+33；服务端与设备的观察可不同。

| 步骤 | 输入或故障 | 预期观察 |
|---|---|---|
| 1 | t+4 发送 poll；服务端 t+14 生成 poll.late，阻塞其交付 | 服务端租约为 seq=3、到 t+44；设备尚未取得该租约 |
| 2 | t+19 客户端 poll 超时并取消请求 | Agent 立即取消工作；独立看门狗仍按最后已接受租约运行 |
| 3 | t+33 设备原租约到期 | 本机 ended；不得自动注册或恢复执行 |
| 4 | t+34 交付结构合法的 poll.late | 即使 seq 更大且服务端 expires_at 仍未到，也必须丢弃：请求已取消、本机已结束；预算按 t+4 发送时刻起算，到 t+34 同样已失效 |
| 5 | t+35 尽力发送 close.lease_expired；仍无退出证据，到 t+40 | close 为 200 ended；最终 Session 为 session.lease35.unknown，Task 为 task.lease40.unknown |

结论：有效 JSON、较大 seq 和较新的服务端截止都不能单独恢复本机授权。累计 spawn=1；接收时刻不能成为新租期起点。

### S01 服务端重启

前置：full 会话与长命令已启动，管理凭据已持久提交，旧 epoch 为 32 个 d。

| 步骤 | 输入或故障 | 预期观察 |
|---|---|---|
| 1 | t+5 重启服务，epoch 改为 32 个 e | 成功提交的管理凭据与撤销记录保留；设备、任务、Agent 身份、管理员登录、MCP 传输上下文清空 |
| 2 | 使用有效旧 MCP Bearer 和旧 MCP-Session-Id 请求 | HTTP 404，error.mcp_missing；不得自动重放 request_command |
| 3 | 原 Agent 请求到新服务 | 原 Agent 身份已不存在，HTTP 401，error.unauthenticated；Agent 因身份失效或观察到新 epoch 立即结束旧授权并取消工作 |
| 4 | 使用有效 MCP Bearer 显式重新 initialize/initialized | 新传输上下文与新 mcl；list_sessions 为 mcp.sessions.empty，查询旧 Session 的任务为 mcp.not_found；重建传输不恢复任务 |
| 5 | 管理员重新登录并查看持久元数据 | 原有效凭据仍可用，已撤销凭据仍失效；秘密原文不可取回 |
| 6 | t+100 操作者显式启动新的 Agent 接入 | 201 registration.new，新 session_id、新 Agent 身份，state=pending；尚无 full 或执行权 |

结论：旧 Task 不跨 epoch 恢复或重放，旧 Agent 累计 spawn 不增加。若新服务不可达，Agent 仍受旧本机租约约束，无法观察新 epoch 不构成延期。

## 补充边界验收

以下各变体独立运行；结构错误、合法结构下的业务失败和实际进程行为分别观察，不能用一个 Schema 通过或拒绝替代整个用例。

### B01 MCP 标准消息与错误分层

| 输入或动作 | 预期观察 |
|---|---|
| mcp.list_sessions.omitted_arguments | 外层及完整调用均合法；按空对象调用 list_sessions，HTTP 200 成功 |
| mcp.initialized.meta | 标准 _meta 可接受，HTTP 202，无 RPC 正文，不改变权限 |
| mcp.integer_id；对应 id=1.5、null 或超出安全整数范围的变体 | 整数原样回显；后三者为 HTTP 400/-32600，不进入工具业务 |
| mcp.envelope.null_arguments | 外层 arguments 类型错误，HTTP 200/-32602 |
| mcp.envelope.business_error | MCPToolRequest 合法，所选工具参数不合法；HTTP 200，isError=true、UNKNOWN_FIELD，不返回 -32602 |
| mcp.envelope.missing_arguments | 外层合法，按空对象校验 get_session；HTTP 200，isError=true、INVALID_ARGUMENT |
| mcp.envelope.unknown_tool；原增强执行参数样例 | 外层可解析，Remota 不支持该工具或增强执行；HTTP 200/-32602 |
| 发送 mcp.tools.list | 与 tools-list.json 同形，恰好八个不同名字；每个 inputSchema/outputSchema 独立解析且与权威定义相同，不需要远端引用 |

### B02 重复许可与首次锚点

前置：短命令，首次 start 在本机 t+4 发送并获许可；服务端保持 start_granted，尚未 spawn。

| 步骤 | 预期观察 |
|---|---|
| t+8 模拟同一 start 请求的重复交付 | 返回原 start.response，所有 grant 字段不变；本机开始截止仍为 t+9、执行截止仍为 t+14 |
| t+8.1 正常创建一次进程并报告 started；t+8.2 再交付 start | HTTP 409/INVALID_STATE，无 StartResponse，无第二次 spawn |
| t+9 再交付 start，当前身份与会话仍有效 | HTTP 410/GRANT_EXPIRED，窗口到期检查优先；原运行任务不因这个重复请求被重新启动或改写 |
| Agent 首次锚点丢失，或已在 G01 标记放弃 | 即使拿到结构合法的相同 grant，也不计算新窗口、不 spawn |

### B03 本机停止与服务端结果裁决

前置：full、身份及租约有效，命令已报告 started 并已确认回收；每行使用独立任务上下文。

| 核心状态与输入 | 预期观察 |
|---|---|
| poll 失败使本机停止，核心尚未接受停止原因；t+4.2 收到 result.agent_cancelled，exit_code=0 | Task 为 task.agent_cancelled：cancelled/exited，stop 省略，不能判为 succeeded |
| 首次 start 发送 t+3.8、服务端签发 t+4，本机固定截止为 t+13.8；收到 result.agent_timed_out | Task 为 task.agent_timed_out：timed_out/exited，stop 省略；不等待服务端 t+14 截止再改判 |
| 核心 t+4.3 已先接受 operator_cancel，t+4.5 收到上报 timed_out、exit_code=0 的合法结果 | Task 为 task.operator_wins，最终 cancelled，保留退出码 0 和原停止原因，不让上报值覆盖核心顺序 |
| t+14 核心执行截止生效，t+14.1 收到 result.succeeded | Task 为 task.deadline_wins：timed_out/exited，保留退出码 0 |
| 无停止原因且正常 succeeded/failed 输入 | 按结果与退出码规则结算；后来的取消不修改结果 |

所有成功接收返回 HTTP 200 TaskResponse。若当前 Session 已 ended，完整 result 为 SESSION_ENDED，只能交无输出回执；若 unknown 仍在活动会话中，迟到完整结果为 RESULT_CONFLICT，不能补写输出或重新结算。

### B04 事实乱序与冲突

| 交付顺序 | 预期观察 |
|---|---|
| t+4.2 完整结果，t+4.3 才收到 started | 先为 task.result_before_started，后为 task.late_started；start_observed_at 晚于 exit_observed_at 合法，outcome 和 settled_at 不变 |
| not_started 后 started/exited；或 started 后 not_started | HTTP 409/RESULT_CONFLICT，原事实不变 |
| unknown 后 started | 仅补启动接收观察，execution 仍 unknown，不能恢复 running |
| 从未接收 started 的 unknown 后 not_started；已有启动事实的 unknown 后 not_started | 前者可从 task.unknown.no_start 更新到 task.unknown.late_not_started，outcome 仍 unknown；后者为 RESULT_CONFLICT |
| unknown 后退出证据 | 更新 execution 与派生 cleanup，原 unknown outcome 不变，禁止补输出 |
| 已确认 exited 后 stop_unconfirmed；或已知退出码与迟到 exited 冲突 | HTTP 409/RESULT_CONFLICT，不降低事实确定性 |

### B05 等于截止与结束保留期

| 边界 | 预期观察 |
|---|---|
| now=execute_before | 不启动，start 为 GRANT_EXPIRED，不能用等于边界取得许可 |
| now=有效租约截止 | Agent ended，服务端禁止续租，不因更大的迟到 seq 复活 |
| now=execution_deadline，成功结果同时到达 | 核心先应用执行截止；确认回收后 outcome 为 timed_out |
| now=ended_at+结束保留期 | 元数据与 Agent 身份清除；原 Agent 身份返回 401/UNAUTHENTICATED，有效 MCP/管理身份查旧资源返回 404，不交付清理事件或输出 |

同轮到期按[公共截止裁决](../README.md#多个截止的裁决顺序)执行。下表各行独立，命令已启动，除列出条件外其他授权有效；退出确认是后续无输出 exited 回执，不把协议结算当作 OS 观察。

| 截止组合或已接受事实 | 预期观察 |
|---|---|
| access、Session、任务执行截止均为 t+120 | 先使 access 失效；Session.end_reason=access_invalid，stop.reason=credential_invalid；完整结果为 SESSION_ENDED，退出确认后 cancelled/exited |
| Session 与任务执行截止均为 t+120，access 更晚 | Session.end_reason=session_expired，stop.reason=session_ended；停止确认截止为 t+125，可跨过会话截止；退出确认后 cancelled/exited |
| 当前租约与任务执行截止均为 t+14，会话截止更晚 | Session.end_reason=lease_expired，stop.reason=session_ended；退出确认后 cancelled/exited，不能改判执行超时 |
| 执行截止 t+14、租约截止 t+20，核心到 t+25 才首次采样处理 | 先接受较早执行截止，stop.reason=execution_deadline、requested_at=t+25、confirmation_deadline=t+30；随后会话因租约到期结束；退出确认后仍 timed_out，不回填时间或覆盖首个停止原因 |
| t+13 已接受 operator_cancel，之后 t+14 同时发生租约和执行到期 | 保留原 operator_cancel 及停止确认截止；会话因租约到期结束，退出确认后仍 cancelled |

临近父凭据到期的注册也在本组验收：access 在 t+30 到期，配置 TTL 为 3600 秒，t+0 注册返回 registration.short_access，实际会话与决定截止均为 t+30；t+25 选择 readonly 返回 decision.short_access，首次有效 poll 窗口仅剩 5000 毫秒。t+30 才到达的决定或首次 poll 不激活旧会话；以该 access 新注册为 401，不创建新 Session。

### B06 时钟回拨、提前与系统休眠

命令运行中分别注入墙上时钟回拨、提前、系统休眠和经过时间无法证明四种情况：回拨不延长已确定的本机/服务端截止；墙钟提前到绝对截止后服务端不继续授权；休眠期间经过时间覆盖租约并导致到期；无法证明有效性时 Agent 结束旧授权。任何情况都不得重建首次 start 锚点、重新执行或自动注册。

### B07 平台执行与 Agent 崩溃

| 注入或输入 | 预期观察 |
|---|---|
| shell 缺失，或命令创建前无法建立所需回收边界 | spawn=0，以明确未启动回执结算；分别为 UNSUPPORTED_SHELL 或 SPAWN_FAILED |
| 命令读取 stdin、依赖 profile 或检查环境 | stdin 立即 EOF，profile 不加载，仅有约定的环境白名单，没有 Remota 凭据 |
| 输出含非 ASCII、跨读取块的 UTF-8 字符、无效字节及超过截取上限的大量数据 | 先按原始字节截取再替换解码；大小、truncated 正确，持续排空且不因输出满阻塞停止 |
| 主 shell 退出而边界内后台进程或管道仍存活 | 不报告 exited 或成功完整结果；继续受原固定截止与租约约束，无法回收则 stop_unconfirmed |
| 进程已创建后执行器失败 | 禁止 not_started；确认回收报退出事实，否则报告无法确认 |
| 运行中崩溃或强杀 Agent | Windows 核对 Job 关闭行为；Linux/macOS 核对可能残留的边界。服务端没有回执时最终仍为 unknown，不由测试端观察到 OS 退出而伪造协议证据 |

新 Agent 不接管旧任务。运行验收环境负责观察和清理注入故障留下的进程；这些动作不属于产品自动恢复授权。

### B08 初始化与存储恢复

| 输入或故障 | 预期观察 |
|---|---|
| 普通启动使用空目录、缺失文件、损坏或未知版本状态 | 拒绝启动，不创建新身份，不进入 ready |
| 显式初始化新空目录；再次对已有状态初始化 | 首次原子提交后一次性交接；再次拒绝且原凭据状态不变 |
| 初始化提交成功但秘密交付失败 | 按已初始化处理，通过本机管理恢复核对并撤销/重建，不重交秘密 |
| 撤销持久写入失败，然后存储恢复且有新 poll | 原失败不能报持久成功；保持 degraded、健康检查 503，新 poll 不能自动恢复授权 |
| 提交应答丢失或超时，恢复核实发现事务其实已经提交 | 当时保留结果未知并进入 degraded，不声称已回滚；按原 operation_id 核对，不再次签发或重交秘密；只在核实并收紧必要凭据后恢复 |
| 本机核实并修复管理状态后重启 | 新 epoch；持久身份状态按核实提交保留，旧临时会话、任务及登录不恢复 |

管理 JSON 的结构使用 admin.login.*、admin.snapshot.*、admin.note.*、admin.revoke.*、admin.end.* 与 health.* 正反样例。2.2 的 HTTP 验收还需核对登录 Cookie、CSRF、错误层次、快照统计和秘密可见性；这些结构样例不能证明端点鉴权或浏览器行为已经实现。

## 静态验收与运行验收的完成条件

静态检查以[最小校验入口](#最小校验入口)返回 0 为准；补充边界中的结构条件由相应正反样例承接。校验输出仅报告本次文件结果。

后续运行验收要求完成本页[总体验收清单](#总体验收清单)，逐项复现 N01–N03、D01–D02、G01、R01、V01–V03、L01–L02、S01、B01–B08，记录各端状态与实际 spawn/退出观察。签发结果未知、重复签发关联 ID 及页面身份隔离同属该清单，不另回契约总则查找验收条目。
