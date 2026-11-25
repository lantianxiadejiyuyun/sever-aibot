# P2P UDP NAT 打洞与传输

P2P 现在支持通过节点进行认证 UDP 会合、交换实际观察到的地址、双端在同一个 UDP socket 上同时打洞，再使用 QUIC 建立可靠的 TLS 1.3 加密数据流。原有 TCP 候选连接仍保留，用于局域网或显式端口映射；TCP 候选连接不是自动 NAT 打洞。

## 配置

在节点进程配置独立的 UDP 会合监听地址，例如：

```text
AIBOT_NODE_RENDEZVOUS_LISTEN=0.0.0.0:7001
```

为控制面的节点资源设置 `rendezvousAddress`，例如 `node.example.com:7001`。该值必须是双方 Agent 能访问的实际 UDP 公网端点。如果节点位于 NAT 后，需为会合服务配置 UDP 端口映射。防火墙、安全组和容器端口发布也必须允许对应 UDP 端口，例如 `7001:7001/udp`。

会合监听为显式启用，不从 HTTPS/WebSocket 域名或端口推断。HTTP 反向代理不能代替 UDP 端口发布。会合端口不能与节点的 QUIC/KCP 隧道监听争用同一个 UDP 地址和端口。

已有 P2P 转发无需填写 Agent 的公网端口映射：控制面向双方下发节点的 `rendezvousAddress` 后，Agent 自动尝试 UDP 路径。未配置会合地址时，继续使用既有 TCP 候选与转发回退策略。

## 协议与授权

1. Agent 在已认证的节点隧道内发送 `@nat:<revision>:<forwardId>` 控制请求。节点检查设备角色、转发范围、当前配置版本、存活会话和策略。
2. 提供端提交短期证书指纹；访问端取得同一指纹。每个角色分别得到随机 UDP 凭证。凭证仅在控制流中交付，不在 UDP 报文中明文发送密钥。
3. 双方从后续 QUIC 使用的同一个 UDP socket 发送带 HMAC 的注册报文。节点只交换自己实际观察到的 UDP 源地址，不接受客户端声称的公网候选。
4. 会合响应带 HMAC，并绑定本次注册随机数。双方收到经过认证的地址后，从原 socket 向对端发送探测包，同时开放 NAT 过滤状态。
5. 访问端使用原 socket 建立 QUIC，验证提供端证书指纹。数据流还需通过绑定转发 ID 和版本的随机挑战认证，之后提供端才按 Agent 的目标地址策略连接目标。

节点会合服务只交换地址，不传输业务载荷。注册表有容量上限；访问端凭证有效期为 30 秒，提供端凭证为 55 分钟，提供端随既有直连证书机制在约 50 分钟时刷新会话。凭证的有效性每次使用都会重新检查，设备会话失效或版本变化后不再提供地址交换。

正常数据流关闭先发送 QUIC FIN，保留最多 5 秒用于排空已排队的数据，避免目标关闭时截断最后一段响应。撤销使用强制关闭，不等待正常排空。配置撤销、设备会话断开和提供端直连服务关闭会终止关联的 UDP socket 与活动数据流。

## 回退与限制

一次访问的直连尝试总预算为 1.5 秒，其中 UDP 会合和 QUIC 尝试最多使用约 900 毫秒；剩余预算可尝试原有 TCP 候选。失败后按配置回退节点转发；禁止回退时连接失败并报告不可用，不会悄悄启用转发。

以下环境可能无法建立 UDP 直连：

- 地址或端口随目的端变化的对称 NAT，即端点相关映射。
- 禁止 UDP 的防火墙、运营商网络、部分 CGNAT，以及不支持所需 NAT 回环行为的路由器。
- 会合服务与对端的网络地址族不兼容，或链路建立时间超过有界直连预算。
- 会合服务没有可达的公网 UDP 端点。

实现不进行端口预测、UPnP 自动映射或无限重试，也不保证能穿透所有家用或运营商 NAT。没有成功直连的场景仍依赖允许的节点转发。

配置节点流量总额、带宽、并发或连接数量限制时，TCP 与 UDP 直连均禁用，由节点转发执行限制，防止 P2P 载荷绕过配额。若同时禁止转发，该 P2P 转发不可用。策略启用后已有直连会话也会撤销。

