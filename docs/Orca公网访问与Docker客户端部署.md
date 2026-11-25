# Orca 公网访问与 Docker 客户端部署

记录日期：2026-11-17。本文记录本次部署的实际配置和验证结果。

## 当前访问地址

目前使用 **公网 IP + 端口** 访问 Orca Web：

**http://47.118.21.177:10000/**

同一局域网内可以直接访问：

**http://192.168.100.143:6768/**

`6768` 是本机 Orca 当前配置中实际使用的端口，并非对所有 Orca 版本默认端口的声明。IP 地址和端口只提供网络入口；配对和身份验证仍由 Orca 处理。

## 访问链路

```text
访问端浏览器
    │ HTTP / TCP
    ▼
公网服务器 47.118.21.177:10000
    │ 复用 Agent 主动建立的 WebSocket 隧道
    ▼
iStoreOS 192.168.100.206
    │ Docker 容器 server-aibot-agent，使用主机网络
    ▼
运行 Orca 的电脑 192.168.100.143:6768
```

Orca 桌面客户端运行在 Windows 电脑上。iStoreOS 上部署的是本项目 Agent，负责将公网中继的连接转发到内网 Orca。

Agent 的控制连接和数据中继连接分别是：

| 用途 | 地址 |
|---|---|
| 控制面连接 | `https://aibot.eugenweb.cn`，实际通过 WSS 连接 `/agent/ws` |
| 数据中继连接 | `wss://node.eugenweb.cn`，当前客户端默认使用 `/tunnel` 路径 |
| 浏览器公网入口 | `http://47.118.21.177:10000/` |

使用 IP 访问网页无需修改以上控制面和中继域名；它们的 TLS 证书按域名校验。

## 已部署的资源

### 磁盘与目录

| 项目 | 当前配置 |
|---|---|
| 数据分区 | `/dev/sda4`，约 57.3 GiB，ext4 |
| 挂载目录 | `/mnt/sda4` |
| 自动挂载配置 | `/etc/config/fstab` 中的 `data_sda4`，按文件系统 UUID 挂载 |
| Docker 数据目录 | `/mnt/sda4/docker` |
| Agent 部署目录 | `/mnt/sda4/server-aibot-agent` |
| Compose 文件 | `/mnt/sda4/server-aibot-agent/compose.yaml` |
| 接入环境文件 | `/mnt/sda4/server-aibot-agent/agent.env`，权限 `0600` |
| Agent 状态目录 | `/mnt/sda4/server-aibot-agent/state` |
| 离线镜像构建目录 | `/mnt/sda4/server-aibot-agent/image` |
| 源码目录 | `/mnt/sda4/server-aibot-agent/source` |

该分区已重新创建并格式化。初次挂载后可用空间约 53.2 GiB，具体以 `df -h` 为准。

### 容器

| 项目 | 当前配置 |
|---|---|
| 容器名称 | `server-aibot-agent` |
| 镜像名称 | `server-aibot-agent:local` |
| 重启策略 | `unless-stopped` |
| 网络模式 | `host` |
| 运行 UID/GID | `65532:65532` |
| 容器状态路径 | `/state`，绑定到宿主机 `state/` |
| 容器根文件系统 | 只读 |
| Linux capabilities | 全部移除 |

Docker 已启用开机启动，数据分区已配置自动挂载，容器已配置自动重启。本次没有重启整台主机验证启动顺序。手动停止容器后，`unless-stopped` 会保留停止状态，需要手动再次启动。

### 控制台设备与转发规则

| 项目 | 当前配置 |
|---|---|
| 设备名称 | `iStoreOS-192.168.100.206` |
| 设备 ID | `dev_98051ae1ee04ac1013a83549` |
| 中继节点 | `云端节点-01` |
| 中继节点 ID | `nod_8f737b9d4f876d0872abb054` |
| 转发规则名称 | `我的笔记本` |
| 转发规则 ID | `fwd_3757c709d6edc5c2ca1a8be1` |
| 规则类型 | 端口转发，当前使用 TCP |
| 公网端口 | `10000` |
| 内网目标 | `192.168.100.143:6768` |

