# Remota

临时远程协助系统的重写设计，采用单实例服务端、主动出站的设备 Agent、内嵌管理台和远程 HTTP MCP。

当前已接入控制核心、服务端初始化与恢复、SQLite 和管理员 API；Agent、MCP 传输与管理台尚未接入。阶段门槛、运行入口与实际验证状态统一见[实施规划](docs/implementation.md)。

## 阅读路径

先了解整体架构，再阅读公共契约和所需领域；实现与验收分别使用对应入口。

| 阅读目标 | 入口 |
|---|---|
| 组件、状态归属与部署边界 | [整体架构](docs/architecture.md) |
| HTTP、身份、期限、错误与规则归属 | [契约总则](docs/contracts/README.md) |
| 设备接入、本机授权与租约 | [设备通道](docs/contracts/agent.md) |
| 命令、开始许可、证据与结算 | [任务语义](docs/contracts/tasks.md) |
| 管理登录、快照与凭据 | [管理员通道](docs/contracts/admin.md) |
| MCP 初始化、工具与错误分层 | [MCP 通道](docs/contracts/mcp.md) |
| 管理台刷新、身份隔离与交互 | [管理台交互](docs/product/console.md) |
| 阶段门槛、技术组合与项目结构 | [实施规划](docs/implementation.md#实现顺序与通过条件) |
| 验收覆盖与正常、异常场景 | [验收总清单](docs/contracts/acceptance/README.md#总体验收清单) |

## 校验入口

- [静态契约校验](docs/contracts/acceptance/README.md#最小校验入口)：检查 Schema、样例、工具定义和文档链接。
- [技术验证复跑](docs/implementation.md#复跑入口与收口条件)：运行独立验证模块；平台结果以该文档记录为准。
- [控制核心验证](docs/implementation.md#21-控制核心结果)：从根模块运行状态机与竞态测试。
- [启动与管理底座](docs/implementation.md#22-启动与管理底座结果)：运行服务、检查恢复状态，并复跑服务端验证。
- [消息 Schema](docs/contracts/schemas/messages.schema.json)与[共用样例](docs/contracts/acceptance/messages.json)：字段结构与校验数据的来源。

架构与契约保持编程语言无关；具体实现选型在实施规划维护。协议规则、消息结构和验收观察的权威来源见[规则归属](docs/contracts/README.md#阅读入口与规则归属)。