## Agent 到节点的 KCP / QUIC 隧道

节点转发通道也支持 KCP 和 QUIC。两者都保留原有短期票据验证、设备与节点身份绑定、转发范围授权及 yamux 多路复用。KCP 的可靠 UDP 字节流外层使用 TLS 1.3；QUIC 使用协议自带的 TLS 1.3。Agent 使用操作系统信任根验证节点证书及其主机名，不提供跳过证书验证的模式。

以下为 QUIC 节点进程配置示例，KCP 将 `quic` 改为 `kcp`：

```text
AIBOT_NODE_TUNNEL_TRANSPORT=quic
AIBOT_NODE_TUNNEL_LISTEN=0.0.0.0:7000
AIBOT_NODE_TLS_CERT=/etc/server-aibot/node-fullchain.pem
AIBOT_NODE_TLS_KEY=/etc/server-aibot/node-key.pem
AIBOT_NODE_TUNNEL_HTTP=0
```

控制面节点的接入协议应选择相同的 `quic` 或 `kcp`，地址填写实际公网主机名和端口，例如 `node.example.com:7000`。下发给 Agent 的地址分别为 `quic://node.example.com:7000`、`kcp://node.example.com:7000`。证书必须覆盖该主机名；如使用 IP 地址，证书必须包含对应 IP SAN。使用内部 CA 时，需预先将 CA 安装到运行 Agent 的操作系统信任库。

防火墙、安全组、NAT 映射和 Docker 端口发布必须放行隧道端口的 UDP，例如 `7000:7000/udp`。普通 HTTP / WebSocket 反向代理不能承载这些通道；节点进程需要直接加载 TLS 证书。缺少 TLS 证书或同时启用 `AIBOT_NODE_TUNNEL_HTTP` 时，KCP/QUIC 配置会被拒绝。一个节点进程选择一种接入传输；未设置传输类型时保持默认 TCP 行为。

需要 P2P 打洞时，另行启用上一节的 `AIBOT_NODE_RENDEZVOUS_LISTEN`，例如 `0.0.0.0:7001`，并发布此独立 UDP 端口。KCP/QUIC 隧道监听和会合监听不能绑定相同 UDP 地址与端口。

拨号、TLS 握手及票据认证受超时和取消控制；节点关闭会关闭监听、认证中的连接和已注册会话。UDP 上发送认证拒绝后，节点允许对端读取拒绝结果再关闭，等待仍受认证截止时间限制。KCP 对端突然消失时通过既有 yamux 心跳检测断线，心跳间隔为 30 秒；不保证远端立即感知 UDP 连接消失。

`internal/node/transport_test.go` 使用真实 UDP 回环 socket、节点运行时和 Agent 协议对端验证两种传输的 TLS、票据及 yamux 大块数据往返，覆盖错误证书主机名、不可信证书、无效票据、超出票据范围的转发和节点关闭清理。`internal/tunnel/transport_test.go` 验证地址解析与拨号取消；`internal/config/config_test.go` 验证 TLS 必填及 HTTP 模式冲突。测试未替代生产公网 UDP 可达性及证书链部署验收。

## 本地验证

对应测试文件：

- `internal/tunnel/nat_test.go`：实际 UDP socket、模拟端点无关地址映射与端口限制过滤、观察地址交换、QUIC 大块数据传输、最终响应排空、伪造注册拒绝、撤销以及错误证书指纹拒绝。
- `internal/agent/nat_test.go`：提供端与访问端真实 UDP 数据路径、目标连接、会话关闭撤销和 UDP 黑洞情况下的有界回退。
- `internal/node/nat_test.go`：真实节点认证隧道中的角色、范围、版本检查，配额与速率/连接策略限制，以及提供端撤销后的查询拒绝。

运行：

```text
go test ./internal/tunnel ./internal/agent ./internal/node
```

模拟 NAT fixture 使用不同的内部地址与实际 UDP 源地址，并拒绝尚未出站访问过的对端入站包。它验证地址交换和同 socket 双向打洞的实现行为，不代表已完成真实路由器、跨运营商或生产公网 NAT 的互通认证。部署验收应分别在可直连、UDP 被禁和对称 NAT 场景检查直连成功或按策略回退。
