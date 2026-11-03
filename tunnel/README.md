# gostc 管理适配
公网节点和内网客户端均使用固定版本 gostc-open。平台后端进程通过 loopback HTTP 调用裁剪后的管理服务，不使用管理数据库模拟下发。

Python 接口：`from tunnel.gateway import Gateway`；`Gateway().call(kind, action, data, identity=None)`。kind 为 forward、p2p、node、client。list 返回 `{"data":[...],"status":"observed"}`；check 返回单条；写入返回持久化后的记录或删除编号，status 为 saved、pending_apply 或 pending_revoke，不能当作公网已验证。无法连接、参数错误、上游错误均抛 RuntimeError。identity 可用作规则 id，优先 data.id。调用端负责服务器与规则关联、权限和确认。

- 所有记录有 id、name、version。list/check 不返回任何密钥。节点和客户端的注册密钥、P2P 的访问密钥由管理员使用本地 `python -m tunnel.enroll node|client|p2p 编号` 单独领取，切勿提供给模型。
- node:create(name,address,forward_ports,disable_relay?)，address 含 FRP 监听端口，例如 192.0.2.1:7000，forward_ports 示例 18080-18090。client:create(name)。
- forward:create(name,node_id,client_id,public_port,target_host,target_port)；p2p:create(name,node_id,client_id,target_host,target_port,allow_relay?)。两者 update 传 id 和变化字段；enable 传 id、enabled（布尔）；delete/check 传 id。节点和客户端在线状态是 online；规则含 enabled、node_online、client_online，P2P 路径无证据显示 unknown。
- 平台和 gostc 服务设置同一随机 GOSTC_CONTROL_SECRET（至少 32 字符）；GOSTC_CONTROL_URL 默认为 http://127.0.0.1:18081。仅公开经 TLS 保护的节点注册 WebSocket 和必要 FRP 回调，不公开管理接口。
