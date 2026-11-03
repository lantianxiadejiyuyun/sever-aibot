# AI 服务器巡检与控制服务

一个管理员通过同一套服务端和可安装的移动 Web App 管理 Linux 机器。面板提供 SSH 凭据、服务监控、巡检报告、模型和会话、部署档案，以及内置的端口转发与 P2P。前端在 `frontend/`，控制 API 在 `backend/`，穿透模块在 `tunnel/`。

需求与规划见[总体规划](docs/AI服务器运维与内网穿透平台总体规划.md)、[平台需求](docs/Linux服务器AI运维平台需求文档.md)和[端口转发与 P2P 需求](docs/端口转发与P2P需求.md)。本机的宝塔安装记录和登录凭据存放在 Git 忽略的 `docs/private/` 目录。

### 在 Linux 安装

需要 Python 3.10+（含 venv）、Git、OpenSSL、Node.js 22.12+ 和支持上游模块的 Go 工具链（本次构建使用 Go 1.27.3）。使用 systemd 的 Linux 主机可按以下步骤构建。Docker **只在要部署 Compose 服务的被管机上需要**，并非面板运行依赖。

~~~sh
# 将仓库放在 /opt/server-aibot，在其根目录执行
(cd frontend && npm ci && npm run build)
python3 tunnel/build.py --output bin
sudo bash deploy/setup-linux.sh
~~~

构建器获取 gostc-open 的固定提交，应用随仓库分发的裁剪补丁，产出真正的 `tunnel-control` 和 `gostc`。可用 `--source /path/to/local/gostc-open` 改用包含同一提交的本地克隆。构建前端和隧道也可在开发机完成后分发；跨平台构建示例：`python tunnel/build.py --os linux --arch amd64 --output bin`。部署目录至少须含 app.py、backend/、tunnel/、deploy/、frontend/dist/ 以及 bin/tunnel-control；节点和访问端另需对应架构的 gostc。

安装脚本创建两个无特权服务账号、独立状态目录和 systemd 服务。首次生成管理员密码、凭据加密主密钥、内部控制令牌和本地 TLS 证书；重新安装不覆盖原密钥或数据库。配置项见 [环境变量示例](deploy/env.example)。用 root 在服务器终端读取管理员密码（不要向别人发送环境文件）：

~~~sh
sudo sed -n 's/^AIBOT_ADMIN_PASSWORD=//p' /etc/server-aibot/platform.env
systemctl status aibot-web aibot-tunnels --no-pager
~~~

默认运维面板只监听 `127.0.0.1:18443`（HTTPS）。从管理电脑执行 `ssh -NT -L 18443:127.0.0.1:18443 用户@控制服务器`，浏览器打开 `https://127.0.0.1:18443`。默认是自签名证书，按可信渠道核对；部署至自己的域名时使用可信 TLS。手机可在可信 HTTPS 网站安装 PWA，PC 也能切换手机视图。[Nginx 示例](deploy/nginx.example.conf) 将面板和节点接入分别代理，只公开需要的接口。**不要把内部 18081 端口或 /control 对公网放行。**

### 第一次使用

