# Remota Linux amd64 候选包运行说明

本候选包包含服务端、内嵌管理台和 Agent，运行时无需 Go、Node.js 或 Python。Agent 执行命令时仍需要本机可用的 `sh`；本机确认需要交互式终端。候选包不等于全部平台验收完成：当前链路在 Linux amd64 验证，真实休眠、其他架构与 macOS 原生验证仍待完成，Windows 原生服务端存在存储初始化问题。

## 核对和解压

将候选包与同次交付的 SHA256SUMS 放在同一目录，从该目录运行：

```sh
sha256sum --check SHA256SUMS
tar -xzf remota-0.1.0-rc.1-linux-amd64.tar.gz
cd remota-0.1.0-rc.1-linux-amd64
```

示例版本应替换为实际包名。包内目录与程序的权限为 0755，RUNNING.md 与 release.json 为 0644。SHA-256 用于检测损坏，文件仍应从可信交付渠道取得。release.json 记录版本、目标平台、来源提交与工作区是否存在未提交修改；CI 只接受干净来源的候选包。

## 初始化与启动

以专用的普通用户运行，状态目录与程序目录分离。初始化只接受新空目录；已有状态须直接启动，不能重新初始化。

```sh
./remota-server init --state-dir "$HOME/.local/state/remota"
./remota-server serve --state-dir "$HOME/.local/state/remota" \
  --listen 127.0.0.1:8080 --public-url http://127.0.0.1:8080
```

初始化在当前终端一次性交付管理员口令、access 和 MCP 秘密。妥善保存，避免终端录屏、日志收集或重定向到共享文件。状态只保存摘要，无法再次取回这些秘密。若交付失败，按下文离线恢复处理。

浏览器访问 http://127.0.0.1:8080，输入管理员口令。管理台可签发、轮换与撤销凭据；新秘密只在当前弹窗交付，关闭后无法重新显示。服务的健康检查地址是 /healthz。

## 对外 HTTPS 接入

服务只监听回环 HTTP，由同机反向代理终止 TLS。例如公开地址为 https://assist.example.com，则启动参数为：

```sh
./remota-server serve --state-dir "$HOME/.local/state/remota" \
  --listen 127.0.0.1:8080 --public-url https://assist.example.com
```

代理将该根地址下的所有路径转发至 http://127.0.0.1:8080，保留公开地址的 Host、原始方法、路径、查询参数和 Origin，不缓冲 MCP 流式应答。代理的响应与空闲超时应留足长轮询时间，例如 60 秒。浏览器、Agent 与 MCP 使用同一个公开根地址，MCP 端点为 /mcp；不部署在 URL 子路径。服务以配置的 public_url 校验 Host 和 Origin，不从转发头推断公开地址。

使用与公开域名匹配、客户端信任的证书。自建 CA 时分别为浏览器、Agent 所在系统和 MCP 客户端配置信任；不要关闭证书、Host、Origin 或 CSRF 校验。正常对外部署使用 HTTPS；非回环 HTTP 需要服务端和 Agent 分别显式允许，不用于此部署方式。

## Agent 与 MCP

在需要协助的 Linux amd64 设备上，从交互式终端运行：

```sh
./remota-agent --server https://assist.example.com
```

按提示输入 access 凭据，秘密不回显。核对服务地址、操作方、期限和权限后，回车同意只读，输入 n 拒绝；输入 f 后还须输入 full 才允许执行命令。运行中输入 r 收紧为只读，输入 q 结束。Agent 会话和任务不会在程序重启后自动续接。

MCP 客户端使用 Streamable HTTP 连接 https://assist.example.com/mcp，通过其受信任的凭据配置机制提供 `Authorization: Bearer <MCP_TOKEN>`。占位符须替换为 MCP 凭据；不要把真实秘密放入命令行或源码。先完成初始化再调用工具。命令执行同时要求 MCP 凭据允许执行及设备本机同意 full；管理台中的接入指引提供地址与配置示例。

## 停止与离线恢复

前台运行时按 Ctrl+C，后台服务向服务端发送 SIGTERM，等待退出后再操作状态目录。停止会收紧会话并通知 Agent 回收；服务进程退出本身不证明失联设备上的命令已退出，必要时在设备本机确认。状态目录只能被一个服务或恢复进程独占。

服务停止后，先检查不含秘密的持久元数据与待恢复操作：

```sh
./remota-server recover --state-dir "$HOME/.local/state/remota" --inspect
```

需要重置管理员口令时运行：

```sh
./remota-server recover --state-dir "$HOME/.local/state/remota" --reset-admin
```

新口令仍只在当前终端交付。撤销指定凭据可运行以下命令，多个 ID 重复提供 --revoke：

```sh
./remota-server recover --state-dir "$HOME/.local/state/remota" --revoke cred_REPLACE_WITH_ID
```

恢复会核实事务结果，并保守撤销结果未确定的操作所涉及的凭据。若初始化已提交但秘密交付失败，重置口令后须检查并撤销不可得的初始凭据，再登录管理台新建凭据。若恢复标记写入本身失败，也须核对最后成功提交的状态并完成必要撤销。恢复标记损坏、状态缺失、损坏或版本未知时应停止操作，保留原目录排查；不要删除状态、手工修改数据库或把它当作新部署初始化。

## 更换版本与状态保留

1. 核对新包校验值和 release.json，解压到新的程序目录。
2. 正常停止旧服务，确认退出后备份完整状态目录，保留访问权限；备份包含认证摘要，应按敏感数据管理。
3. 从新目录启动，继续使用原来的 --state-dir、监听地址和公开地址，核对健康状态、管理台登录与凭据元数据。设备与 MCP 客户端重新接入并完成授权。
4. 保留旧程序与升级前的离线备份，直到核对完成。当前没有自动升级或数据库迁移承诺；遇到未知状态版本须停止，不直接让旧程序读取新版本状态。

程序目录中的 release.json 和运行说明可以随包替换；状态目录不能被压缩包覆盖，也不应放进候选包。Linux arm64、Windows 与 macOS 的交叉编译结果不构成本说明中的原生运行保证。