设备使用独立接入密钥，不与原 Windows Agent 共用身份。本文不记录密码、设备密钥或配对链接。

## 构建与更新

通用构建说明见 [Docker Agent 部署说明](../deploy/README-agent-docker.md)。

本次下载 Docker Hub 的 Go 基础镜像时发生认证连接 `EOF`，因此采用以下方式完成部署：

1. 在开发机使用 Go 1.27.3 交叉编译静态 Linux amd64 Agent。
2. 将可执行文件和可信系统 CA 证书放入独立构建目录。
3. 使用 `FROM scratch` 构建镜像，无需下载基础镜像。
4. 使用 Compose 启动容器。

更新时应使用包含 WebSocket 中继支持的当前源码。POSIX shell 下的编译命令如下；该命令在项目根目录执行，编译产物命名为 `agent`：

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags='-s -w' -o agent ./cmd/agent
```

将编译产物上传到 iStoreOS 的 `image/agent` 后，在 iStoreOS 执行：

```sh
cd /mnt/sda4/server-aibot-agent
chmod 555 image/agent
docker build -t server-aibot-agent:local image
docker compose -f compose.yaml up -d --no-build --force-recreate
```

本次更新前保留的回退镜像标签为 `server-aibot-agent:before-ws-update`。该旧镜像不支持当前配置使用的 WSS 中继地址，不应作为日常运行版本。

## iStoreOS 开机自启动与挂载保护

2026-11-17 后续检查发现 `/dev/sda4` 未挂载，Docker 在 `/mnt` 的 tmpfs 路径下启动，因而看不到磁盘中原有的容器。停止 Docker、重新挂载分区后，原部署文件和镜像恢复可见，客户端成功启动。

已增加以下配置和保护：

| 文件 | 用途 |
|---|---|
| `/etc/config/server_aibot` | 数据分区 UUID、挂载路径、等待时间和 Compose 路径 |
| `/usr/sbin/aibot-storage-ready` | 校验已挂载分区 UUID；未挂载时等待设备并挂载 ext4 |
| `/etc/init.d/dockerd` | 启动 Docker 前调用挂载检查，失败则拒绝启动 Docker |
| `/etc/init.d/server-aibot-agent` | 开机等待 Docker 就绪，再通过 Compose 启动 Agent |
| `/root/dockerd.before-aibot-storage` | 修改前的 Docker 启动脚本备份 |

`/etc/config/server_aibot` 的实际配置：

```uci
config storage 'storage'
    option uuid 'f63061fe-e3d3-4c1a-a75e-4e844ffce5a0'
    option target '/mnt/sda4'
    option wait_seconds '30'

config agent 'agent'
    option compose_file '/mnt/sda4/server-aibot-agent/compose.yaml'
```

Docker 和 Agent 启动项均已启用。Docker 的 S99 启动项先执行挂载保护，Agent 的 S99 启动项随后等待 Docker 就绪。设备尚未就绪时，挂载检查最多等待约 30 秒；UUID 不匹配或挂载失败时停止本次启动，不在内存文件系统上继续初始化 Docker。

```sh
# 检查挂载和开机启动设置
/usr/sbin/aibot-storage-ready
/etc/init.d/dockerd enabled
/etc/init.d/server-aibot-agent enabled

# 使用系统服务管理客户端
/etc/init.d/server-aibot-agent start
/etc/init.d/server-aibot-agent stop
/etc/init.d/server-aibot-agent restart
/etc/init.d/server-aibot-agent status
```

脚本源码保存在仓库的 [存储检查脚本](../deploy/istoreos-storage-ready.sh) 和 [Agent 启动脚本](../deploy/server-aibot-agent.init)。已完成 shell 语法检查，并实际重启 Docker 验证 Agent 恢复在线、公网返回 Orca Web HTTP 200；没有重启整台主机验证完整开机过程。若系统升级覆盖 `/etc/init.d/dockerd`，需要重新添加 `start_service()` 开头的 `/usr/sbin/aibot-storage-ready || return 1` 检查。

## 常用管理命令

以下命令在 iStoreOS SSH 终端执行。

```sh
# 查看容器状态
docker ps -a --filter name=server-aibot-agent

