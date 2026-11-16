# Go 控制面运行与 API

本文对应 `go-rewrite` 分支的 Go 控制面（M1 骨架）。设计背景见
[Web 控制台与资产访问规划](Web控制台与资产访问规划.md)。

## 1. 构建与启动

需要 Go 1.27+ 与 Node.js 22+。

```sh
# 构建前端（产物在 web/dist，由 Go 二进制内嵌）
cd web && npm install && npm run build && cd ..

# 构建三个二进制
go build -o bin/control ./cmd/control
go build -o bin/agent ./cmd/agent
go build -o bin/node ./cmd/node
```

控制面首次启动必须提供主密钥与初始管理员口令。主密钥只从环境变量或
`/etc/server-aibot/master.key`（0600）读取；初始口令只在第一次建库时使用，
经内存困难哈希后入库，不会写回磁盘或日志。

```sh
export AIBOT_MASTER_KEY="$(openssl rand -base64 32)"
export AIBOT_ADMIN_PASSWORD='<至少 8 位的临时口令>'
export AIBOT_DB=var/aibot.sqlite3
export AIBOT_LISTEN=127.0.0.1:7400
./bin/control
```

生产环境使用 `deploy/control.env.example` 与
`deploy/server-aibot-control.service`，通过 TLS 反向代理或 SSH 隧道访问，
不要直接监听公网。`AIBOT_SECURE_COOKIE=1` 时应只通过 HTTPS 访问。

Agent 使用设备创建时一次性返回的密钥：

```sh
export AIBOT_CONTROL_URL=https://panel.example.com
export AIBOT_DEVICE_ID=dev_...
export AIBOT_DEVICE_KEY='<一次性设备密钥>'
./bin/agent
```

设备密钥只放在环境变量或 0600 文件中，禁止放进命令行参数。

公网节点运行 `cmd/node`，使用节点创建时一次性返回的密钥：

```sh
export AIBOT_CONTROL_URL=https://panel.example.com
export AIBOT_NODE_ID=nod_...
export AIBOT_NODE_KEY='<一次性节点密钥>'
export AIBOT_NODE_TUNNEL_LISTEN=0.0.0.0:7000
export AIBOT_NODE_ENTRY_HOST=0.0.0.0
./bin/node
```

节点详情中的「接入地址」必须与内网 Agent 能访问到的
`AIBOT_NODE_TUNNEL_LISTEN` 公网地址一致。需要 TLS 时设置
`AIBOT_NODE_TLS_CERT` / `AIBOT_NODE_TLS_KEY`，Agent 会用系统根证书校验。

若节点放在 nginx 等反向代理后面，设置 `AIBOT_NODE_TUNNEL_HTTP=1`，让隧道以
WebSocket（`/tunnel`）提供，由代理终止 TLS；此时「接入地址」写成
`wss://node.example.com`（可带路径，缺省 `/tunnel`），Agent 会自动按 WebSocket
拨号。反代需要转发 `Upgrade`/`Connection` 头，并把读超时调大。

## 2. 认证

- `POST /api/v1/auth/login` `{username,password}` 成功后设置 HttpOnly、
  SameSite=Lax 的 `aibot_session` Cookie，并返回 `csrfToken` 与过期时间。
- 除 GET 外的所有浏览器请求必须带 `X-CSRF-Token`；同时校验 `Origin`
  与请求 Host 一致。控制面在反向代理后面时，请求 Host 是回环地址，
  需要把 `AIBOT_PUBLIC_HOST` 设为浏览器访问的域名，该域名也会被接受。
- `GET /api/v1/auth/me` 用于页面刷新后恢复登录态与 CSRF Token。
- 登录失败按 IP 限流：10 分钟内最多 10 次。
- 会话默认 12 小时，服务端只保存 Token 的 SHA-256。

错误统一为：

```json
{"error":{"code":"invalid_argument","message":"节点名称不能为空"}}
```

## 3. 已实现接口（M1）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/healthz` | 健康检查，不需要登录 |
| POST | `/api/v1/auth/login` | 登录 |
| POST | `/api/v1/auth/logout` | 退出，需要 CSRF |
| GET | `/api/v1/auth/me` | 当前用户、CSRF 与 step-up 截止时间 |
| POST | `/api/v1/auth/step-up` | 重新验证管理员口令，开启 5 分钟敏感读窗口 |
| GET | `/api/v1/overview` | 设备/节点/转发计数与最近审计 |
| GET | `/api/v1/audit?limit=` | 审计日志 |
| GET/POST | `/api/v1/devices` | 设备列表 / 创建 |
| GET/PATCH/DELETE | `/api/v1/devices/{id}` | 设备详情 / 修改 / 删除 |
| POST | `/api/v1/devices/{id}/rotate-key` | 轮换设备密钥 |
| GET/POST | `/api/v1/nodes` | 节点列表 / 创建（响应含一次性 `nodeKey`） |
| GET/PATCH/DELETE | `/api/v1/nodes/{id}` | 节点详情 / 修改 / 删除 |
| POST | `/api/v1/nodes/{id}/rotate-key` | 轮换节点密钥并断开在线节点 |
| GET/POST | `/api/v1/forwards?device=&node=&kind=` | 转发列表 / 创建 |
| GET/PATCH/DELETE | `/api/v1/forwards/{id}` | 转发详情 / 修改 / 删除 |
| POST | `/api/v1/forwards/{id}/apply` | 下发配置到节点与 Agent（当前支持 `port` 类型） |
| GET/POST | `/api/v1/credentials` | 凭据列表（仅元数据）/ 创建 |
| PATCH/DELETE | `/api/v1/credentials/{id}` | 修改（可重新加密）/ 删除 |
| POST | `/api/v1/credentials/{id}/reveal` | 取回明文，需要 CSRF + 5 分钟内 step-up，每会话每分钟 5 次；`ssh_key` 默认拒绝导出 |
| POST | `/api/v1/credentials/{id}/test` | 预留，当前返回 501 |
| GET/POST | `/api/v1/assets?device=&tag=&q=` | 资产列表 / 创建 |
| GET/PATCH/DELETE | `/api/v1/assets/{id}` | 资产详情 / 修改 / 删除 |
| GET | `/api/v1/assets/{id}/route` | 解析最多 3 跳的访问路径 |
| POST | `/api/v1/assets/{id}/check` | 预留，当前返回 501 |
| GET | `/api/v1/assets/{id}/ssh-config` | 生成不含密钥的 OpenSSH 配置块 |

