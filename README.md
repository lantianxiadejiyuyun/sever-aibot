# AI 服务器巡检与控制服务

当前 `go-rewrite` 分支使用 Go 控制面、独立 Agent 和公网中继节点，以及内嵌 Vue 控制台。面向单管理员自建场景，管理资产、凭据、SSH 终端、转发、指标、巡检、通知与 AI 审批。

## 构建与运行

需要 Go 1.27+ 和 Node.js 22+。在仓库根目录执行：

```sh
npm --prefix web ci
npm --prefix web run typecheck
npm --prefix web run build
go build -o bin/control ./cmd/control
go build -o bin/agent ./cmd/agent
go build -o bin/node ./cmd/node
go test ./...
```

`web/dist` 由控制面二进制内嵌，因此更新前端后需要重新构建控制面。运行所需环境变量、设备和节点注册方式见 [Go 控制面运行与 API](docs/Go控制面API.md)。管理员初始口令只存哈希；设备密钥加密保存，管理员二次验证后可再次查看，旧设备可验证现有密钥后加密保留；节点密钥仍仅在创建或轮换时显示一次；主密钥必须持久保存，并与数据库分别备份。

项目现有域名为 `https://aibot.eugenweb.cn` 与节点入口 `wss://node.eugenweb.cn/tunnel`。生产部署沿用宝塔 API 流程；仓库中的本地验证不代表已部署到这些地址。环境凭据位于 Git 忽略的 `docs/private/`，不得提交。

## 使用与验收

| 功能 | 说明与文档 |
|---|---|
| Windows 客户端 | 原生 GUI、托盘常驻、自动重连、登录启动和 DPAPI 加密配置：[Windows 客户端](docs/Windows客户端.md) |
| 资产与终端 | 凭据绑定、Agent 反向访问、跳板、SSH 指纹固定、PTY、会话历史与录制：[资产访问与终端](docs/资产访问与终端.md) |
| 数据源 | PostgreSQL/MySQL 登记、schema 与受限只读查询，复用资产连接与凭据；页面 `/datasources` |
| 转发 | TCP/UDP、HTTP Host、私有访问、受限 CONNECT/SOCKS5 代理、认证 TCP/QUIC 直连与中继回退：[转发验收](docs/转发与直连验收.md)；[自动 NAT 打洞与 KCP/QUIC 接入](docs/NAT打洞与UDP传输.md)；[流量额度](docs/流量额度.md) |
| 监控与巡检 | Linux/Windows 真实指标与离线内存补传、阈值、固定只读 SSH 检查、批次报告、告警和通知：[巡检与通知](docs/巡检与通知.md) |
| AI | 服务商、任务默认模型、模板快照、会话事实、日志/历史摘要、受限工具与人工审批、逐次调用用量及费用：[AI 模型与审批](docs/AI模型与审批.md) |
| 系统安全 | TOTP、恢复码、会话撤销、审计导出与离线主密钥轮换：[系统管理与密钥轮换](docs/系统管理与密钥轮换.md) |

[实施与验收清单](docs/后续功能实施与验收.md) 记录当前完成度和限制，[本地功能验收记录](docs/本地功能验收记录.md) 记录实际运行的流程。外部模型调用、真实通知送达、跨 NAT 访问、远程数据库以及手机安装需在对应环境单独验收。

AI 写操作保留人工审批，高风险操作要求 TOTP。固定 SSH 配置变更只适用于显式登记的手工管理目标；宝塔托管生产配置使用既有宝塔 API 部署流程。

## 目录

- `cmd/control`、`internal/api`、`internal/store`：控制面、HTTP API 与 SQLite 持久化。
- `cmd/agent`、`internal/agent`：内网 Agent、指标采集与资产访问。
- `cmd/node`、`internal/node`：公网节点与转发数据通道。
- `web`：Vue 控制台、PWA 静态资源与 Go embed。
- `cmd/vault-rotate`：停服执行的主密钥轮换工具。

`backend/`、`frontend/`、`tunnel/` 中的 Python 版本保留作迁移参考，旧部署脚本和旧环境变量不适用于当前 Go 运行方式。设计依据见 [总体规划](docs/AI服务器运维与内网穿透平台总体规划.md)、[Web 控制台与资产访问规划](docs/Web控制台与资产访问规划.md) 和 [端口转发与 P2P 需求](docs/端口转发与P2P需求.md)。规划描述目标，实际支持范围以实现与验收记录为准。
