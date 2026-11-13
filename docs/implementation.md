# Remota 第二阶段实施规划

本文维护推荐技术组合、验证基线与分步门槛；当前实现进度见 [2.6 管理台进展](#26-管理台进展)，基础探针范围见 [2.0 技术验证结果](#20-技术验证结果)。

[整体架构](architecture.md)和 [Draft 2 契约](contracts/README.md)继续保持编程语言无关。本文描述一套推荐实现，替换语言或框架时仍以相同契约和[验收规格](contracts/acceptance/README.md)为准。

## 实现顺序与通过条件

按下表的依赖推进。公共消息与基础技术验证通过后可以进入使用可控时钟和受控执行事实的 2.1；平台缺口按对应阶段补齐，未验证项不因此变成平台支持。每步沿用已有验收规则，不以“能启动”替代相应行为验证。

| 顺序 | 实现内容 | 通过条件 |
|---|---|---|
| 2.0 技术验证 | 独立探针与依赖基线；状态只在[验证矩阵](#20-技术验证结果)维护 | 公共 Schema/原始 JSON、MCP 适配、SQLite/文件锁基础验证通过，可进入 2.1；平台待验证项继续保留 |
| 2.1 控制核心 | 使用可控时钟和执行事实构建会话、唯一任务/许可、幂等、撤权、结算及清理投影 | D01/D02、V01、B02–B05 的核心部分与近到期注册通过；同截止按统一顺序裁决；交付前通过竞态检测，快照一致且不推断未知退出 |
| 2.2 启动与管理底座 | 初始化/普通启动/恢复、SQLite、凭据、管理员身份与管理 API | B08、管理消息正反例、Cookie/CSRF、签发结果未知及撤销写失败通过；慢提交不阻塞停止，到期/失败不报告持久成功 |
| 2.3 只读 Agent | 本机确认、出站通道、poll、租约看门狗、ping/info、降权与结束 | N01 与 L02/B06 的相关部分通过；readonly 不探测约定之外的信息、不创建命令进程 |
| 2.4 MCP 与任务闭环 | 接上 SDK 适配和八工具，测试中接入受控执行器 | B01、完整消息顺序、错误层次与幂等通过；受理、开始、结果分别可观察，测试替身不进入产品入口 |
| 2.5 真实平台执行 | 实现进程边界、shell、有限输出、回收事实和独立看门狗 | 对应 OS 原生探针及真实休眠观察先通过，再验收 N02/N03、六类异常和 B02–B07；核对实际 spawn 次数与子进程状态；其他平台可分别推进 |
| 2.6 管理台 | 登录、快照、备注、凭据、结束操作、中英文与一次性秘密 | 管理台交互规范通过；旧请求不能跨登录更新状态，修改应答丢失先核对；真实浏览器验证 Cookie/CSRF |
| 2.7 集成与发布 | 平台构建、真实 CLI/HTTP/浏览器联合验收、静态资源嵌入与运行材料 | 全部验收用例闭环，发行包可独立运行，运行时无需 Go、Node 或 Python；明确各平台回收保证 |

2.0 的基础验证不能替代后续状态机或跨端验收。Go 的竞态检测与真实网络测试配合使用；前端使用 [Vitest](https://vitest.dev/guide/)做组件行为验证，[Playwright](https://playwright.dev/docs/intro)验证浏览器完整流程。

## 推荐组合

建议 Agent 与服务端统一使用 Go，管理台使用 React、TypeScript 与 Vite，持久管理状态使用 SQLite。按单实例、临时授权、出站 HTTP 和跨平台命令回收的当前范围选择依赖。

| 部分 | 建议选型 | 采用理由与边界 |
|---|---|---|
| Agent | Go 1.27 系列受支持补丁版；标准 flag、net/http、os/exec，配合 golang.org/x/term、x/sys | 发布原生可执行文件；终端确认、通道和平台执行器在同一工程，平台资源显式回收 |
| 服务端 | 同一 Go 工具链；net/http ServeMux、context、log/slog | 当前接口规模可由标准库承载；核心状态独立于 HTTP 和 MCP |
| JSON 与 Schema | encoding/json/v2、encoding/json/jsontext；santhosh-tekuri/jsonschema/v6，配合 regexp2 的 ECMAScript 模式 | 严格解析后按现有 Draft 2020-12 目录验证；Schema 编译一次，禁用外部引用加载，启用格式断言 |
| MCP | 官方 modelcontextprotocol/go-sdk，使用低层工具处理接口 | 复用协议与 Streamable HTTP 支持，工具 Schema 与业务错误仍按 Draft 2 显式处理 |
| 持久管理状态 | SQLite、database/sql、modernc.org/sqlite | 进程内事务存储；该驱动采用 CGo-free 实现，适合当前二进制发布方式 |
| 管理台 | React、TypeScript strict、Vite、CSS Modules；原生 fetch | 静态管理页面同源内嵌，认证和请求代际显式控制；状态先用局部状态与 Context |
| 构建 | 一个 Go module；web 下单一 npm 工程及锁文件；Node 受支持 LTS | 两个原生程序与一份管理台静态产物，构建工具不进入终端用户的运行环境 |
| 验证 | Go testing/httptest 与竞态检测；Vitest、React Testing Library、Playwright；保留 Python 契约校验 | 分别覆盖核心裁决、HTTP、平台进程和浏览器身份隔离 |

统一 Go 是当前范围下减少工具链、重复通道代码和平台测试成本的判断，不把实现类型升级为协议定义。若以后采用 Rust、C# 或其他语言实现 Agent，只需继续满足相同外部契约。

Go 1.27 已发布新的严格 JSON API；React 官方也提供基于 Vite 的独立应用路径。这里的静态管理台适合该组合，Node 只承担构建和测试。依据见 [Go 1.27](https://go.dev/doc/go1.27)、[React 独立应用](https://react.dev/learn/build-a-react-app-from-scratch)与 [Vite 构建](https://vite.dev/guide/)。

产品依赖固定在根 [go.mod](../go.mod) 与 [go.sum](../go.sum)，基础探针保留独立的 [validation/go.mod](../validation/go.mod) 与 [go.sum](../validation/go.sum)。2.2 沿用已验证的 SQLite、Schema 和系统接口版本，并使用 golang.org/x/net v0.62.0 的 [IDNA Lookup](https://pkg.go.dev/golang.org/x/net@v0.62.0/idna)规范化域名；该版本不受 [GO-2026-5026](https://pkg.go.dev/vuln/GO-2026-5026)影响，同时将间接依赖 x/text 更新至 v0.43.0。2.3 使用已验证的 golang.org/x/term v0.47.0 处理终端输入，2.4 接入已验证的官方 MCP SDK v1.8.0。新增或升级依赖后重新验证。前端版本现由 [package.json](../web/package.json) 和 [package-lock.json](../web/package-lock.json) 固定，验证结果见 [2.6](#26-管理台进展)；MCP 协议版本继续固定为契约中的 2025-11-25。

## 建议项目结构

以下为产品目标目录，随相应步骤逐步建立；当前已建立服务端入口、Agent、认证、时钟、协议校验、控制核心、存储和 MCP 适配。validation 的独立 Go module 只用于技术探针，产品采用根目录单一 module。

```text
Remota/
├─ cmd/
│  ├─ remota-server/          # 服务端入口，组装配置、存储、核心及通道
│  └─ remota-agent/           # Agent 入口与退出协调
├─ schemas.go                 # 直接嵌入权威 Schema
├─ internal/
│  ├─ auth/                  # 秘密生成与摘要
│  ├─ protocol/              # 传输 DTO、严格 codec、Schema 访问
│  ├─ clock/                 # 真实/平台经过时间；测试时注入时钟
│  ├─ server/                # 启动、初始化、恢复和服务生命周期
│  │  ├─ core/               # 会话、任务、凭据生效与快照的一致性边界
│  │  ├─ http.go             # HTTP 路由与管理员 API；agent.go 提供设备通道
│  │  ├─ mcp.go、mcp_tools.go # SDK 适配、工具映射与传输绑定
│  │  ├─ store/              # SQLite 事务、状态版本与目录独占
│  │  └─ webui/              # 静态资源嵌入；dist 为构建产物
│  └─ agent/                 # Agent 运行上下文与协作循环
│     ├─ consent/            # 本机决定、降权与交互输入
│     ├─ transport/          # 出站 HTTP、poll、epoch 与请求代际
│     └─ executor/           # 进程、管道、回收；平台文件按 OS 隔离
├─ web/
│  ├─ src/
│  │  ├─ api/                # 管理 API DTO 与 fetch 封装
│  │  ├─ auth/               # 当前登录代际与敏感状态清除
│  │  ├─ features/           # 会话、MCP 客户端、凭据页面功能
│  │  ├─ components/         # 页面确实复用的控件
│  │  └─ i18n/               # 中英文消息
│  ├─ package.json
│  └─ package-lock.json
├─ tests/
│  ├─ integration/           # 真实 HTTP、多端时序、网络故障注入
│  └─ platform/              # 原生 OS 进程与休眠验证
├─ docs/                     # 已有架构、契约、验收与本规划
├─ tools/                    # 已有契约校验；后续构建/资源导出入口
├─ validation/               # 已有独立技术验证：*_test.go、go.mod、go.sum
├─ go.mod
└─ go.sum
```

单元测试与对应 Go/前端模块同目录；跨端测试才进入 tests。测试直接读取现有契约样例并关联用例 ID。core 按业务拆文件，不把每个实体拆成公共包；core 的状态转换不依赖 MCP SDK、SQL 驱动、HTTP 请求或真实子进程。Agent 不导入 server 包。

## 实现边界

### 严格消息处理与模型

入站按体积与身份检查、严格 JSON 解析、相应消息 Schema、跨消息业务条件的顺序处理。保留数值精度，显式检查安全整数、有限值、孤立代理项、尾随值及空白规则；不能先把数字舍入成 float64 再决定是否合法。

Go DTO 只表达传输字段，不能替代 Schema；时间显式编码为固定毫秒 UTC，必填空集合输出数组，可选字段缺省时省略。MCP 输入校验须保留[协议错误与业务错误的分层](contracts/mcp.md#错误分层)。现有 Python 正反样例作为 Go 消费端的共同数据来源，不复制成另一套夹具。

底层 JSON、Schema、SQL 和 SDK 错误统一映射到契约错误，不直接回显输入对象或底层异常文本。

jsonschema/v6 已通过现有 Draft 2020-12 样例验证；format 默认行为需要显式配置。后续编解码沿用探针中的离线编译与格式断言设置，参见[格式断言接口](https://pkg.go.dev/github.com/santhosh-tekuri/jsonschema/v6#Compiler.AssertFormat)。

该库默认使用 Go 正则引擎；已通过扩展接口接入 regexp2 的 ECMAScript 模式，并验证现有 Unicode 转义与空白模式。不为适配宿主正则而改写权威 Schema。参见[正则引擎配置](https://pkg.go.dev/github.com/santhosh-tekuri/jsonschema/v6#Compiler.UseRegexpEngine)。

管理登录、AdminSnapshot、撤销/结束响应与健康消息已纳入现有 Schema 和共用样例。实现对应端点时仍验证 Cookie/CSRF、统计、字节及资源关联；页面组件只使用管理 API 允许的投影。

### 控制核心与持久提交

控制核心集中持有临时状态，用短临界区串行裁决权限与状态；HTTP 长轮询等待、进程等待和磁盘 I/O 在锁外进行。poll 等待通过会话变化通知唤醒，唤醒后回到核心重新检查，再生成响应。

持久管理修改串行化：先在核心内阻止相关身份获得新能力，锁外执行存储事务，再回到核心提交管理投影或进入 degraded。写入等待不能堵住停止与到期处理，写入失败也不恢复已经收紧的权限。提交结果分类与恢复只按[存储故障规则](contracts/README.md#重启与存储故障)执行。

SQLite 只保存契约声明的持久管理数据。初版建议采用 DELETE 日志模式、synchronous=EXTRA、启用外键、单连接起步；管理修订与相关凭据变更放在同一事务。SQLite 官方区分了删除日志下 FULL 与 EXTRA 的持久性，配置后需读取实际值并验证。参见[同步配置](https://www.sqlite.org/pragma.html#pragma_synchronous)与[事务原子性](https://www.sqlite.org/atomiccommit.html)。

普通启动用不创建文件的打开模式，显式初始化才创建数据库；管理目录另持有系统排他锁。记录内部状态版本，未知版本按契约拒绝启动。驱动及其配套 modernc.org/libc 版本一并锁定，依据[驱动依赖说明](https://pkg.go.dev/modernc.org/sqlite)。

### Agent 的时钟与执行器

Agent 由本机授权状态、传输循环、独立看门狗和执行器组成。本机收紧先于网络提交；传输恢复只能恢复仍有效的原上下文。测试替身通过测试构造注入，产品入口不增加 fake/full 授权开关。

时钟接口区分 UTC 展示时间与覆盖休眠的经过时间。Go 文档明确指出部分系统的单调时钟在休眠时可能停止，因此不能直接把 time.Since 当作完整租约时钟；平台候选及验证状态见 [2.0 结果](#20-技术验证结果)。timer 只用于唤醒，恢复后重新检查权限。依据见 [Go 时钟说明](https://go.dev/src/time/time.go)与[系统时钟语义](https://man7.org/linux/man-pages/man3/clock_gettime.3.html)。

执行器通过平台文件隔离 Windows Job 与 Linux/macOS 进程组：Windows 在创建时将进程加入 Job，POSIX 使用 os/exec 建立进程组；两者均自行处理管道和完整回收。以[平台执行器约定](contracts/tasks.md#平台执行器约定)为验收边界，参见 [os/exec 取消行为](https://pkg.go.dev/os/exec#CommandContext)。

### MCP SDK 的使用范围

官方 SDK 作为协议适配依赖。2.0 已验证锁定版本可限制支持版本、返回 JSON 并接收原始工具参数；必需的响应补齐见[技术验证结果](#20-技术验证结果)。参见[官方 Go SDK](https://go.sdk.modelcontextprotocol.io/)及[低层 ToolHandler](https://pkg.go.dev/github.com/modelcontextprotocol/go-sdk/mcp#ToolHandler)。

工具输入输出使用契约 Schema，采用 Server.AddTool/ToolHandler 显式校验并构造结果。Remota 管理自己的身份绑定、初始化期限、空闲期限与结束流程；SDK 传输会话和 mcl 投影对应，但不是第二个授权来源。

2.4 已将 SDK 适配接入真实核心，覆盖初始化/空闲期限、管理结束及已受理任务不受 RPC 取消影响的验收。SDK 默认的额外方法或能力由适配层收口，不修改契约或把业务状态迁入 SDK。

### 管理台与资源构建

管理台使用一份同源 fetch 封装处理 Cookie、CSRF、AbortController 和登录代际；刷新行为、秘密清除及语言切换遵循[管理台交互](product/console.md)。不为各页面另建认证缓存。中英文消息先采用类型化字典，组件局部状态保存表单草稿。

开发环境通过 Vite 代理连接后端，并让显式 public_url、Host、Origin 保持一致；保留与生产相同的 CSRF 与身份检查。前端自动重试策略不得覆盖修改请求的结果核对规则。

服务端通过根目录的 Go embed 直接嵌入权威 Schema，避免维护另一份副本。后续构建再将管理台静态产物嵌入服务端；两个程序共享协议数据与编解码，不共享服务端状态或执行策略。工具定义由同一契约数据构建，RPC 信封实时生成，tools-list.json 用于一致性检查。

## 构建目标与验收材料

Agent 的目标矩阵为 Windows、Linux、macOS 的 amd64/arm64；服务端首批发布以 Linux amd64/arm64 为主，同时保留本机开发测试能力。矩阵属于交付目标，只有对应原生 runner 的进程、计时和打包测试通过后才标记支持；交叉编译成功不等于平台验收通过。

后续 CI 按契约静态校验、Go 单元/集成、前端检查、原生平台测试、发行打包的顺序建立。认证摘要、token、命令和输出不进入构建日志或快照；日志只保留允许的定位元数据。

每步记录通过的用例 ID 与尚未覆盖的真实边界。实施时首先运行现有[最小校验入口](contracts/acceptance/README.md#最小校验入口)，再按当前变更增加必要测试；目录、模型、工具定义与样例共同保持一致。

## 2.0 技术验证结果

2.0 验证代码位于 validation，例如[消息验证](../validation/schema_test.go)，仅含测试、回环 HTTP 适配和固定角色的受控子进程，不包含产品入口、授权状态机或远程命令执行。直接读取现有 Schema 与消息样例，不执行样例中的命令正文；SQLite 仅操作测试临时目录中的通用表。下表保留当时的验证结果；完整 MCP 契约回归现由[产品通道测试](../internal/server/mcp_test.go)承接，独立模块保留 [SDK 取消上下文探针](../validation/mcp_test.go)。

### 锁定基线

以下为本次验证版本摘要，依赖清单以 go.mod/go.sum 为准；其中 libc 跟随 SQLite 驱动要求固定，不单独升级。

| 依赖 | 版本 |
|---|---|
| Go 工具链 | 1.27.3 |
| github.com/modelcontextprotocol/go-sdk | v1.8.0 |
| github.com/santhosh-tekuri/jsonschema/v6 | v6.0.3 |
| github.com/dlclark/regexp2 | v1.12.0 |
| modernc.org/sqlite / modernc.org/libc | v1.62.0 / v1.80.1 |
| golang.org/x/sys / golang.org/x/term | v0.49.0 / v0.47.0 |

Go 1.27.3 采用已发布的受支持补丁，见 [Go 发布记录](https://go.dev/doc/devel/release#go1.27.0)。本次以 CGO_ENABLED=0 构建并运行测试；go mod verify 与 go vet 均通过。

### 已验证的能力与限制

| 验证对象 | Linux amd64 实测结果 |
|---|---|
| 严格 JSON / Draft 2020-12 | Go 与 Python 共用消息正反样例及 raw_json，结果一致；覆盖管理快照等新增消息、非法 JSON 和精确数值；离线编译、format 断言和 ECMAScript 正则可用。Python 使用精确十进制与整数类型断言，不先舍入成 float |
| MCP SDK 适配 | 真实回环 HTTP 下，固定版本协商、初始化门禁、八工具完整定义及自包含 Schema、参数错误分层、显式布尔字段、凭据绑定、逐请求撤销检查、GET 405、DELETE 204 均通过 |
| RPC 取消 | SDK 能取消工具处理上下文；由此确认后续已受理任务必须拥有独立于 RPC 的核心生命周期。本轮未实现任务或验证其取消裁决 |
| SQLite | DELETE / EXTRA / 外键配置回读正确；失败事务回滚、提交后重开、提交前后强杀子进程恢复及 integrity_check 通过；缺失/损坏文件不被重新初始化；max_page_count 触发真实 SQLITE_FULL 且保留原提交数据 |
| 目录独占 | 两个真实进程竞争文件锁，第二个被拒绝；正常释放或强杀持锁者后均可再次获取 |
| 平台经过时间 | CLOCK_BOOTTIME 可调用且持续推进；真实休眠尚未实测 |
| 进程边界 | 同组父子进程均被组信号回收；父进程被强杀后子进程仍存活，验证了 POSIX 进程组不提供崩溃自动清理保证；测试随后显式回收 |
| 终端检测 | x/term 正确识别非终端输入；交互确认流程留待 2.3 |

SDK v1.8.0 的默认输出会省略 listChanged=false、isError=false，Tool 类型也没有 execution 字段。2.0 测试适配只补齐这些契约字段后，再对实际 SDK 应答逐字段核对；没有替换整份发现应答。2.4 产品适配继续补齐这些字段，并显式限制方法、HTTP 表面、错误格式和身份绑定。

Windows 的时钟探针绑定 QueryInterruptTimePrecise，包含休眠补偿；macOS 绑定 CLOCK_MONOTONIC_RAW，底层采用连续时间。选择依据见 [Windows 中断时间](https://learn.microsoft.com/en-us/windows-hardware/drivers/ddi/wdm/nf-wdm-kequeryinterrupttime)及 [Apple 时钟实现](https://github.com/apple-oss-distributions/Libc/blob/main/gen/clock_gettime.c)。Windows Job 探针覆盖先挂起创建、加入 Job 后继续、关闭 Job 及持有者崩溃回收。执行状态统一见下表。

| 平台 | 编译 | 原生测试 |
|---|---|---|
| Linux amd64 | 通过 | WSL Linux 内实际运行，全部通过；不代表物理机休眠或全部 Linux 发行环境 |
| Linux arm64 | 通过 | 无对应设备，未运行 |
| Windows amd64 | 通过 | 已运行 Job 进程树、持有者崩溃和普通时钟推进探针；完整独立验证套件尚未重跑 |
| Windows arm64 | 通过 | 无对应设备，未运行 |
| macOS amd64 / arm64 | 均通过 | 无原生 runner，未运行 |
| 真实休眠 | 不适用 | 未执行；仅清醒状态的默认及 1 秒时钟窗口通过 |
| Go 竞态检测 | 不适用 | 2.0 独立探针未运行竞态；2.1 核心的竞态结果见[下节](#21-控制核心结果) |

### 复跑入口与收口条件

在对应原生系统使用 Go 1.27.3，从仓库根目录执行：

```text
cd validation
go version
go mod verify
go vet -mod=readonly ./...
go test -mod=readonly -count=1 -timeout=90s ./...
```

如宿主 Go 版本不同，先在当前进程设置 GOTOOLCHAIN=go1.27.3；验证构建设置 CGO_ENABLED=0。也可以先 go test -c 再在同架构原生系统从 validation 目录运行测试二进制，保留相邻 docs 目录；本次 Linux 即由 Windows 交叉构建后在 WSL 运行。

真实休眠单项入口为 go test -mod=readonly -run '^TestPlatformElapsedClock$' -count=1 -v -timeout=5m -args -clock-window=2m。操作者在观察窗口内使测试设备休眠并恢复，期间保持 UTC 时钟稳定，保存 OS 休眠/恢复记录；探针对比平台经过时间与 UTC 跨度，允许 2 秒观测误差。没有实际休眠记录的通过只证明普通推进，探针不会自动挂起设备。

竞态入口为 go test -race -mod=readonly -count=1 -timeout=90s ./...，要求当前进程设置 CGO_ENABLED=1 并提供可用 C 编译器。阶段门槛统一见[实现顺序](#实现顺序与通过条件)。这些探针只证明基础能力，不能代替完整 B01、B06–B08 的产品验收。

## 2.1 控制核心结果

2.1 已完成。根模块的 [core](../internal/server/core/core.go)采用单个互斥锁、内存状态和[可注入时钟](../internal/clock/clock.go)。每个核心入口用同一时间样本先处理到期条件，再检查权限和状态；等待通过 BeginPoll/CompletePoll/AbortPoll 留在锁外，核心不创建后台循环或执行进程。

| 验收目标 | 已验证的核心范围 | 测试入口 |
|---|---|---|
| D01/D02、G01、R01、V01 与 B02–B04 | 意图幂等与冲突、唯一许可及重复预算不变、撤权先于开始、结果裁决、回执重复和乱序；读取现有消息样例作状态断言 | [核心流程](../internal/server/core/core_test.go) |
| B05、近到期注册、B06 的服务端计时部分 | 等于截止失效、同轮按绝对截止及优先级裁决、首次原因固定、回拨/提前/模拟休眠、poll 返回前复查、结束保留和缓存脱敏 | [期限与并发](../internal/server/core/deadlines_test.go) |
| 状态与权限边界 | 决定和激活窗口、MCP 初始化/空闲期限、只读 MCP 权限、任务累计配额不淘汰幂等记录、UTF-8 字节及结果组合 | [边界检查](../internal/server/core/limits_test.go) |
| 一致性 | 并发提交同一意图、撤权与 start、取消与结果、快照读取及返回值隔离；Linux amd64 竞态检测通过 | [并发检查](../internal/server/core/deadlines_test.go) |

本次使用 Go 1.27.3，在 WSL Linux amd64 中以 GCC 和 CGO_ENABLED=1 原生运行根模块测试，竞态检测通过；go vet 与 go mod verify 通过，静态契约校验通过。平台支持状态仍以上述 2.0 矩阵和后续原生执行验收为准。

在具备 C 编译器的测试环境，从仓库根目录执行；工具链使用 Go 1.27.3，宿主不同则设置 GOTOOLCHAIN=go1.27.3，竞态运行设置 CGO_ENABLED=1：

```text
go mod verify
go vet ./...
go test -race -count=1 -timeout=90s ./...
```

根模块的 ./... 不包含独立 validation 模块，其[复跑入口](#复跑入口与收口条件)保留。核心类型是进程内模型，不能直接当作 HTTP/MCP DTO；只读 Agent 授权与租约发送锚点已在 2.3 接入，MCP 传输见 [2.4](#24-mcp-与任务闭环结果)，设备侧命令开始许可锚点及进程回收见 [2.5](#25-真实平台执行进展)。核心测试的执行事实由受控输入提供，不证明实际 spawn 或 OS 回收。

## 2.2 启动与管理底座结果

服务端入口为 [remota-server](../cmd/remota-server/main.go)。初始化只接受新空目录，一次性交付管理员口令、access 和 MCP 秘密；SQLite 只保存口令与凭据摘要及管理元数据。普通启动要求已有完整状态和目录独占锁，不将缺失、损坏或未知版本当作新部署。管理修改先在核心收紧权限，在锁外完成持久提交，失败或结果未知进入 degraded；本机恢复检查实际状态并收紧未确定的凭据。

管理员登录、Cookie/CSRF、凭据签发/轮换/撤销、快照、备注及结束操作已接入现有 HTTP 契约和 Schema。设备和 MCP 的秘密鉴权已接入核心；设备通道的只读部分已在 2.3 接入，MCP 通道已在 2.4 接入，管理台待 2.6 实现。

| 验证范围 | 入口 |
|---|---|
| 管理 API、Schema、身份隔离、一次性交付、慢提交、写失败和结果未知 | [HTTP 测试](../internal/server/http_test.go) |
| SQLite 事务、缺失/损坏状态、真实写满、进程中断与恢复标记 | [存储测试](../internal/server/store/store_test.go) |
| 一次性交接失败、本机恢复及真实服务进程重启 | [入口测试](../cmd/remota-server/main_test.go)、[进程测试](../cmd/remota-server/main_process_test.go) |
| 严格 JSON 和共用契约样例 | [协议测试](../internal/protocol/schema_test.go) |

在 Linux 上使用 Go 1.27.3，从仓库根目录初始化并启动：

```text
go run ./cmd/remota-server init --state-dir ~/.local/state/remota
go run ./cmd/remota-server serve --state-dir ~/.local/state/remota --listen 127.0.0.1:8080
```

初始化输出只交付一次，妥善保存秘密且不要写入日志。对外部署时用 `--public-url` 指定 HTTPS 根地址，由外部代理终止 TLS 并保留对应 Host；服务本身只监听回环地址。

服务停止后，可运行 `go run ./cmd/remota-server recover --state-dir ~/.local/state/remota --inspect` 查看不含秘密的持久状态与待恢复操作；使用 `--reset-admin` 重置口令，使用 `--revoke` 指定需撤销的凭据 ID，该参数可重复。恢复会核实并保守撤销未确定操作涉及的凭据；恢复标记损坏时拒绝自动修复。若初始化已提交但秘密交付失败且无恢复标记，重置口令后须核对并撤销不可得的初始凭据，再新建凭据。若写入恢复标记本身失败，操作者仍须核对最后成功提交的状态并完成必要撤销。

本阶段在 WSL Linux amd64 的 Linux 文件系统上运行服务进程和存储测试。Windows 原生测试中服务端存储初始化失败；当前目录同步使用只读句柄，而 [FlushFileBuffers 要求 GENERIC_WRITE](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-flushfilebuffers)，仍需单独定位和修复，目前请使用 WSL/Linux。macOS 原生目录同步和持久性尚未验证，交叉编译不等于平台通过。真实系统休眠和浏览器端到端验收仍在后续阶段；MCP 的验证范围见 [2.4](#24-mcp-与任务闭环结果)。

在有 C 编译器的 Linux 测试环境从仓库根目录运行，竞态检测设置 `CGO_ENABLED=1`：

```text
go mod verify
go vet ./...
go test -race -count=1 -timeout=120s ./...
```

## 2.3 只读 Agent 结果

已接入 [只读 Agent](../internal/agent/agent.go) 和[设备 HTTP 通道](../internal/server/agent.go)：注册、读取会话、决定、降权、长轮询、结束及固定诊断结果。设备凭据只绑定当前会话，并在结束保留期后失效；poll 在锁外等待，诊断和结束事件可唤醒等待。固定 ping/info 由核心关联调用身份与授权，结果重传不重复探测。命令相关设备端点和 MCP 传输见 [2.4](#24-mcp-与任务闭环结果)。

服务端启动后，在设备的交互终端运行：

```text
go run ./cmd/remota-agent --server http://127.0.0.1:8080
```

按提示输入 access 凭据，输入不会回显；确认页面显示服务地址、操作方、期限和权限说明，回车同意只读，输入 `n` 拒绝。输入 `f` 后还须输入 `full` 才会批准命令执行。运行中输入 `q` 结束，`r` 收紧为只读。远端地址使用 HTTPS；使用非回环 HTTP 时，服务端和 Agent 均须显式设置 `--allow-insecure-http`。

| 验证范围 | 入口 |
|---|---|
| 本机确认、首个租约、ping/info、结果应答丢失后重传、本机结束 | [只读链路测试](../internal/agent/integration_test.go) |
| 阻塞 poll、租约到期、迟到响应、模拟休眠与时钟异常 | [链路测试](../internal/agent/integration_test.go)、[状态测试](../internal/agent/state_test.go) |
| 会话身份、重复 poll、固定诊断、结果幂等与降权 | [设备通道测试](../internal/server/agent_test.go) |
| 真实伪终端确认及凭据不回显、拒绝非交互输入 | [终端测试](../cmd/remota-agent/terminal_linux_test.go)、[入口测试](../cmd/remota-agent/main_test.go) |

在 WSL Linux amd64 运行根模块竞态测试、终端测试及真实回环 HTTP；2.4 补充了实际只读 Agent 与 MCP 的 ping 链路。L02/B06 中的断联、迟到响应和经过时间异常已覆盖，真实系统休眠尚未验证。Windows 的普通时钟推进已原生验证，本机 full 确认与凭据不回显由下节的 Windows 端到端测试覆盖；macOS 的 Agent 终端及时钟尚未做原生运行验证。交叉编译不代表平台验收通过。复跑命令见[服务端验证入口](#22-启动与管理底座结果)，独立契约校验见[最小校验入口](contracts/acceptance/README.md#最小校验入口)。

## 2.4 MCP 与任务闭环结果

服务端在 `/mcp` 接入官方 SDK 的 Streamable HTTP 及八个工具，按契约单独校验 Bearer、传输绑定、版本、生命周期、消息和工具参数；工具 Schema 从嵌入的权威消息目录生成。设备通道接入任务开始许可、回执和结果接口，任务受理、去重、停止、裁决与脱敏仍由控制核心统一负责。

| 验证范围 | 入口 |
|---|---|
| 握手、工具发现与 Schema 一致性、B01 错误分层、身份绑定、到期、撤销和缓存头 | [MCP 测试](../internal/server/mcp_test.go) |
| 受控设备通过 HTTP 报告许可、启动、退出与结果；重复许可、意图重试、取消和结束后脱敏 | [MCP 任务测试](../internal/server/mcp_test.go) |
| MCP 固定诊断、等待取消或超时、断连时并发限制、客户端结束期间的等待清理 | [MCP 测试](../internal/server/mcp_test.go)、[SDK 取消探针](../validation/mcp_test.go) |
| 真实只读 Agent 通过回环 HTTP 响应 MCP ping | [Agent 联合测试](../internal/agent/integration_test.go) |

本阶段的受控设备替身只提交预设执行事实，不创建进程；不能将其测试视为真实平台执行验收。测试与静态检查的复跑入口见[服务端验证入口](#22-启动与管理底座结果)和[最小校验入口](contracts/acceptance/README.md#最小校验入口)。

## 2.5 真实平台执行进展

Agent 已接入本机 full 二次确认、一次性开始许可、真实命令执行、独立有限输出及停止后的进程回收。Windows 创建命令进程时以 Job 属性建立管理边界；Linux/macOS 使用独立进程组。无法确认边界和管道均已回收时报告 `stop_unconfirmed`。降权、结束和断连会停止本机输出采集并触发回收；详情仍以[设备通道](contracts/agent.md)和[任务语义](contracts/tasks.md)为准。

| 验证范围 | 入口 |
|---|---|
| 真实服务与 Agent：成功执行、丢失开始许可或结果应答、许可到达前取消和窗口到期、取消与超时、断连和降权；模拟时钟推进跨过首个租约并续租 | [联合测试](../internal/agent/tasks_integration_posix_test.go) |
| 有限输出、无效 UTF-8、环境隔离、stdin、shell 退出与后台进程、停止回收 | [POSIX 执行器测试](../internal/agent/executor_posix_test.go)、[Windows 执行器测试](../internal/agent/executor_windows_test.go) |
| MCP 客户端结束或凭据失效后只发无输出回执；保留首次停止原因，后续身份失效仍丢弃输出并取消结果重试 | [联合测试](../internal/agent/tasks_integration_posix_test.go)、[任务测试](../internal/agent/tasks_test.go) |
| 首次发送锚点、许可核对、经过时间截止、任务事件去重与冲突、回执及结果重试遵守 Retry-After | [任务测试](../internal/agent/tasks_test.go) |
| full 二次确认未通过时不授权、凭据不回显，以及决定应答未返回时的本机降权 | [终端测试](../cmd/remota-agent/terminal_linux_test.go)、[授权竞态测试](../internal/agent/consent_integration_test.go) |
| Windows 产品 CLI 经 ConPTY 完成本机 full 二次确认；真实 MCP 命令链路、意图重试只执行一次、许可与结果应答丢失、双流输出上限、取消与超时、MCP 身份失效、本机降权与结束、poll 失败及长命令续租；用原生进程句柄核对父子进程退出。工具发现、协议错误及元数据规则由 2.4 测试承接 | [Windows 端到端验收](../tests/integration/agent_windows_test.go) |

本次已在 WSL Linux amd64 运行真实进程、HTTP 联合测试及 Go 竞态测试；Windows amd64 原生运行了 Agent 输出和进程树取消测试、Job 持有者崩溃探针及普通时钟推进检查，并通过 Linux 服务端、真实 MCP 与 Windows 原生 full Agent 的 10 个端到端场景。长命令实际运行 33 秒，验证持续 poll 续租可跨过首个 30 秒租约；没有以模拟时钟替代该观察。Windows 原生服务端存储初始化问题仍在，macOS 与其他架构没有原生 runner。其余五组目标的 Agent 和测试已交叉编译；真实休眠和完整验收清单尚未验证，因此 2.5 的平台验收门槛尚未全部通过。交叉编译不代表进程回收或休眠行为已在对应平台验证。

Windows 端到端验收从仓库根目录的 PowerShell 运行，需要 Go 1.27.3、支持 ConPTY 的 Windows、可用的 WSL/Linux，以及 Windows 到 WSL 服务的 localhost 转发。`REMOTA_E2E_WSL_DISTRO` 可选指定已安装的发行版，未设置时使用默认发行版：

```powershell
$env:REMOTA_WINDOWS_E2E = '1'
go test -mod=readonly -run '^TestWindowsMCPAgent$' -count=1 -v -timeout=6m ./tests/integration
```

测试自动构建 Windows 产品 Agent 与 Linux 测试服务，在 WSL 临时目录初始化独立 SQLite；全部业务操作经过真实 MCP 或管理 HTTP，秘密通过测试子进程的私有管道和本机终端交付。故障注入与请求计数只位于测试服务，应答丢失须核实原处理成功；每个场景按新增会话 ID 定位，清理先正常结束再强制回收。长命令按 500 毫秒间隔查询，保留真实的跨租约观察。进程和临时状态在结束后清理；未显式启用时跳过该环境专项测试，不将跳过计为平台验收通过。

## 2.6 管理台进展

第一批已接入登录、退出、登录恢复及设备会话、MCP 客户端和凭据的只读快照；入口为 [App.tsx](../web/src/App.tsx)。本机授权与进程事实仍只通过现有 API 的投影展示。搜索和凭据状态筛选在当前页面内执行，统计始终来自完整快照。可见页面每 5 秒刷新，同一时刻最多一个请求；隐藏时暂停，失败保留并标出旧数据。设备会话的 5 分钟提醒、凭据的 7 天提醒及到期估算沿用现有交互规则。

第二批已接入备注编辑、结束设备会话和 MCP 客户端；共用[就地操作区](../web/src/ResourceAction.tsx)，结束前明确确认。备注草稿与基准修订独立于刷新保存，冲突后展示当前备注并由操作者显式采用当前修订，交互规则见[管理台规范](product/console.md#快照与列表)。修改串行发送，期间取消旧读取并暂缓轮询，完成后重新读取完整快照；明确的参数或来源拒绝直接提示，不追加刷新。应答丢失保留结果未知，不自动重发，核对失败时继续阻止修改直至取得有效快照，核对状态由会话 Hook 统一维护。注销、身份变化与页面销毁均中止本页修改请求并丢弃迟到应答；结束确认与回收状态分别展示。

[会话 Hook](../web/src/useAdmin.ts)统一处理登录代际、取消与过期。退出先清除页面数据，再等待服务端确认；401 退出仍清理本地状态，网络失败显示未取得确认。登录与退出请求串行，旧响应、旧 CSRF 和被取消的刷新不能更新后续登录。口令提交后立即清空输入，CSRF 仅在页内存保存；名称、备注和其他远端文本按纯文本渲染。[请求封装](../web/src/api.ts)先核对约定的 HTTP 状态和 JSON 媒体类型，再直接引用权威消息 Schema，以 Ajv Draft 2020-12 和格式断言校验应答，TypeScript 仅声明界面使用的 DTO。

前端使用 Node 22.23.2、npm 10.9.8 完成验证，Node 范围以 package.json 的 engines 为准。React/React DOM 19.3.1、TypeScript 7.0.2、Vite 8.3.8、React 插件 6.3.0、Ajv 8.20.0/ajv-formats 3.0.1、Vitest 5.1.0、Playwright 1.65.0 及全部直接开发依赖均使用精确版本，并提交 npm 锁文件。运行页面不加载外部字体、脚本或图片。

### 开发运行

按 [2.2](#22-启动与管理底座结果)准备已有状态目录后，在 Linux/WSL 的仓库根目录启动后端：

```text
go run ./cmd/remota-server serve --state-dir ~/.local/state/remota --listen 127.0.0.1:8080 --public-url http://127.0.0.1:5173
```

另一个终端从仓库根目录启动前端：

```text
cd web
npm ci
npm run dev
```

浏览器访问 `http://127.0.0.1:5173`，输入初始化时交付的管理员口令。Vite 将 `/api`、`/mcp` 和 `/healthz` 转发到 `http://127.0.0.1:8080`，保留浏览器 Host 和 Origin；后端的 public_url 须与浏览器地址一致。使用其他后端地址时设置 `REMOTA_API_TARGET`，并相应调整服务 public_url；不关闭 Host、Origin 或 CSRF 校验。

### 验证入口与范围

在 web 目录运行：

```text
npm run format:check
npm run test
npm run build
npx playwright install chromium
npm run test:browser
```

Windows 的浏览器测试通过 WSL/Linux 运行真实服务端，需要可用的 localhost 转发和与本机架构相同的 WSL。`REMOTA_E2E_WSL_DISTRO` 可指定发行版，否则使用默认发行版；Linux 直接启动测试服务。服务端测试程序每次运行只编译一次，各浏览器场景分别启动服务并建立独立 SQLite，结束时关闭服务并清理状态；编译或路径转换失败也清理构建目录。测试服务固定占用 `127.0.0.1:18080`，前端固定占用 `127.0.0.1:4173`，场景串行执行，bootstrap 秘密仅经私有管道进入当前场景夹具。已安装 Edge 时可设置 `REMOTA_BROWSER_CHANNEL=msedge` 使用该浏览器；本次实际使用 Windows 原生 Edge（Chromium）验收，并检查了桌面与移动端截图，六个场景使用 `--repeat-each=2` 共十二次执行通过。

| 验证范围 | 入口 |
|---|---|
| 刷新单请求与隐藏暂停、失败保留旧数据、401/到期/身份变更清理、旧成功或失败响应不能跨登录、退出结果未知与认证请求串行、Schema 拒绝、筛选与数据清除 | [组件与会话用例](../web/src/useAdmin.test.tsx) |
| 修改串行与旧读取隔离、核对门槛与自动刷新恢复、迟到修改成功或 401 应答隔离、修改 401/CSRF 清理、明确拒绝不补读、核对期间禁止采用修订、自动刷新保留草稿、结束与回收分别展示 | [组件与会话用例](../web/src/useAdmin.test.tsx) |
| 应答状态与 JSON 媒体类型、修改方法/正文/Schema/资源 ID/epoch、编译失败后的目录清理与原始错误保留 | [请求校验](../web/src/api.test.ts)、[启动器异常清理](../web/e2e/server.test.ts) |
| 真实 HTTP 登录正反例、Cookie/CSRF、三类列表与筛选、纯文本渲染、退出与恢复、修订冲突保留草稿后保存、分别结束客户端与设备、成功处理后丢失修改应答、移动端无页面横向溢出 | [6 个浏览器场景](../web/e2e/console.spec.ts)、[Linux 测试服务](../tests/integration/console_linux_test.go) |

后续仍需实现凭据签发/轮换/撤销、一次性秘密弹窗和中英文切换。构建产物位于 web/dist，服务端静态资源内嵌及真实 HTTPS 浏览器验收留待后续集成；当前仅提供开发入口。Windows 原生服务端存储问题及各平台原生验收缺口继续按前文记录处理。
