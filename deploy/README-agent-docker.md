# Docker Agent

Agent 使用独立设备 ID 和密钥连接控制面。先在控制台创建设备；不要与另一台设备共用身份。

本次 iStoreOS 部署的实际配置、Orca 公网 IP 入口、验证结果和排障记录见 [Orca 公网访问与 Docker 客户端部署](../docs/Orca公网访问与Docker客户端部署.md)。

## 从源码构建

在部署目录放置 `compose.yaml`（来自 `deploy/compose.agent.yaml`），将源码放到 `source/`，将配置放到同目录的 `agent.env`：

```dotenv
AIBOT_CONTROL_URL=https://your-control.example.com
AIBOT_DEVICE_ID=your-device-id
AIBOT_DEVICE_KEY=your-device-key
AIBOT_LOG_LEVEL=info
```

准备状态目录并启动：

```sh
mkdir -p state
chown 65532:65532 state
chmod 700 state
chmod 600 agent.env
docker compose -f compose.yaml build
docker compose -f compose.yaml up -d
```

镜像使用静态 Go 可执行文件及 CA 证书，进程以 UID/GID 65532 运行。容器根文件系统只读，状态目录可写。使用主机网络以访问主机所在内网，无需发布管理端口。`restart: unless-stopped` 使容器随 Docker 恢复启动。

## 无基础镜像下载的构建

若 Docker Hub 不可达，可在有 Go 1.27.3 的开发机执行：

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o agent ./cmd/agent
```

将编译产物 `agent`、可信系统的 `ca-certificates.crt` 和 `deploy/Dockerfile.agent.binary` 放到单独的 `image/` 目录，Dockerfile 重命名为 `Dockerfile`。不要把 `agent.env` 放入构建目录。

```sh
chmod 555 image/agent
docker build -t server-aibot-agent:local image
docker compose -f compose.yaml up -d --no-build
```

## iStoreOS 存储

本次部署目录为 `/mnt/sda4/server-aibot-agent`，状态保存在其 `state/` 下。Docker 自身的数据目录为 `/mnt/sda4/docker`。确保 ext4 分区 `/mnt/sda4` 已挂载并配置开机自动挂载，再启动 Docker。

验证运行和控制面在线状态：

```sh
docker inspect --format '{{.State.Status}} restart={{.RestartCount}}' server-aibot-agent
docker logs --tail 30 server-aibot-agent
```

还需在控制台确认设备心跳为在线。容器运行状态不能代替控制连接或实际业务转发验证。