# 查看运行状态、重启次数和重启策略
docker inspect --format \
  'status={{.State.Status}} restarts={{.RestartCount}} policy={{.HostConfig.RestartPolicy.Name}}' \
  server-aibot-agent

# 查看日志
docker logs --tail 50 server-aibot-agent

# 启动、停止或重启
docker start server-aibot-agent
docker stop server-aibot-agent
docker restart server-aibot-agent

# 查看资源使用
docker stats --no-stream server-aibot-agent

# 确认 Docker 实际数据目录
docker info --format '{{.DockerRootDir}}'

# 确认分区挂载及可用空间
mount | grep /dev/sda4
df -h /mnt/sda4
```

修改 `agent.env` 后，需要重新创建容器以重新载入环境变量；仅执行 `docker restart` 不会更新容器环境：

```sh
cd /mnt/sda4/server-aibot-agent
docker compose -f compose.yaml up -d --no-build --force-recreate
```

## 验证方法与已验证范围

### 内网段

在 iStoreOS 上执行：

```sh
curl --noproxy '*' -I --connect-timeout 5 --max-time 10 \
  http://192.168.100.143:6768/
```

本次返回 `HTTP/1.1 200 OK`。

### Agent 与中继

在日志中确认出现：

```text
agent connected
hello acknowledged
tunnel connected
```

同时确认控制台设备在线，并有新的心跳时间。仅看到容器 `running` 或转发规则 `applied` 不足以证明业务可达。

### 公网入口

从公网访问端执行：

```sh
curl --noproxy '*' -I --connect-timeout 5 --max-time 10 \
  http://47.118.21.177:10000/
```

本次已验证：

- 公网入口返回 HTTP 200，页面标题为 `Orca Web`。
- 公网页面和内网页面均为 5409 字节，SHA-256 一致。
- 页面的入口 JavaScript 文件可以通过公网入口下载，返回 HTTP 200。
- 控制台转发规则的流量计数增长。

上述结果证明 HTTP 页面和静态资源经过中继到达 Orca。尚未验证 Orca 配对登录、浏览器会话 WebSocket、远程会话操作，以及主机重启后的完整恢复流程。公网 IP 入口当前使用 HTTP。

## 已遇到的问题

### Agent 在线，但隧道连接失败

旧容器日志曾出现：

```text
lookup tcp///node.eugenweb.cn: unknown port
```

原因是旧 Agent 将 `wss://node.eugenweb.cn` 当作普通 TCP 地址解析。已用包含 WebSocket 中继支持的当前源码重新构建并替换容器，随后日志出现 `tunnel connected`。

### 控制面证书域名不匹配

旧配置连接 `aibot.dragnoble.top`，而服务器证书对应 `aibot.eugenweb.cn` 和 `node.eugenweb.cn`，导致 TLS 校验失败。

已将 `agent.env` 中的 `AIBOT_CONTROL_URL` 更新为 `https://aibot.eugenweb.cn`，重新创建容器后恢复在线。

### 域名访问返回 403

访问 `http://node.eugenweb.cn:10000/` 时，返回阿里云的 `Non-compliance ICP Filing` 页面。此次采用用户选择的公网 IP + 端口入口：

```text
http://47.118.21.177:10000/
```

### Orca 电脑 IP 改变或休眠

转发目标固定为 `192.168.100.143:6768`。电脑需要保持 Orca 运行且网络可达；如果局域网 IP 改变，需要更新控制台转发规则的目标地址。可通过路由器 DHCP 地址保留维持该地址。