列表响应为 `{"items":[...],"total":n}`。设备创建与密钥轮换响应中的
`deviceKey`、节点创建与轮换响应中的 `nodeKey` 都只出现一次，服务端只保存哈希。

`apply` 的状态流转为 `saved → applying → applied | error`。节点离线或 Agent
离线时仍会保存目标状态，二者重新连接后自动下发；节点绑定入口端口成功后回报
`bind_result`，失败时把错误写入 `lastError`。

节点能力开关为 `features.{web,forward,tunnel,p2p,proxy}`。创建转发时会校验
对应能力是否开启，并校验 `forwardPorts`（形如 `20000-30000,40000`）是否包含
入口端口；同一节点同一端口由部分唯一索引保证不冲突（409）。

凭据信封加密（argon2id，AAD 绑定记录 ID 与类型）、step-up 后显式
reveal、限流与审计、资产路径解析已经接入。巡检、性能、通知与 AI 接口在后续里程碑接入；
未实现的路径返回 501 与明确说明，不会伪造成功状态。

## 4. Agent 协议

- WebSocket：`/agent/ws`
- 请求头：`X-Device-ID`、`Authorization: Bearer <device-key>`
- 文本帧 JSON：

```json
{"type":"hello","version":"0.1.0","os":"linux","arch":"amd64"}
{"type":"heartbeat","ts":1760000000}
```

控制面回复 `hello_ack` / `heartbeat_ack`，并每 30 秒发送 WebSocket Ping。
Agent 20 秒一次心跳；90 秒无消息即断开重连。控制面重启时会把所有设备与节点
标为离线，避免残留在线状态。

Agent 在 `hello_ack` 后发送 `config_request`，控制面回复：

```json
{"type":"config","nodes":[{"nodeId":"nod_...","addr":"wss://node.example.com",
  "tls":false,"ticket":"...","forwards":[{"id":"fwd_...","targetIp":"192.168.1.10",
  "targetPort":3389,"remotePort":20001}]}]}
```

### 节点控制协议

- WebSocket：`/node/ws`
- 请求头：`X-Node-ID`、`Authorization: Bearer <node-key>`
- `hello` 中的 `tls` 表示该节点的隧道监听是否启用 TLS
- 控制面下发 `config`：`tunnelSecret`（base64，仅节点内存持有）+ 该节点的
  全部生效转发
- 节点回报 `bind_result`（`forwardId`、`ok`、`message`）和周期性的 `traffic`
- `kick_device` 用于设备密钥轮换后关闭其隧道会话

### 数据面（节点中继）

```
用户 → 节点入口端口 → yamux 流 → 内网 Agent → 目标 TCP
```

1. Agent 主动连接节点的隧道地址，发送 8 字节 magic `SAIBOT01`、长度前缀的
   JSON 认证帧（`deviceId` + 控制面签发的 `ticket`）。
2. 节点用控制面下发的 `tunnelSecret` 验证 HMAC 与有效期；票据绑定设备、节点
   和转发 ID，默认 24 小时，节点不落盘。
3. 认证通过后建立 yamux 会话（节点为 client，Agent 为 server）。
4. 每个公网连接，节点打开一条流并写入转发 ID；Agent 按自己的配置查找目标，
   经 `internal/relay` 策略校验后拨号，返回状态字节并双向透传。
5. 目标策略仍拒绝云元数据地址，回环地址只放行显式端口；Agent 不监听任何
   公网端口。

节点入口端口只监听 `AIBOT_NODE_ENTRY_HOST`，端口范围来自节点的
`forwardPorts`；删除或修改转发会重新推送完整配置，节点据此关闭旧监听。

## 5. 安全边界

- 控制面持有目标凭据；Agent 只做 TCP 字节转发，不接收凭据。
- Agent 侧策略默认只允许 RFC1918 / IPv6 ULA；拒绝云元数据地址、组播、
  未指定地址；回环地址仅放行显式配置的端口。
- 设备密钥与节点密钥相互独立，Agent 不能注册成节点。
- 节点隧道票据由主密钥派生的节点密钥签名，短期有效；节点只在内存中持有签名
  密钥，日志不输出票据或密钥。
- 所有敏感操作写审计；列表与日志不返回密文或明文密钥。
- SQLite 开启 `foreign_keys=ON`、WAL 与 busy timeout；控制面只能单进程运行。
- Web 响应启用 CSP、`X-Frame-Options: DENY`、`nosniff`。

## 6. 测试

```sh
go test ./...
go vet ./...
```

API 测试覆盖登录/CSRF、设备与节点一次性密钥、节点能力与端口段校验、端口冲突、
Agent 握手与在线状态。凭据信封加密与资产路径解析的测试在对应包内。
`internal/tunnel` 覆盖票据签名/过期/篡改与协议帧；`internal/node` 的端到端测试
会启动真实控制面、节点运行时和 Agent，验证 公网端口 → 节点 → 隧道 → 内网目标的
完整链路。