1. **接入 Linux。** 添加服务器，查看 SSH 主机指纹，并与服务器管理员独立核对；确认后在右栏手动导入一把专用巡检私钥，绑定非 root 账号。跳板和目标机分别认证。部署需再绑定不同的凭据与非 root 部署账号。私钥和口令加密保存，不回显。
2. **查看状态。** 执行一次只读巡检，再打开监控和报告查看采集时间、进程、网络和服务证据。SSH 中断时展示最后观测。宝塔规则、系统 NAT、后端响应、公网入口分别判断；读不到不报告正常。只有控制端确实处在目标网络之外且已登记业务探测时，才开启 `AIBOT_EXTERNAL_PROBE=1`。不具备这个条件时显示“公网未验证”。
3. **接入模型。** 在设置中填写兼容接口、模型名与 API Key，测试连接后选为默认模型。例如 DeepSeek 官方地址为 `https://api.deepseek.com`。会话中可切换模型、查看和调整上下文。只读模式不能发起写操作；部署和转发变更由服务端生成确认任务。凭据不进入提示词。
4. **配置穿透。** 在当前主机下登记节点与客户端；通过受信任的本地管理员终端领取各自的接入密钥（见下文）。在当前服务下创建转发或 P2P 规则。端口映射按上游同时承载 TCP、UDP；P2P 通过 XTCP 优先直连，隧道及节点都允许中继时才配置 STCP 回退。未获得真实路径证据时显示“未确认”。规则保存、客户端回执和业务验证分开记录。
5. **部署服务。** 在服务详情登记一个已存在的 Compose 服务、配置路径、镜像变量、健康检查及期望端口。选择目标镜像，预览和确认后才执行。成功后保存运行镜像、端口及验证时间；失败保留上次成功档案。回退是对上一镜像新建并确认的部署任务。接口和部署前置条件见[后端说明](backend/README.md)与[API 契约](backend/API.md)。

### 运行公网节点、内网客户端和 P2P 访问端

先在控制机本地进入受限管理员终端，载入仅供隧道服务使用的环境变量，按面板所示编号领取密钥：

~~~sh
cd /opt/server-aibot
set -a; . /etc/server-aibot/tunnel.env; set +a
python3 -m tunnel.enroll node 节点编号
# client 客户端编号 / p2p 隧道编号同理。只在交互终端显示。
~~~

在各设备上从受限环境文件或交互输入设置 `GOSTC_AGENT_KEY`；不要将密钥放进命令行参数。节点使用节点登记密钥，内网端使用客户端登记密钥，访问方使用单隧道的访问密钥。

~~~sh
# 在不同设备分别运行；域名替换成自己的 TLS 节点接入地址
bin/gostc -role node    -addr agents.example.invalid:443
bin/gostc -role client  -addr agents.example.invalid:443
bin/gostc -role visitor -addr agents.example.invalid:443 -port 9000
~~~

访问端连接本机 127.0.0.1:9000。若缺乏已验证的公网域名，联调可用独立 SSH 隧道将管理连接接到本机，再指定 `-tls=false -addr 127.0.0.1:18081`；此明文模式只允许回环地址。**节点另有独立的数据端口**：登记的节点地址应是客户端可达的 FRP 地址（如 node.example.org:7000），转发入口选用允许端口段中的端口。SSH 转发控制连接不会使 TCP/UDP 业务入口自动公网可达。[隧道接口说明](tunnel/README.md)

### 开发与验证

~~~sh
python3 -m venv .venv
. .venv/bin/activate
pip install -r backend/requirements.txt
python -m unittest backend.test_control
(cd frontend && npm ci && npm run build)
python tunnel/build.py --output build/native
python -m tunnel.test_transport --bin build/native
~~~

传输验收启动真正的节点、客户端及 visitor，并临时监听端口；建议在测试机或隔离网络命名空间中运行。它验证 TCP/UDP 往返、客户端回执、目标切换、启停、删除、私有访问和中继配置条件；不能据此认定两个真实 NAT 网络一定能打洞。单进程运行控制服务，不要用多个 worker 竞争同一 SQLite 任务队列。数据库与主密钥分开备份；密钥丢失无法恢复加密凭据。

上游版本及授权见 [gostc-open](https://github.com/SianHH/gostc-open/tree/16671e05b8c0e5d32db0146db862bc71c35e39ee) 和[上游许可证](tunnel/LICENSE-gostc-open)。本仓库包含适配器和对该固定版本的补丁，不包含任何实际机器的凭据。

宝塔普通 SSH 账号的只读取证配置见[巡检助手说明](backend/BT_HELPER.md)。首版[测试机验收记录](docs/acceptance.md)区分了规则配置、实际数据传输和公网可达性。
