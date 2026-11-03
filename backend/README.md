# 控制后端
在项目根目录用 Python 3.10+ 安装 backend/requirements.txt（FastAPI、Paramiko、cryptography、httpx、uvicorn）。单进程运行：

    python -m uvicorn backend.main:app --host 127.0.0.1 --port 8000 --workers 1 --no-access-log

控制服务需要 AIBOT_ADMIN_PASSWORD（至少 12 字符）、AIBOT_MASTER_KEY（Fernet 密钥）和可写 AIBOT_DB（默认 var/aibot.sqlite3）。管理员在部署环境的受限凭据文件或服务密钥库中配置，勿写入仓库；备份主密钥与数据库时分开保管。生成主密钥可用 Python 的 cryptography.fernet.Fernet.generate_key()。主密钥丢失将无法恢复凭据。反代应保留 Host，通过 HTTPS 暴露 /api。会话 cookie 默认要求 HTTPS；仅本机 HTTP 开发可设 AIBOT_COOKIE_SECURE=0。启动不扫描或自动导入任何 SSH 文件。不要把进程环境、认证请求或响应体写入代理日志。

管理端与 gostc 配置相同的 GOSTC_CONTROL_SECRET，其他设置见 tunnel/README.md。节点、客户端、访客的接入密钥在管理机器通过 tunnel.enroll 领取，不在任务、聊天或报告中展示。每个节点和客户端关联创建时选择的平台主机，规则另关联服务。已保存/待应用、已验证分开。规则变更后只有外部探测节点处于外部网络且设置 AIBOT_EXTERNAL_PROBE=1 才探测服务登记的公网入口；可设 AIBOT_PROBE_NAME 标明位置。仅本次业务响应匹配能作为业务证据。

巡检和部署须在目标机建立独立的非 root 账号、独立密钥和最小远程权限；管理端拒绝账号/密钥混用。巡检通过预定义 Linux 命令，部署仅支持 Docker Compose v2：使用既有配置的服务，image 从已登记镜像变量读取，例如 image: ${APP_IMAGE}，目录、配置路径、镜像变量、HTTP(S) 健康检查须提前登记。通过 compose config 在执行前验证变量实际生成目标镜像；只 pull/up 指定服务。成功后持久化运行镜像/摘要/宿主端口与健康证据。部署不改远程 compose.yaml 或 .env，外部自动部署工具需自行同步镜像变量。回退须用另一个明确确认的任务；不会自动把未知状态标成成功。模型仅能调用固定工具，不能传入命令。模型设置中填写兼容 API 的基础路径（例如 https://provider.example/v1），服务端追加 /chat/completions；远程地址须 HTTPS，仅允许本机接口使用 HTTP。

SQLite 保存任务、报告、通知、绑定、会话和经加密的密钥。一个后台线程领取任务；进程重启恢复 queued，running 标成 unknown，禁止重放可能部分执行的写入。用户断线不取消服务器任务。数据库文件和本地代理仅允许服务账号访问，Linux 建议私有状态目录和 UMask=0077。

巡检单独记录面板版本、宝塔数据库转发记录、系统 iptables 规则及 Nginx 反代摘要（只返回上游主机、协议、端口和配置文件名）。宝塔转发表使用 [aaPanel 的 firewall_forward 字段](https://github.com/aaPanel/aaPanel/blob/master/class/firewallModel/comModel.py)；同时兼容旧 firewall_trans 表。12.0.0 从 class/common.py 中只读匹配 g.version，旧版可从 data/version.pl 获取版本；会列出可读的数据表及其结果，空表不等于读取失败。没有支持的表或无读取权限时列为未核实。公网 HTTP(S) 探测需要预期响应特征；普通 TCP/UDP 需登记请求和匹配内容。规则验证只使用匹配节点地址与映射端口的已登记入口，P2P 不使用公网探测证明直连。

针对性验证：在仓库根目录运行 `python -m unittest -v backend.test_control`。测试隔离使用临时 SQLite、本机模型协议服务和内存生成的 SSH 身份；不扫描用户密钥，不访问线上服务。

普通账号无法读取宝塔数据库时，可按 [最小权限安装与现场核验](BT_HELPER.md) 安装可选助手。该授权只允许一个固定路径、无参数的证据命令。

手机 Web Push：在控制服务环境中配置 AIBOT_VAPID_SUBJECT（例如管理员实际的 mailto 联系地址）并重启。首个订阅前生成 VAPID P-256 密钥，私钥和订阅能力地址用平台主密钥加密保存；请同时保留数据库和独立主密钥的备份。浏览器须在受信任的 HTTPS 来源中授权通知并订阅；仅有站内通知记录不代表设备已收到。未配置时推送保持关闭，不影响巡检。独立发送线程使用 aes128gcm 加密推送，订阅过期自动撤销。故障持续期间只发一次，恢复后再发；网络结果不明不会盲目重发。
