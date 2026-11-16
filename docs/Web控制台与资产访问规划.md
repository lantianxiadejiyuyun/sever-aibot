# Web 控制台、凭据中心与跳板访问规划

> 适用版本：Go 重写版（`cmd/control` + `cmd/agent`）
> 状态：设计定稿，待评审
> 关联：总体架构以 Go 版设计为准；本文补充 **前端、凭据、资产/跳板、轮询/轮检、AI 配置** 五块。

---

## 0. 结论先行

| 问题 | 结论 |
|---|---|
| 前端形态 | 浅色企业后台：白卡片、蓝色主色、分组侧栏；一套 Vue 3 代码两套 Shell（桌面侧边栏 / 手机底部标签），断点 640/1024，PWA 可安装，PC 可强制切手机视图 |
| 凭据放哪 | 只存在控制面。ChaCha20-Poly1305 信封加密，主密钥外置，数据库里只有密文 |
| 跳板怎么接 | Agent **不持有凭据**，只做 TCP 字节转发；SSH/DB/HTTP 客户端全部跑在控制面 |
| 原生 SSH | v1 提供 ProxyJump 命令/配置生成，流量不经过控制面；v2 再做浏览器终端 |
| 局域网 Windows | 用 SSH 本地端口转发（`ssh -L 3389:win:3389 -J jump`）；浏览器 RDP 留到 v3（Guacamole） |
| AI 运维 | 多模型接入 + 提示词模板；只读工具默认允许，写操作走"提案 → 人工审批 → 执行"；数据源走 Agent 流转发 |
| 长上下文 | 模型元数据记录 256K/1M；80% 自动压缩、检索优先、token/成本计量 |
| 轮询/轮检 | 控制面内置单调度器；轮询 30s–5min，轮检按 cron；Agent 只执行白名单检查 kind |
| 通知 | 巡检/告警完成后推送飞书；群 Webhook + 应用 Bot 两种模式；卡片、重试、投递日志 |
| 转发管理 | 管理内容对齐 gostc-open：节点能力/端口池/域名策略/准入/流量统计；保留单管理员，去掉积分计费与商用多租户，样式自研 |
| 服务器性能 | Agent 本地 15s 采样推送；原始 7 天 + 1min/5min 降采样；阈值复用检查告警 |
| 权限模型 | v1 单管理员；表结构预留 `users/roles`，不提前做 RBAC |
| 仓库 | 先 `git restore .`；旧 Python 版打 tag/分支，Go 重写在同一仓库继续（历史与文档不丢） |

两个默认值（可逆）：

1. **Agent 不落任何凭据**，收到的是"帮我连 `host:port`"，不是用户名密码。
2. **会话录制默认开启**，asciinema v2 格式，保留 30 天，路径与保留期可配。
3. **轮询默认 60s、最小 30s，轮检默认每日 03:30**；Agent 侧只执行结构化检查，不接受任意命令。

---

## 1. 总体架构

```
        手机浏览器 / 桌面浏览器 / PWA
                    │  HTTPS + WSS（同源）
                    ▼
┌─────────────────────────────────────────────────────────┐
│ cmd/control                                               │
│                                                           │
│  api/chi ── auth/session ── CSRF ── 限流 ── 审计           │
│     │                                                     │
│  ┌──┴───────────────┬──────────────┬──────────────┐       │
│  │ devices/forwards │ vault        │ assets       │       │
│  │ hub (WS 连接表)   │ 信封加密      │ 路径解析      │       │
│  └──────────────────┴──────┬───────┴──────────────┘       │
│                            │                              │
│  checks/inspection: 到期扫描、执行池、结果聚合、告警        │
│  relay: x/crypto/ssh、database/sql、net/http              │
│         └── 全部通过 agent 的 yamux 流转发                 │
│                                                           │
│  store: SQLite (modernc) + sqlc + 内嵌迁移                 │
│  web: //go:embed web/dist                                 │
└───────────────────────────┬───────────────────────────────┘
                            │ /agent/ws
                            │ 设备密钥认证
                            │ 控制帧(JSON) + yamux 多路复用流
                            ▼
┌─────────────────────────────────────────────────────────┐
│ cmd/agent（内网机器 / 跳板机）                             │
│  client.go      重连、心跳、配置哈希幂等                   │
│  supervisor.go  frpc 子进程、环形日志、退避重启            │
│  gateway.go     dial_check / tcp stream / 并发与 CIDR 校验 │
│  persist.go     原子写 frpc.toml                          │
└───────────┬───────────────────────────┬─────────────────┘
            │ frpc                      │ 直连
            ▼                           ▼
      frps（公网节点）            局域网资产 192.168.x.x
```

### 1.1 关键决策：Agent 只做字节管道

| 方案 | 凭据位置 | 优点 | 缺点 |
|---|---|---|---|
| A. 把凭据下发给 Agent | Agent | 实现直白 | Agent 被控即泄露所有目标凭据；审计分散 |
| **B. 控制面持凭据，Agent 转发 TCP（选定）** | 控制面 | 凭据不出控制面；统一审计；SSH/DB 客户端复用成熟库 | 控制面承担加解密与协议栈 |

选 B。Agent 的 `gateway` 只接受 `host:port`，并强制 CIDR/端口白名单：

- 默认允许 RFC1918 + 链路本地；
- 默认拒绝 `169.254.169.254`（云元数据）与回环；
- 回环只放行显式配置的端口（如跳板机自身 `127.0.0.1:22`）；
- 每设备最大并发流 16，单流空闲 10 分钟回收。

### 1.2 两条访问路径

**路径一：原生 SSH（v1，零流量经过控制面）**

```
管理员 ssh -J jump@node.example.com:18022 root@192.168.1.10
        └──→ frps ──→ frpc ──→ 跳板机 ──→ 目标机
```

控制面只负责：资产台账、跳板选择、凭据保管、命令/`~/.ssh/config` 生成、主机指纹核对。
适合：SSH 运维、`ssh -L` 跑 RDP/VNC/数据库客户端、`ssh -D` 做临时 SOCKS。

**路径二：Web 终端 / 数据源代理（v2，手机可用）**

```
浏览器 WS ⇄ 控制面(x/crypto/ssh) ⇄ yamux 流 ⇄ Agent ⇄ TCP ⇄ 目标
```

凭据始终在控制面内存里；Agent 看到的是加密后的 SSH 字节流。
同一套流转发同时服务：Web 终端、局域网数据库、宝塔面板 HTTP 请求。

### 1.3 与 frp 的关系

- frp 只负责"公网节点 ↔ 内网 Agent"的端口映射，保持现有 `frptmpl` 单点知识。
- 跳板访问**不新增 frps 插件**，全部走 `/agent/ws` 的 yamux 流。
- 原生 SSH 需要跳板机的 22 端口暴露时，复用一条普通 TCP 转发规则，不另造机制。
- `v1 不启用 frps HTTP 插件；插件路径预留` 的约定不变。
- 转发面的节点、条目、端口池、准入与流量管理对齐 gostc-open 的管理内容（不借样式），见第 15 节。

---

## 2. 数据模型增量

在现有 `nodes / devices / forwards / audit_logs` 基础上，本节新增凭据/资产/会话与 AI 基础共 6 张表；AI 扩展表见第 5 节，轮询/轮检见第 6 节，性能见 6.6，节点/转发的增补字段与流量表见 15.8。

```sql
-- ---------- 凭据中心 ----------
CREATE TABLE credentials (
  id           TEXT PRIMARY KEY,
  name         TEXT NOT NULL,
  kind         TEXT NOT NULL,              -- ssh_password | ssh_key | baota_api
                                           -- db_password | api_token | generic
  username     TEXT,
  secret_ct    BLOB NOT NULL,              -- nonce(24) || ChaCha20-Poly1305 密文
  key_id       TEXT NOT NULL,              -- 主密钥版本，支持轮换
  public_meta  TEXT NOT NULL DEFAULT '{}', -- 指纹、面板地址、端口等非敏感信息
  created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  last_used_at DATETIME
);
CREATE INDEX ix_credentials_kind ON credentials(kind);

-- ---------- 资产 / 跳板 ----------
CREATE TABLE assets (
  id            TEXT PRIMARY KEY,
  name          TEXT NOT NULL,
  address       TEXT NOT NULL,             -- IP 或主机名
  port          INTEGER NOT NULL DEFAULT 22,
  protocol      TEXT NOT NULL DEFAULT 'ssh', -- ssh|rdp|vnc|http|mysql|postgres
  via_device_id TEXT REFERENCES devices(id), -- 跳板 Agent；空 = 控制面直连
  via_asset_id  TEXT REFERENCES assets(id),  -- 二级跳板；v1 链长 ≤ 3
  credential_id TEXT REFERENCES credentials(id),
  host_key      TEXT,                      -- SSH 主机公钥指纹（首次人工核对后固定）
  tags          TEXT NOT NULL DEFAULT '',
  enabled       INTEGER NOT NULL DEFAULT 1,
  notes         TEXT,
  created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX ix_assets_device ON assets(via_device_id);

CREATE TABLE asset_checks (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  asset_id   TEXT NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
  device_id  TEXT NOT NULL,
  ok         INTEGER NOT NULL,
  latency_ms INTEGER,
  error      TEXT,
  checked_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- ---------- 会话（终端 / 数据源 / HTTP） ----------
CREATE TABLE sessions (
  id         TEXT PRIMARY KEY,
  asset_id   TEXT REFERENCES assets(id),
  device_id  TEXT,
  actor      TEXT NOT NULL,
  kind       TEXT NOT NULL,                -- terminal | db | http
  started_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  ended_at   DATETIME,
  bytes_in   INTEGER NOT NULL DEFAULT 0,
  bytes_out  INTEGER NOT NULL DEFAULT 0,
  recording  TEXT,                         -- 相对路径，asciinema v2
  result     TEXT,                         -- ok | error | timeout | killed
  error      TEXT
);

-- ---------- AI ----------
CREATE TABLE ai_providers (
  id            TEXT PRIMARY KEY,
  name          TEXT NOT NULL,
  base_url      TEXT NOT NULL,             -- OpenAI 兼容，如 https://api.deepseek.com
  model         TEXT NOT NULL,
  api_key_ct    BLOB,
  key_id        TEXT,
  params        TEXT NOT NULL DEFAULT '{}', -- temperature、max_tokens 等
  is_default    INTEGER NOT NULL DEFAULT 0,
  enabled       INTEGER NOT NULL DEFAULT 1,
  last_check_at DATETIME,
  last_check_ok INTEGER,
  last_error    TEXT,
  created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE data_sources (
  id            TEXT PRIMARY KEY,
  name          TEXT NOT NULL,
  kind          TEXT NOT NULL,             -- mysql | postgres | sqlite
  address       TEXT NOT NULL,
  port          INTEGER NOT NULL,
  database      TEXT,
  username      TEXT,
  secret_ct     BLOB,
  key_id        TEXT,
  via_device_id TEXT REFERENCES devices(id), -- 局域网库走跳板
  read_only     INTEGER NOT NULL DEFAULT 1,
  options       TEXT NOT NULL DEFAULT '{}', -- sslmode、charset、timeout
  enabled       INTEGER NOT NULL DEFAULT 1,
  last_check_at DATETIME,
  last_check_ok INTEGER,
  last_error    TEXT,
  created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

`audit_logs` 增补三列：`ip`、`user_agent`、`result`。所有敏感操作（reveal、连接、数据源查询、密钥轮换）必须落审计。

注意：
- SQLite 需要 `PRAGMA foreign_keys=ON` 才会执行外键。
- `forwards` 的 `ux_forwards_port` 唯一索引保留，端口分配逻辑不变。
- 资产与转发是两套概念：转发是"把端口暴露到公网"，资产是"我要连谁、经过谁"。不要合并。

---

## 3. 凭据中心（Vault）

### 3.1 加密方案

- 主密钥：32 字节随机，来源优先级 `AIBOT_MASTER_KEY`（base64）> `/etc/server-aibot/master.key`（0600）。
- 算法：ChaCha20-Poly1305（`golang.org/x/crypto/chacha20poly1305`）。
- 每条记录：`nonce(24B) || ciphertext`；AAD = `credentialID || 0x00 || kind`，防止密文被跨记录搬运。
- `key_id` = SHA-256(主密钥) 前 8 字节的 hex。解密按 `key_id` 选键。
- 主密钥**不与数据库同备份**。丢失即不可恢复，安装脚本和 UI 都要明确提示。
- 轮换：`POST /api/v1/admin/rotate-master-key`，新键加密全表、旧键保留在 `AIBOT_MASTER_KEY_OLD` 直到确认完成；分批事务，可重入。
- 明文只在内存中存在，用完尽量覆写（Go 只能尽力而为，需在文档中说明边界）。

### 3.2 API 行为（写多读少）

| 操作 | 规则 |
|---|---|
| 列表 | 只返回 `id/name/kind/username/publicMeta/lastUsedAt`，**永不返回密文或明文** |
| 新建/替换 | 接收明文，立即加密；响应返回指纹，不回显 |
| 修改元数据 | 不触碰 `secret_ct` |
| `POST /credentials/{id}/reveal` | 二次认证（密码 + 可选 TOTP）→ 返回一次明文 → 审计 → 限流 5 次/分钟 |
| SSH 私钥 | 默认**不允许 reveal**，只给指纹与"替换"；需要导出时走单独开关 + 审计 |
| 删除 | 软删或检查引用；被资产/转发引用时返回 409 |

### 3.3 各类型凭据的字段约定

| kind | 必填 | public_meta | 说明 |
|---|---|---|---|
| `ssh_password` | username + password | `port` | 跳板/目标机口令 |
| `ssh_key` | username + privateKey(+passphrase) | `fingerprint`, `keyType` | 上传时算指纹，拒绝 <2048 位 RSA / 弱算法 |
| `baota_api` | apiKey | `panelUrl`, `ipWhitelist` | 优先 API Key，不用面板账号密码；控制面出口 IP 需加白 |
| `db_password` | username + password | `dbKind`, `defaultDatabase` | 只读账号 |
| `api_token` | token | `scope`, `expiresAt` | 第三方 API |
| `generic` | secret | 自定义 | 兜底 |

宝塔补充：面板通常在局域网，API 调用走 Agent 流转发；若面板公网可达，才允许控制面直连，并强制 IP 白名单。现有的 `BT_HELPER.md` 只读取证思路保留，迁移为 Go 侧的只读检查任务。

---

## 4. 资产与跳板

### 4.1 路径解析

```
resolve(asset):
  hops = []
  cur = asset
  while cur.via_asset_id != nil:  # 最多 3 跳
    hops.prepend(cur)
    cur = load(cur.via_asset_id)
  device = cur.via_device_id or null
  return { device, hops, entry: device ? find_ssh_forward(device) : direct }
```

- `device` 为空 → 控制面直连（公网资产）。
- `device` 非空 → 必须先确认该设备在线，并存在一条指向跳板机 SSH 的启用转发（或直接用 Agent 流，不需要转发）。
- 跳板机指纹首次连接时展示，人工核对后写入 `assets.host_key`；不一致直接阻断并告警。

### 4.2 三种连接产物

| 场景 | v1 产物 | 说明 |
|---|---|---|
| SSH 原生 | `ssh -J ...` / `~/.ssh/config` 片段 | 复制即用；可选短期证书 |
| Web 终端 | `POST /assets/{id}/connect` → `{sessionId, wsUrl}` | 手机/无客户端场景 |
| RDP / VNC / DB | `ssh -L 3389:target:3389 -J jump` 命令 | v1 不内嵌 RDP 客户端 |
| 浏览器 RDP | v3 可选 Guacamole | 单独评估，不进 v1 |

### 4.3 Web 终端数据通路

1. 用户点"连接"→ 控制面校验资产、设备在线、步进认证。
2. 控制面通过 Agent 控制 WS 打开一条 yamux 流，写入首行 JSON：
   ```json
   {"host":"192.168.1.10","port":22,"purpose":"ssh","ttl":300}
   ```
3. Agent 校验 CIDR/端口/并发 → `net.DialTimeout` → 回 `{"ok":true}` 或错误。
4. 控制面用 `x/crypto/ssh` 握手（凭据来自 Vault，主机指纹比对 `assets.host_key`）。
5. 多跳：`sshClient.Dial("tcp", "next:22")` 再握手，链长 ≤ 3。
6. 浏览器 WS ↔ SSH PTY 双向桥接；支持 resize、空闲 30 分钟断开、主动终止。
7. 录制 asciinema v2；`sessions` 表记录起止、字节数、结果。

WS 复用实现：`websocket.NetConn` 包装 Agent 控制连接 → `hashicorp/yamux`。控制帧仍走原来的 JSON Envelope，数据流走 yamux，互不干扰。

### 4.4 局域网数据库 / 宝塔面板

数据库驱动通过自定义 dialer 接到 yamux 流上：

- MySQL：注册私有 network（如 `aibot`），DSN 用 `user:pass@aibot(host:port)/db`。
- Postgres：`pgx` 的 `DialFunc` 指向同一 dialer。
- HTTP（宝塔）：`http.Transport.DialContext` 指向 dialer，超时 10s，响应体上限 4MB。

这样"局域网数据源"和"公网数据源"在业务代码里没有分支，只差一个 `via_device_id`。

### 4.5 可选增强（不进 v1 关键路径）

- Agent 侧 ARP/ICMP 扫描，生成"待登记资产"建议（需显式开启，且只报私有网段）。
- 拓扑图：控制面 → 节点 → 设备 → 资产，用 SVG 画，纯展示。
- 资产分组/标签过滤。

---

## 5. AI 运维与模型接入

目标：AI 不只是问答，而是能在**人工审批**下执行服务器管理与配置变更；同时支持多模型、提示词模板、256K/1M 长上下文。

### 5.1 多模型接入

- `ai_providers`：provider 级配置（base_url、API key、组织、代理、健康检查）。
- `ai_models`：一个 provider 下多个模型，记录 `context_window`（如 256000 / 1000000）、`max_output`、`supports_tools`、`supports_vision`、`supports_cache`、输入/输出单价。
- 支持 OpenAI 兼容、Anthropic、Gemini、本地 Ollama/vLLM。本地模型用于敏感数据不出网。
- 路由策略：默认模型、按任务类型（巡检摘要 / 故障定位 / 变更方案 / 日志分析）路由、失败回退链、超时与重试。
- 健康检查：`POST /ai/models/{id}/test`，记录延迟、可用性、最近错误。
- API Key 走 Vault 加密；列表只显示末四位；凭据不进入提示词。

```sql
CREATE TABLE ai_providers (
  id            TEXT PRIMARY KEY,
  name          TEXT NOT NULL,
  kind          TEXT NOT NULL,             -- openai_compatible|anthropic|gemini|ollama|vllm
  base_url      TEXT NOT NULL,
  api_key_ct    BLOB,
  key_id        TEXT,
  org           TEXT,
  proxy         TEXT,
  enabled       INTEGER NOT NULL DEFAULT 1,
  last_check_at DATETIME,
  last_check_ok INTEGER,
  last_error    TEXT,
  created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE ai_models (
  id              TEXT PRIMARY KEY,
  provider_id     TEXT NOT NULL REFERENCES ai_providers(id),
  model_id        TEXT NOT NULL,
  display_name    TEXT NOT NULL,
  context_window  INTEGER NOT NULL,        -- 256000 / 1000000
  max_output      INTEGER,
  supports_tools  INTEGER NOT NULL DEFAULT 1,
  supports_vision INTEGER NOT NULL DEFAULT 0,
  supports_cache  INTEGER NOT NULL DEFAULT 0,
  input_price     REAL,                    -- 每 1M tokens
  output_price    REAL,
  enabled         INTEGER NOT NULL DEFAULT 1,
  is_default      INTEGER NOT NULL DEFAULT 0,
  created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

### 5.2 提示词模板

- 模板由 `name / scope / system_prompt / variables / version` 组成；变量用 `${device}`、`${time_range}`、`${metrics}`、`${findings}`、`${logs}`。
- 内置：巡检摘要、故障定位、变更方案、日志分析；支持自定义与版本化。
- 模板渲染在服务端完成，变量值来自受控查询，不把原始用户输入拼进系统提示。
- 提示词变更审计；可按设备/资产覆盖。

```sql
CREATE TABLE prompts (
  id            TEXT PRIMARY KEY,
  name          TEXT NOT NULL,
  scope         TEXT NOT NULL,             -- inspection|incident|change|log|custom
  system_prompt TEXT NOT NULL,
  variables     TEXT NOT NULL DEFAULT '[]',
  version       INTEGER NOT NULL DEFAULT 1,
  enabled       INTEGER NOT NULL DEFAULT 1,
  created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

### 5.3 AI 服务器管理（工具 + 审批）

AI 只能调用**有 JSON Schema 的工具**，不能执行任意 shell。

只读工具（默认允许）：

- `list_devices`、`get_device_status`、`get_metrics`、`get_check_results`、`get_inspection_findings`；
- `read_logs`（tail N，脱敏）、`run_readonly_sql`、`list_forwards`、`get_asset_route`。

写工具（只生成提案，不直接执行）：

- `propose_service_restart`、`propose_config_change`、`propose_forward_create/update/delete`、`propose_cleanup`、`propose_deploy`。
- 提案包含：目标、工具/命令、diff、影响面、风险等级、回滚方案、验证步骤。
- 执行必须由人确认：预览 diff → 二次认证（高风险要求 TOTP）→ 执行 → 健康检查 → 失败回滚。
- v1 **不开放 AI 自动执行写操作**；后续可按设备/工具配置低风险自动执行，但必须有速率限制和审计。

```sql
CREATE TABLE ai_policies (
  id         TEXT PRIMARY KEY,
  scope      TEXT NOT NULL,               -- global|device|asset
  target_id  TEXT,
  tool       TEXT NOT NULL,
  mode       TEXT NOT NULL,               -- allow|approval|deny
  min_risk   TEXT,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

风险分级：`read` / `low` / `medium` / `high`。默认：read 允许，low/medium 审批，high 二次认证 + 审批，任意 shell 拒绝。

### 5.4 长上下文（256K / 1M）

- 模型元数据记录 `context_window`；UI 常驻上下文占用条。
- 预算：system + prompt + pinned facts + summary + retrieved snippets + recent turns + tool results + output reserve。
- 到 80% 自动压缩：保留 pinned、最近 N 轮、工具结果摘要；旧轮次生成滚动摘要。
- 工具输出先截断/摘要再入上下文；日志用 map-reduce，不整段塞入。
- 1M 模型用于大日志、多设备故障、跨报告分析；默认仍用 256K，避免成本和延迟。
- 检索优先：巡检报告、审计、指标、文档走 SQL/全文检索，必要时再加向量检索；长上下文不替代检索。
- Token 计数：按 provider 实现；无精确计数时用保守估算。
- Prompt caching：稳定前缀（system + pinned）放前面，兼容 provider 的缓存计费。
- 成本：记录 `tokens_in/out/cached` 与单价，按会话/天/模型汇总；切到 1M 时弹成本提示。

### 5.5 对话、流式与用量

```sql
CREATE TABLE ai_conversations (
  id             TEXT PRIMARY KEY,
  title          TEXT,
  model_id       TEXT REFERENCES ai_models(id),
  prompt_id      TEXT REFERENCES prompts(id),
  context_used   INTEGER NOT NULL DEFAULT 0,
  context_window INTEGER NOT NULL,
  summary        TEXT,
  pinned         TEXT,                    -- JSON
  created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE ai_messages (
  id              TEXT PRIMARY KEY,
  conversation_id TEXT NOT NULL REFERENCES ai_conversations(id) ON DELETE CASCADE,
  role            TEXT NOT NULL,          -- system|user|assistant|tool
  content         TEXT,
  tool_calls      TEXT,                   -- JSON
  tokens_in       INTEGER,
  tokens_out      INTEGER,
  at              DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE ai_tool_calls (
  id              TEXT PRIMARY KEY,
  conversation_id TEXT REFERENCES ai_conversations(id),
  tool            TEXT NOT NULL,
  target          TEXT,
  args            TEXT,
  result          TEXT,
  risk            TEXT NOT NULL,          -- read|low|medium|high
  status          TEXT NOT NULL,          -- proposed|approved|rejected|executed|failed
  approved_by     TEXT,
  at              DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE ai_usage (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  provider_id     TEXT,
  model_id        TEXT,
  conversation_id TEXT,
  tokens_in       INTEGER,
  tokens_out      INTEGER,
  cached_tokens   INTEGER,
  cost            REAL,
  at              DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

- 流式：`POST /ai/conversations/{id}/messages` 返回 SSE，事件 `token / tool_call / tool_result / approval_required / done / error`。
- 审批：`POST /ai/tool-calls/{id}/approve|reject`；执行结果回填会话。
- 取消：`POST /ai/conversations/{id}/cancel`。

### 5.6 API

```
GET/POST/PATCH/DELETE /api/v1/ai/providers
GET/POST/PATCH/DELETE /api/v1/ai/models
POST   /api/v1/ai/models/{id}/test
GET/POST/PATCH/DELETE /api/v1/ai/prompts
GET/POST /api/v1/ai/conversations
GET    /api/v1/ai/conversations/{id}
POST   /api/v1/ai/conversations/{id}/messages   # SSE
POST   /api/v1/ai/conversations/{id}/cancel
GET    /api/v1/ai/tool-calls
POST   /api/v1/ai/tool-calls/{id}/approve
POST   /api/v1/ai/tool-calls/{id}/reject
GET    /api/v1/ai/usage?from=&to=&groupBy=
GET/POST/PATCH/DELETE /api/v1/ai/policies

# 数据源
GET/POST/PATCH/DELETE /api/v1/datasources
POST   /api/v1/datasources/{id}/test
POST   /api/v1/datasources/{id}/query
GET    /api/v1/datasources/{id}/schema
```

### 5.7 数据源

- 支持 MySQL / PostgreSQL / SQLite；局域网库绑定 `via_device_id`。
- 只读账号是前提；连接后立即执行会话级只读：
  - MySQL：`SET SESSION TRANSACTION READ ONLY`
  - PostgreSQL：`SET default_transaction_read_only = on`
- 应用层再拦一层：单语句、仅允许 `SELECT/SHOW/EXPLAIN/DESCRIBE/WITH`，拒绝 `INSERT/UPDATE/DELETE/DROP/ALTER/CREATE/GRANT/TRUNCATE/CALL/LOAD_FILE/INTO OUTFILE/COPY`，拒绝多语句。
- 超时 5s、行数上限 500、结果集上限 2MB。
- 结果脱敏：列名命中 `password|passwd|token|secret|phone|id_card|email` 默认打码，可在数据源级别配置。
- 所有 AI 发起的查询审计，actor 记为 `ai:<conversationId>`。

### 5.8 UI

- **对话页**：模型选择、上下文占用、提示词模板、工具调用卡片、审批卡片、diff、风险等级、执行结果、审计链接。
- **模型页**：provider/model 两级、上下文窗口、能力标签、测试、默认/回退、路由。
- **提示词页**：模板列表、变量说明、版本、预览。
- **数据源页**：只读徽标、跳板路径、测试、schema。
- **用量页**：token、成本、按模型/会话/天。
- 移动端：对话为主，审批卡片可展开，上下文条常驻。

### 5.9 安全

- 提示注入：日志/DB/网页内容标记为不可信数据；模型输出不能直接变成命令；工具参数服务端校验。
- 凭据不进入提示词；日志与查询结果先脱敏。
- 写操作默认拒绝；提案与执行分离；高风险二次认证；全量审计（actor = `ai:<conversation>`，approver = 人类）。
- 本地模型选项；provider 数据留存/训练策略记录在 provider 元数据。
- 速率限制：每会话工具调用数、每设备写提案数、每日 token 预算。

---

## 6. 轮询与轮检

### 6.1 三类数据，一个底座

| | 服务轮询 | 定期轮检 | 性能采集 |
|---|---|---|---|
| 目标 | 现在是否可用 | 有没有隐患 | 趋势与容量 |
| 频率 | 30s–5min | 每天 / 每周 / 手动 | 10–60s，默认 15s |
| 粒度 | 单个检查项 | 一组 profile | 主机/磁盘/网卡/进程 |
| 输出 | up/down + 延迟 | 报告 + 发现项 + 证据 | 时序指标 + 阈值告警 |
| 保留 | 原始 7 天 + 5 分钟 rollup 90 天 | 报告 180 天，可配 | 原始 7 天 + 1min 30 天 + 5min 180 天 |

三者共用：Agent 通道、结果落库、告警、审计。轮询/轮检走调度器，性能采集走 Agent 本地定时推送。

### 6.2 检查类型

| kind | 执行位置 | 参数 | 说明 |
|---|---|---|---|
| `tcp` | agent / control | host, port | 连接成功 + 延迟 |
| `http` | agent / control | url, method, expectStatus, expectBody | 状态码/关键字/TLS 到期 |
| `process` | agent | unit 或 name/port | systemd 单元、进程名、监听端口 |
| `system` | agent | metrics[] | CPU/内存/磁盘/负载/uptime/网卡 |
| `ssh` | control over agent 流 | assetId, commands[] | 仅白名单只读命令 |
| `baota` | control over agent 流 | credentialId | 面板 API：站点/SSL/磁盘/安全日志 |
| `db` | control over agent 流 | dataSourceId | SELECT 1、连接数、复制延迟 |
| `frp` | control | forwardId | applied_rev + 节点侧观测 |
| `cert` | agent / control | host, port | 证书剩余天数 |
| `metric` | control（基于 Agent 上报） | metric, labels, warn, crit, duration | 性能阈值，复用状态机与告警 |

Agent 侧只接受结构化 kind + JSON 参数，**不接受 shell 字符串**。

### 6.3 调度器

- 控制面内置单调度器，1s tick 扫描 `checks.next_run_at`；不引 cron 依赖。
- 巡检用 5 段 cron 或 `daily@HH:MM`，解析后同样落 `next_run_at`。
- 每次执行用 `lease_owner/lease_until` 抢占，防止控制面重启或多实例重复跑。
- 单检查超时默认 5s、重试 1 次、`±10%` 抖动；上一轮未结束则跳过本轮，不排队堆积。
- 并发池：全局 16、单设备 4、单检查 1。
- 状态机：`unknown → up ⇄ down`；连续失败达到 `failure_threshold`（默认 3）才判 down；恢复需 1 次成功且稳定 2 分钟，防抖。
- Agent 离线时检查记为 `stale`，**不判 down**，避免误告警。
- 维护窗口/静默：照常记录结果，只抑制告警。
- 控制面停机后不补跑历史，恢复时从当前时间重排。

### 6.4 数据表

```sql
CREATE TABLE checks (
  id                  TEXT PRIMARY KEY,
  name                TEXT NOT NULL,
  kind                TEXT NOT NULL,       -- tcp|http|process|system|ssh|baota|db|frp|cert
  target_asset_id     TEXT REFERENCES assets(id),
  device_id           TEXT REFERENCES devices(id), -- 执行 vantage；空 = 控制面
  params              TEXT NOT NULL DEFAULT '{}',
  interval_seconds    INTEGER NOT NULL DEFAULT 60,
  timeout_ms          INTEGER NOT NULL DEFAULT 5000,
  retries             INTEGER NOT NULL DEFAULT 1,
  failure_threshold   INTEGER NOT NULL DEFAULT 3,
  severity            TEXT NOT NULL DEFAULT 'warning',
  enabled             INTEGER NOT NULL DEFAULT 1,
  state               TEXT NOT NULL DEFAULT 'unknown', -- unknown|up|down|stale|paused
  consecutive_failures INTEGER NOT NULL DEFAULT 0,
  last_run_at         DATETIME,
  last_ok_at          DATETIME,
  last_error          TEXT,
  next_run_at         DATETIME,
  lease_owner         TEXT,
  lease_until         DATETIME,
  created_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX ix_checks_due ON checks(enabled, next_run_at);

CREATE TABLE check_results (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  check_id   TEXT NOT NULL REFERENCES checks(id) ON DELETE CASCADE,
  at         INTEGER NOT NULL,             -- unix ms
  ok         INTEGER NOT NULL,
  latency_ms INTEGER,
  value      REAL,                         -- 数值指标：磁盘%、连接数等
  detail     TEXT,                         -- JSON，已脱敏
  error      TEXT
);
CREATE INDEX ix_results_check_at ON check_results(check_id, at);

-- 5 分钟聚合，供长周期图表；原始数据 7 天后删除
CREATE TABLE check_rollups (
  check_id       TEXT NOT NULL,
  bucket_at      INTEGER NOT NULL,
  ok_count       INTEGER NOT NULL,
  fail_count     INTEGER NOT NULL,
  avg_latency_ms INTEGER,
  max_value      REAL,
  PRIMARY KEY (check_id, bucket_at)
);

CREATE TABLE inspections (
  id           TEXT PRIMARY KEY,
  name         TEXT NOT NULL,
  schedule     TEXT NOT NULL,              -- cron 或 daily@03:30
  target_scope TEXT NOT NULL,              -- JSON: deviceIds / assetIds / tags
  profile      TEXT NOT NULL,              -- linux-basic|baota|db
  enabled      INTEGER NOT NULL DEFAULT 1,
  retain_days  INTEGER NOT NULL DEFAULT 180,
  next_run_at  DATETIME,
  created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE inspection_runs (
  id            TEXT PRIMARY KEY,
  inspection_id TEXT NOT NULL REFERENCES inspections(id),
  started_at    DATETIME,
  finished_at   DATETIME,
  status        TEXT NOT NULL,             -- running|ok|warning|critical|failed
  score         INTEGER,
  summary       TEXT,
  trigger       TEXT NOT NULL              -- schedule|manual|api
);

CREATE TABLE inspection_findings (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id      TEXT NOT NULL REFERENCES inspection_runs(id) ON DELETE CASCADE,
  device_id   TEXT,
  asset_id    TEXT,
  category    TEXT NOT NULL,               -- system|disk|service|network|security|tls|backup|log
  severity    TEXT NOT NULL,               -- info|warning|critical
  title       TEXT NOT NULL,
  evidence    TEXT,                        -- 脱敏后的 JSON/文本
  remediation TEXT,
  fingerprint TEXT                         -- 跨轮次去重/趋势
);
```

### 6.5 巡检 profile

`linux-basic`：

- 负载、uptime、CPU、内存、swap；
- 各挂载点使用率、inode；
- CPU/内存 Top 进程；
- systemd failed units；
- 监听端口与期望对照；
- 时间同步偏差；
- dmesg 错误、OOM killer；
- journal 错误计数；
- SSH 基线（PermitRootLogin、PasswordAuthentication）；
- 防火墙状态；
- 证书到期；
- 备份新鲜度（配置路径）。

`baota`：面板版本、站点、SSL 到期、磁盘、安全日志、计划任务失败。
`db`：连通性、版本、连接数、复制延迟、慢查询计数（只读）。

发现项带 `fingerprint`，用于跨轮次去重与趋势；评分 = 100 - 加权扣分，报告展示历史趋势。

### 6.6 服务器性能采集

**采集方式**：Agent 本地定时采样（默认 15s），通过控制 WS 推送 `MetricsSample`；控制面只做校验、入库、聚合。控制面短暂不可用不影响采样，Agent 用环形缓冲补传。

**指标**：

| 维度 | 指标 |
|---|---|
| CPU | 总使用率、每核使用率（可选）、user/system/iowait/steal |
| 负载 | load1 / load5 / load15 |
| 内存 | total/used/available/cache/buffers、swap |
| 磁盘 | 每个挂载点 total/used/avail、inode、读写字节、IO util |
| 网络 | 每个网卡 rx/tx 字节、包、错误、丢包、带宽 |
| 进程 | Top N（默认 10）PID/名称/CPU%/MEM%/RSS |
| 系统 | uptime、进程数、fd 数、TCP 连接数、启动时间 |
| 可选 | 温度、GPU、SMART、NTP 偏差 |

**实现**：优先 `gopsutil/v4`（Linux/Windows 都能跑），Linux 下也可直接读 `/proc`；CPU、网卡、磁盘 IO 用两次采样的差值算速率。进程只存名称/PID/CPU/内存，**不存完整命令行**，避免把密钥写进时序库。

**开销**：采集器 CPU < 1%，内存 < 20MB；Top N 限制 10；单次采集超时 2s；失败不阻塞下一轮。

**写入量**：单机 15s 采样约 5,760 条 `host_samples`/天，Top 10 进程约 57,600 条 `proc_samples`/天。50 台以上建议 30–60s；控制面批量事务写入（100 条或 1s 刷盘），SQLite 用 WAL + `synchronous=NORMAL`。

**补传与时钟**：Agent 环形缓冲最近 240 个样本（15s × 1h）；断线重连后按时间戳补传，控制面按 `(device_id, at)` 去重。Agent 上报时间与控制面接收时间偏差 > 2 分钟时记录 skew 指标并告警。

**存储**：

```sql
CREATE TABLE host_samples (
  device_id     TEXT NOT NULL,
  at            INTEGER NOT NULL,          -- unix ms
  cpu_pct       REAL,
  cpu_user      REAL, cpu_system REAL, cpu_iowait REAL, cpu_steal REAL,
  load1         REAL, load5 REAL, load15 REAL,
  mem_total     INTEGER, mem_used INTEGER, mem_available INTEGER,
  mem_cache     INTEGER, mem_buffers INTEGER,
  swap_total    INTEGER, swap_used INTEGER,
  uptime_sec    INTEGER,
  proc_count    INTEGER,
  fd_count      INTEGER,
  tcp_conns     INTEGER,
  boot_time     INTEGER,
  PRIMARY KEY (device_id, at)
);
CREATE INDEX ix_host_samples_at ON host_samples(at);

CREATE TABLE disk_samples (
  device_id   TEXT NOT NULL,
  at          INTEGER NOT NULL,
  mount       TEXT NOT NULL,
  fs          TEXT,
  total       INTEGER, used INTEGER, avail INTEGER,
  inode_total INTEGER, inode_used INTEGER,
  read_bytes  INTEGER, write_bytes INTEGER, io_util REAL,
  PRIMARY KEY (device_id, at, mount)
);

CREATE TABLE net_samples (
  device_id TEXT NOT NULL,
  at        INTEGER NOT NULL,
  iface     TEXT NOT NULL,
  rx_bytes  INTEGER, tx_bytes INTEGER,
  rx_packets INTEGER, tx_packets INTEGER,
  rx_errors INTEGER, tx_errors INTEGER,
  rx_drops  INTEGER, tx_drops INTEGER,
  PRIMARY KEY (device_id, at, iface)
);

-- 只存 Top N，避免进程维度把库撑爆
CREATE TABLE proc_samples (
  device_id TEXT NOT NULL,
  at        INTEGER NOT NULL,
  pid       INTEGER NOT NULL,
  name      TEXT,
  cpu_pct   REAL, mem_pct REAL, rss INTEGER,
  PRIMARY KEY (device_id, at, pid)
);

-- 降采样：step_seconds = 60 或 300
CREATE TABLE metric_rollups (
  device_id   TEXT NOT NULL,
  metric      TEXT NOT NULL,               -- cpu_pct|mem_used_pct|disk_used_pct|net_rx_bps|...
  labels      TEXT NOT NULL DEFAULT '',    -- mount / iface
  step_seconds INTEGER NOT NULL,
  bucket_at   INTEGER NOT NULL,
  min         REAL, avg REAL, max REAL,
  samples     INTEGER NOT NULL,
  PRIMARY KEY (device_id, metric, labels, step_seconds, bucket_at)
);

CREATE TABLE metric_thresholds (
  id               TEXT PRIMARY KEY,
  device_id        TEXT REFERENCES devices(id), -- 空 = 全局默认
  metric           TEXT NOT NULL,
  labels           TEXT,
  warn             REAL,
  crit             REAL,
  duration_seconds INTEGER NOT NULL DEFAULT 300,
  enabled          INTEGER NOT NULL DEFAULT 1
);
```

**阈值**：

- 每个指标可配 warn/crit + 持续时间（默认 5 分钟），支持全局默认 + 单设备覆盖。
- 阈值复用第 6 章的检查状态机与告警去重，kind=`metric`，不再写一套告警。
- 示例：`cpu_pct > 85% 持续 5min`、`disk_used_pct{mount="/"} > 90%`、`mem_available < 10%`。

**UI**：

- **性能页**：设备选择、时间范围（1h/6h/24h/7d/30d）、CPU/负载/内存/磁盘/网络图表、挂载点表、网卡表、Top 进程、阈值编辑。
- **概览**：最忙的 N 台设备、磁盘告警、近 24h 峰值。
- **资产详情**：嵌一张紧凑性能卡。
- 手机：纵向堆叠卡片，图表高度 160–200px，支持横屏看细节。

**API**：

```
GET /api/v1/metrics/devices/{id}/latest
GET /api/v1/metrics/devices/{id}?from=&to=&step=&metric=&labels=
GET /api/v1/metrics/devices/{id}/top?at=
GET /api/v1/metrics/overview
GET/POST/PATCH/DELETE /api/v1/metrics/thresholds
WS  /api/v1/metrics/live          # 可选，实时 tail
```

**降采样**：查询 `step` 只能是 15s/1m/5m/1h；超过 6 小时自动用 1m，超过 2 天用 5m，超过 30 天在查询时把 5m 再聚合为 1h。原始数据保留 7 天，1 分钟聚合 30 天，5 分钟聚合 180 天。

**安全**：指标接口要求登录；进程信息不含命令行；导出走审计；Agent 采集器不接受任意命令，GPU/温度等可选采集使用固定路径。

### 6.7 执行与证据

- 控制面检查：直连或经 Agent 流。
- Agent 检查：`RunCheck{checkId, kind, params}` → 结构化结果；两端都设超时。
- SSH 检查：控制面开流 → SSH（低权限账号）→ 执行固定命令序列 → 截断 stdout/stderr。
- 证据：原始输出截断 64KB + 归一化 JSON；落盘前脱敏（password/token/key/Authorization）。
- 记录 vantage（控制面或哪台 Agent）；没有外部 vantage 时显示"未验证"，沿用现有产品语义。

### 6.8 告警

- 状态翻转生成告警；按 `(checkId, state)` 去重；恢复发 recovery。
- 渠道 v1：站内 + 飞书（群 Webhook / 应用 Bot）+ 通用 Webhook；邮件/企微/钉钉按需追加，详见第 7 节。
- 静默（带到期）、维护窗口、确认（ack）。
- 持续 down 超过 N 分钟可升级为 critical（可选）。

### 6.9 UI

- **监控**：检查项列表（状态灯、延迟 sparkline、最后错误、暂停/立即执行）；详情时间线、结果表。
- **巡检**：计划列表、下次执行、最近评分；报告按严重度分组、证据查看、与上一轮 diff、导出 Markdown/HTML。
- **告警**：列表、确认、静默。
- **概览**：down 数、critical 发现、下次巡检。
- 手机：列表 + 详情、立即执行、发现项折叠。

### 6.10 API

```
GET/POST/PATCH/DELETE /api/v1/checks
POST /api/v1/checks/{id}/run
POST /api/v1/checks/{id}/pause
POST /api/v1/checks/{id}/resume
GET  /api/v1/checks/{id}/results?from=&to=&step=
GET  /api/v1/checks/{id}/state

GET/POST/PATCH/DELETE /api/v1/inspections
POST /api/v1/inspections/{id}/run
GET  /api/v1/inspection-runs?inspection=&status=
GET  /api/v1/inspection-runs/{id}
GET  /api/v1/inspection-runs/{id}/findings
GET  /api/v1/inspection-runs/{id}/export?format=md|html

GET/POST/DELETE /api/v1/silences
GET  /api/v1/alerts
POST /api/v1/alerts/{id}/ack
```

### 6.11 安全边界

- 不接受任意脚本/命令；Agent 只执行白名单 kind。
- SSH 巡检账号非 root，sudoers 仅放行固定只读命令（沿用 `BT_HELPER.md` 思路）。
- 参数校验 + 输出大小/超时限制，防 SSRF/DoS。
- 证据落盘前脱敏；报告导出审计。
- 检查/巡检的创建、修改、手动执行、告警确认全部审计。

### 6.12 验收

- [ ] 30s 检查连续跑 1 小时无重叠、无漏跑，延迟入库正确。
- [ ] 停服务 → 达到阈值 → down + 告警；恢复 → recovery。
- [ ] Agent 断网 → 检查 stale，不误报 down。
- [ ] 控制面重启 → 调度恢复，无重复执行。
- [ ] 原始结果 7 天清理、5 分钟 rollup 正确。
- [ ] 巡检报告含证据、可导出；同一指纹跨轮次去重。
- [ ] SSH 检查只读账号；任意命令被拒绝。

---

## 7. 通知与飞书推送

巡检完成、检查翻转、AI 审批请求都需要主动触达。v1 先做飞书，其他渠道复用同一投递管线。

### 7.1 触发事件

| event | 触发点 | 默认策略 |
|---|---|---|
| `inspection.completed` | 巡检跑完 | warning/critical 必推；ok 每日汇总推一次 |
| `inspection.failed` | 巡检执行失败 | 必推 |
| `check.down` / `check.recovered` | 检查状态翻转 | down 必推，recovered 合并推送 |
| `ai.approval_required` | AI 生成写操作提案 | 必推，带审批链接 |
| `forward.failed` | 转发应用/观测失败 | 必推 |
| `credential.reveal` | 高敏凭据被查看 | 可选，默认只落审计 |

### 7.2 两种飞书接入

| 模式 | 凭证 | 能力 | 适用 |
|---|---|---|---|
| **群自定义机器人 Webhook**（v1 默认） | webhook URL + 可选签名密钥 | 只能发到固定群；支持 text/post/interactive card | 5 分钟接入，无需应用审核 |
| **企业自建应用 Bot**（v2） | app_id + app_secret + chat_id | 可发群/个人、更新/撤回消息、卡片交互回调 | 需要 `im:message:send_as_bot` 权限，机器人入群 |

- Webhook 模式用 `net/http` 直接 POST，卡片 JSON 自己拼；签名算法：`timestamp + "\n" + secret` 作为 HMAC-SHA256 的 key，对空串计算后 base64，请求体带 `timestamp` 和 `sign`。
- 应用 Bot 模式用官方 SDK `github.com/larksuite/oapi-sdk-go/v3`：

```go
import (
    lark "github.com/larksuite/oapi-sdk-go/v3"
    larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

client := lark.NewClient(appID, appSecret)
resp, err := client.Im.V1.Message.Create(ctx, larkim.NewCreateMessageReqBuilder().
    ReceiveIdType("chat_id").
    Body(larkim.NewCreateMessageReqBodyBuilder().
        ReceiveId(chatID).
        MsgType("interactive").
        Content(cardJSON).
        Build()).
    Build())
```

- 国际版 Lark 把 base_url 配成 `https://open.larksuite.com`；国内飞书用 `https://open.feishu.cn`。
- 多群/多团队：每个群一个 channel，路由按事件/严重度选择。

### 7.3 巡检卡片

卡片用飞书 `interactive` 消息，颜色跟结果走：

- `ok` → green，`warning` → yellow，`critical` / `failed` → red；
- 标题：`巡检完成 · 得分 92`；
- 字段：计划、范围、耗时、发现项（严重/警告/提示）；
- 正文：Top 3 发现项，带设备与证据摘要；
- 按钮：`查看报告`（控制面深链）、`确认`（v2 回调）、`静默 1 天`；
- 页脚：run id、时间、vantage。

示例（Webhook 模式）：

```json
{
  "msg_type": "interactive",
  "card": {
    "config": { "wide_screen_mode": true },
    "header": {
      "template": "red",
      "title": { "tag": "plain_text", "content": "巡检异常 · db-master" }
    },
    "elements": [
      {
        "tag": "div",
        "fields": [
          { "is_short": true, "text": { "tag": "lark_md", "content": "**评分**\n92 / 100" } },
          { "is_short": true, "text": { "tag": "lark_md", "content": "**发现项**\n1 严重 · 2 警告" } }
        ]
      },
      { "tag": "hr" },
      {
        "tag": "div",
        "text": { "tag": "lark_md", "content": "**根分区使用率 87%**\ndb-master · /dev/vda1" }
      },
      {
        "tag": "action",
        "actions": [
          {
            "tag": "button",
            "text": { "tag": "plain_text", "content": "查看报告" },
            "url": "https://ops.example.com/inspections/runs/R-1042",
            "type": "primary"
          }
        ]
      }
    ]
  }
}
```

要点：

- 一次巡检只发一条卡片，发现项聚合，不要每个 finding 发一条。
- 证据先脱敏、截断；卡片里不放日志全文。
- 支持 `@` 值班人：应用 Bot 用 `<at id=ou_xxx></at>`；Webhook 模式按群机器人能力降级。
- 卡片 `url` 使用配置的 `public_base_url`，不要写死。

### 7.4 投递管线

- **Outbox**：事件先落 `notify_deliveries`（pending），后台 worker 发送；控制面重启不丢。
- **重试**：3 次，指数退避 1s / 5s / 30s；失败保留 `last_error`。
- **去重**：`dedupe_key = event + target + bucket`（如 `inspection:R-1042`、`check:db-master:down:10m`），唯一索引兜底。
- **限流**：按 channel 限速；飞书群机器人有每分钟上限，超限排队而不是丢弃。
- **静默/维护窗口**：critical 始终发；warning 在 22:00–08:00 默认抑制，可配。
- **降级**：飞书失败后按路由回退到通用 Webhook/站内；仍然失败只留投递日志，不阻塞巡检。
- **成功回执**：记录飞书 `message_id`，v2 可更新/撤回。

### 7.5 数据表

```sql
CREATE TABLE notify_channels (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  kind       TEXT NOT NULL,              -- feishu_webhook|feishu_app|webhook|email
  config_ct  BLOB NOT NULL,              -- 加密 JSON：url/secret/appId/appSecret/chatId
  key_id     TEXT NOT NULL,
  enabled    INTEGER NOT NULL DEFAULT 1,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE notify_routes (
  id            TEXT PRIMARY KEY,
  event         TEXT NOT NULL,           -- inspection.completed|check.down|ai.approval_required|...
  min_severity  TEXT,                    -- info|warning|critical
  channel_id    TEXT NOT NULL REFERENCES notify_channels(id),
  quiet_hours   TEXT,                    -- 如 22:00-08:00
  enabled       INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE notify_deliveries (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  channel_id  TEXT NOT NULL,
  event       TEXT NOT NULL,
  dedupe_key  TEXT NOT NULL,
  status      TEXT NOT NULL,             -- pending|sent|failed|skipped
  attempts    INTEGER NOT NULL DEFAULT 0,
  last_error  TEXT,
  message_id  TEXT,                      -- 飞书 message_id
  at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX ux_notify_dedupe ON notify_deliveries(channel_id, dedupe_key);
```

### 7.6 API

```
GET/POST/PATCH/DELETE /api/v1/notify/channels
POST   /api/v1/notify/channels/{id}/test
GET/POST/PATCH/DELETE /api/v1/notify/routes
GET    /api/v1/notify/deliveries?status=&event=
POST   /api/v1/notify/deliveries/{id}/retry
POST   /api/v1/notify/feishu/callback     # v2 卡片交互回调
```

### 7.7 安全

- webhook URL、签名密钥、app_secret 全部走 Vault 加密；列表只显示末四位。
- SSRF：只允许 `open.feishu.cn`、`open.larksuite.com` 或显式配置的域名，强制 HTTPS。
- 回调校验签名/Encrypt Key + 时间戳，防重放；按 `event_id` 幂等。
- 卡片内容先脱敏：不放密码、令牌、完整日志。
- 审计：渠道创建/修改/测试、路由变更、投递失败、回调操作全部记录。

### 7.8 UI

- **设置 → 通知**：渠道列表（飞书 Webhook / 应用 Bot）、测试发送、路由（事件 → 渠道）、静默时段、投递日志。
- **巡检报告**：显示"已推送飞书"状态、手动重推、卡片预览。
- **告警**：确认后同步更新飞书卡片（v2）。
- 手机：同一套页面，卡片预览纵向排列。

### 7.9 验收

- [ ] 测试发送 3 秒内到达，卡片在飞书正常渲染。
- [ ] 巡检 warning/critical 自动推送；ok 只发每日汇总。
- [ ] 同一 run 重复触发只发一次（dedupe）。
- [ ] 飞书失败重试 3 次并记录 `last_error`，不阻塞巡检。
- [ ] 错误签名被拒绝；日志与卡片中无密钥。
- [ ] v2 卡片按钮回调验签通过，确认动作写入审计。

---

## 8. HTTP API 增量

在原有 `/api/v1/*` 基础上追加：

```
# 节点 / 转发 / 流量
GET/POST/PATCH/DELETE /api/v1/nodes
POST   /api/v1/nodes/{id}/test
POST   /api/v1/nodes/{id}/rotate-key
GET    /api/v1/nodes/{id}/ports
POST   /api/v1/nodes/{id}/ports/clean
GET/POST/PATCH/DELETE /api/v1/forwards
POST   /api/v1/forwards/{id}/apply
POST   /api/v1/forwards/{id}/enable
POST   /api/v1/forwards/{id}/disable
POST   /api/v1/forwards/{id}/migrate
GET/POST/PATCH/DELETE /api/v1/admissions
GET    /api/v1/traffic?from=&to=&groupBy=
GET    /api/v1/traffic/top?window=7d|30d&kind=
GET/PATCH /api/v1/settings
GET/POST/PATCH/DELETE /api/v1/notices
GET    /api/v1/logs?source=node|client&level=&q=

# 凭据
GET    /api/v1/credentials
POST   /api/v1/credentials
PATCH  /api/v1/credentials/{id}
DELETE /api/v1/credentials/{id}
POST   /api/v1/credentials/{id}/reveal
POST   /api/v1/credentials/{id}/test

# 资产
GET    /api/v1/assets?device=&tag=&q=
POST   /api/v1/assets
PATCH  /api/v1/assets/{id}
DELETE /api/v1/assets/{id}
GET    /api/v1/assets/{id}/route
POST   /api/v1/assets/{id}/check          # Agent 侧 TCP 探测
POST   /api/v1/assets/{id}/connect        # {mode:"terminal"|"ssh-config"}
GET    /api/v1/assets/{id}/ssh-config

# 会话
GET    /api/v1/sessions?asset=&actor=
GET    /api/v1/sessions/{id}
GET    /api/v1/sessions/{id}/recording
POST   /api/v1/sessions/{id}/terminate
WS     /api/v1/sessions/{id}/ws

# AI 模型与提示词
GET/POST/PATCH/DELETE /api/v1/ai/providers
GET/POST/PATCH/DELETE /api/v1/ai/models
POST   /api/v1/ai/models/{id}/test
GET/POST/PATCH/DELETE /api/v1/ai/prompts

# AI 对话与工具
GET/POST /api/v1/ai/conversations
GET    /api/v1/ai/conversations/{id}
POST   /api/v1/ai/conversations/{id}/messages   # SSE
POST   /api/v1/ai/conversations/{id}/cancel
GET    /api/v1/ai/tool-calls
POST   /api/v1/ai/tool-calls/{id}/approve
POST   /api/v1/ai/tool-calls/{id}/reject
GET    /api/v1/ai/usage?from=&to=&groupBy=
GET/POST/PATCH/DELETE /api/v1/ai/policies

# 数据源
GET/POST/PATCH/DELETE /api/v1/datasources
POST   /api/v1/datasources/{id}/test
POST   /api/v1/datasources/{id}/query
GET    /api/v1/datasources/{id}/schema

# 轮询 / 轮检
GET/POST/PATCH/DELETE /api/v1/checks
POST   /api/v1/checks/{id}/run
POST   /api/v1/checks/{id}/pause
POST   /api/v1/checks/{id}/resume
GET    /api/v1/checks/{id}/results?from=&to=&step=
GET/POST/PATCH/DELETE /api/v1/inspections
POST   /api/v1/inspections/{id}/run
GET    /api/v1/inspection-runs?inspection=&status=
GET    /api/v1/inspection-runs/{id}
GET    /api/v1/inspection-runs/{id}/findings
GET    /api/v1/inspection-runs/{id}/export?format=md|html
GET/POST/DELETE /api/v1/silences
GET    /api/v1/alerts
POST   /api/v1/alerts/{id}/ack

# 通知 / 飞书
GET/POST/PATCH/DELETE /api/v1/notify/channels
POST   /api/v1/notify/channels/{id}/test
GET/POST/PATCH/DELETE /api/v1/notify/routes
GET    /api/v1/notify/deliveries?status=&event=
POST   /api/v1/notify/deliveries/{id}/retry
POST   /api/v1/notify/feishu/callback

# 性能
GET    /api/v1/metrics/overview
GET    /api/v1/metrics/devices/{id}/latest
GET    /api/v1/metrics/devices/{id}?from=&to=&step=&metric=&labels=
GET    /api/v1/metrics/devices/{id}/top?at=
GET/POST/PATCH/DELETE /api/v1/metrics/thresholds

# 管理
POST   /api/v1/admin/rotate-master-key
POST   /api/v1/auth/step-up
```

约定：
- JSON camelCase；错误 `{"error":{"code","message","details"}}`。
- 列表统一 `limit/cursor`。
- 创建支持 `Idempotency-Key`。
- 所有非 GET 校验 `Origin` + CSRF Token。

Agent 侧协议只加能力声明，不加凭据字段：

```go
type Hello struct {
    Version      string   `json:"version"`
    OS           string   `json:"os"`
    Arch         string   `json:"arch"`
    LocalRev     int64    `json:"localRev"`
    LocalHash    string   `json:"localHash,omitempty"`
    Capabilities []string `json:"capabilities"` // stream, dialcheck, frpc-status, check, metrics
}
```

轮询/轮检的执行帧：

```go
// control → agent
type RunCheck struct {
    CheckID   string          `json:"checkId"`
    Kind      string          `json:"kind"`      // tcp|process|system|cert
    Params    json.RawMessage `json:"params"`
    TimeoutMS int             `json:"timeoutMs"`
}

// agent → control
type CheckResult struct {
    CheckID   string          `json:"checkId"`
    OK        bool            `json:"ok"`
    LatencyMS int             `json:"latencyMs,omitempty"`
    Value     *float64        `json:"value,omitempty"`
    Detail    json.RawMessage `json:"detail,omitempty"`
    Error     string          `json:"error,omitempty"`
}
```

性能采集帧（Agent 默认 15s 推送一条）：

```go
// agent → control
type MetricsSample struct {
    At    int64           `json:"at"`              // unix ms
    Host  json.RawMessage `json:"host,omitempty"`  // 对应 host_samples
    Disks json.RawMessage `json:"disks,omitempty"` // 对应 disk_samples
    Nets  json.RawMessage `json:"nets,omitempty"`  // 对应 net_samples
    Procs json.RawMessage `json:"procs,omitempty"` // Top N，对应 proc_samples
    SkewMS int64          `json:"skewMs,omitempty"` // Agent 时钟偏差（可选）
}
```

---

## 9. 前端设计

### 9.1 视觉方向（浅色企业后台）

参考用户提供的企业后台截图，采用"浅色、白卡片、蓝色主色、分组侧栏"的风格；不复制其具体页面结构。

| 项 | 规范 |
|---|---|
| 页面底色 | `#f4f7fb`，内容区不使用深色或高对比纹理 |
| 卡片 | `#ffffff`，1px `#e8edf4` 边框，12px 圆角，`0 4px 16px rgba(29,42,60,.05)` 轻阴影 |
| 主色 | `#1677ff`（按钮、选中态、链接、主图表） |
| 辅助色 | `#13c2c2` 青、`#722ed1` 紫（仅用于区分系列） |
| 语义色 | 成功 `#00b42a`、警告 `#ff7d00`、危险 `#f53f3f` |
| 侧边栏 | 236px 白色；分组标签"监控 / 管理 / 系统"；选中项浅蓝底 `#e8f3ff` + 蓝字，无发光条 |
| 顶栏 | 64px 白色；面包屑、搜索、通知、主题、头像；底部 1px 分隔线 |
| 卡片标题 | 14px / 650；副标题 10.5px 灰色，不再使用大写字距 mono |
| 数字 | 大号数字用无衬线加粗；ID、IP、命令、日志保留等宽字体 |
| 字号/行高 | 正文 13–14px、行高 1.6–1.7；辅助文字不小于 11.5px；表格正文 13px、表头 12px；不用 9–10px 的说明文字 |
| 对比度 | 浅色主题辅助文字用 `#5f6b7a`，正文 `#1d2129`；深色主题辅助文字用 `#93a3b5`；小字不使用浅灰 |
| 图表 | 蓝/青折线与浅色面积；橙色表示告警；网格线极浅 |
| 装饰 | 去掉网格背景、扫描线、HUD 角标、霓虹发光；动效 150–200ms |
| 深色/浅色 | 默认跟随浏览器 `prefers-color-scheme`；可手动固定浅色或深色；首屏内联脚本先定主题，避免闪烁 |
| 手机 | 白色底部导航，选中项蓝色；卡片单列，统计 2×2 |

### 9.2 技术栈

| 项 | 选择 | 说明 |
|---|---|---|
| 框架 | Vue 3 + `<script setup>` + TypeScript | 与现有前端同族，重写成本低 |
| 构建 | Vite | 产物 `web/dist` |
| 路由/状态 | Vue Router + Pinia | 不引 Vuex |
| 样式 | CSS 变量 + 少量自绘组件 | 不引 Element/Naive，避免移动端二次改造 |
| 无头组件 | reka-ui（可选） | 只取 Dialog/Popover 的可访问性 |
| 图标 | lucide-vue-next | 体积小 |
| 终端 | @xterm/xterm + addon-fit | Web 终端 |
| 图表 | 概览 sparkline 自绘；性能页 uPlot | 时序数据量大，不引 ECharts |
| 校验 | valibot | 表单轻量校验 |
| PWA | 手写 SW + manifest | 只缓存壳，不缓存 API |

Go 侧嵌入注意：`//go:embed` 不能写 `../`，所以 embed 指令放在 `web/embed.go`：

```go
package web

import "embed"

//go:embed all:dist
var FS embed.FS
```

### 9.3 信息架构

```
/login

桌面侧边栏                 手机底部标签
├─ 概览                    ├─ 概览
├─ 监控                    ├─ 监控
│   ├─ 性能                ├─ 资产
│   ├─ 检查项              ├─ 巡检
│   └─ 告警                └─ 更多
├─ 巡检
│   ├─ 巡检计划
│   └─ 巡检报告
├─ 资产
│   ├─ 资产列表
│   ├─ 资产详情
│   └─ Web 终端（全屏）
├─ 设备
├─ 转发与节点
│   ├─ 端口转发
│   ├─ 域名解析
│   ├─ 私有隧道 / P2P
│   ├─ 节点与端口池
│   └─ 流量统计
├─ 凭据
├─ AI 运维
│   ├─ 对话
│   ├─ 模型
│   ├─ 提示词
│   └─ 用量
├─ 会话
├─ 审计
└─ 设置
    ├─ 基础配置与功能开关
    ├─ 通知（飞书 / Webhook）
    ├─ 公告与日志
    ├─ 安全（口令、TOTP、主密钥）
    └─ 保留期与版本
```

页面清单与要点：

1. **登录**：口令 + 可选 TOTP；失败限流；不记住密码。
2. **概览**：在线设备、down 检查数、critical 发现、最忙设备、异常转发、最近会话。
3. **监控**：检查项列表（状态灯、延迟 sparkline、最后错误、暂停/立即执行）；按设备/资产分组。
4. **性能**：设备选择、时间范围（1h/6h/24h/7d/30d）、CPU/负载/内存/磁盘/网络图表、挂载点表、网卡表、Top 进程、阈值编辑。
5. **检查详情**：状态时间线、延迟图表、最近结果、关联资产与凭据、告警历史。
6. **巡检**：计划列表、下次执行、最近评分；手动触发。
7. **巡检报告**：按严重度分组、证据查看、与上一轮 diff、导出 Markdown/HTML。
8. **告警**：列表、确认、静默（带到期）、维护窗口。
9. **设备**：在线灯、版本、`applied_rev`、`last_error`；安装命令一键复制；密钥轮换只显示一次。
10. **转发与节点**：顶部今日 IN/OUT 与节点在线；节点条显示协议、能力开关、端口段、配额；Tab 分端口转发/域名解析/私有隧道/P2P/代理；表格带 7 天流量与配额；状态 `saved → applied → observed`，冲突 409。节点抽屉管理端口池、域名策略、限流与密钥轮换。
11. **资产**：卡片/表格；跳板路径 chip（`控制面 → 节点 → agent → 目标`）；连通性；连接按钮。
12. **资产详情**：基本信息、凭据绑定、跳板链、主机指纹、最近探测、性能卡、检查项、会话历史。
13. **Web 终端**：全屏、可复制、移动端快捷键条（Ctrl/Esc/Tab/方向键）、字号、断线重连。
14. **凭据**：类型图标、指纹、最后使用；新建/替换；reveal 二次确认；列表不见明文。
15. **AI 运维**：对话、模型、提示词、用量；工具调用卡片、变更提案 diff、审批、上下文占用、数据源只读。
16. **会话**：进行中/历史、录制回放、终止。
17. **审计**：时间线 + 过滤；导出 CSV。
18. **设置**：基础配置与功能总开关、公告、节点/客户端日志、改密码、TOTP、主密钥轮换、保留期、外观（跟随系统/浅色/深色）、构建版本。
19. **通知**：飞书渠道（Webhook / 应用 Bot）、测试发送、事件路由、静默时段、投递日志与重推；巡检报告页显示推送状态与卡片预览。

### 9.4 双端适配规范

| 项 | 桌面 | 手机 |
|---|---|---|
| 断点 | >1024 | ≤640（641–1024 为平板折叠态） |
| 导航 | 左侧 240px 固定 | 底部 56px + 安全区 |
| 列表 | 表格 + 右侧详情抽屉 | 卡片列表 → 详情整页 |
| 弹层 | Dialog / Drawer | 全屏 Sheet |
| 操作 | hover 显示 + 快捷键 | 常驻"更多"菜单，触控 ≥44px |
| 表单 | 两列 | 单列，主按钮吸底 |
| 终端 | 分栏 + 可拖拽 | 全屏 + 键盘工具条 |
| 输入 | 14px | 16px（防 iOS 缩放） |
| 视口 | `100vh` | `100dvh` + `env(safe-area-inset-*)` |

补充：
- PC 设置里保留"手机视图"开关（现有产品已有该能力），通过根节点 class 强制窄布局。
- 主题三态：跟随系统（默认）/ 浅色 / 深色；系统外观变化时实时切换，手动选择后持久化到本地。
- 无横向滚动是验收项（360px 宽）。
- 动效克制：只做 150–200ms 的透明度/位移，不做大面积动画。

**iOS 侧滑返回与路由**

- 一个功能一个页面；路由用 `createWebHistory`，不用 hash。每次导航都产生真实历史记录，iOS Safari 边缘侧滑与浏览器返回键行为一致。
- 列表、详情、新建、编辑都是独立路由：`/devices`、`/devices/:id`、`/devices/new`、`/devices/:id/edit`；节点、转发同理。筛选条件用 query，不把多个功能塞进一个页面的页签。
- 新建/编辑即使视觉上是弹层，也必须改变 URL；侧滑返回应关闭表单而不是离开整个功能区。
- 手机端表格改为卡片列表，避免左侧贴边的横向滚动容器抢占侧滑手势；确需横向滚动时与屏幕边缘留出间距。
- `scrollBehavior` 在返回/前进时恢复位置，新导航回到顶部。
- `viewport-fit=cover`，底部导航与主按钮使用 `env(safe-area-inset-bottom)`；输入框 16px 防止 iOS 聚焦缩放。
- 深链接被 401 拦截时跳转 `/login?redirect=<当前路径>`，登录后回到原页面。

### 9.5 前端安全

- 会话用 HttpOnly + Secure + SameSite=Lax Cookie，**不放 localStorage**。
- 所有写请求带 CSRF Token，并校验 `Origin`。
- API 响应 `Cache-Control: no-store`；SW 不缓存 `/api`。
- 终端页面禁止被 iframe 嵌入（CSP `frame-ancestors 'none'`）。
- 复制凭据后 30 秒自动清空剪贴板提示（可选）。

---

## 10. 认证与安全清单

- 口令哈希使用 bcrypt（m=64MB, t=3, p=4）；初始管理员口令由部署脚本从环境变量读取并哈希入库，**禁止**写进仓库、日志、前端包或环境示例。
- 会话：服务端 `sessions` 表 + 12h 空闲 / 7d 绝对超时；支持"登出所有设备"。
- 步进认证：reveal、连接生产资产、轮换主密钥时要求重新输入口令（可选 TOTP）。
- 限流：登录 5/min/IP；reveal 5/min；连接 10/min；按账号 + IP 双维度。
- 审计：登录、失败、reveal、连接、查询、轮换、配置变更全记录；`detail` 中禁止出现明文。
- 日志：slog JSON；统一脱敏中间件，禁止打印请求体中的 `password/secret/key/token` 字段。
- 主机指纹：SSH 首次连接人工核对，之后固定；变化即阻断。
- 轮询/巡检不接受任意命令；Agent 只执行白名单 kind，SSH 检查使用低权限账号 + sudoers 白名单。
- 检查参数校验、输出限长、超时；证据落盘前脱敏。
- 轮询/巡检/告警确认全部审计。
- Agent 侧：设备密钥 0600；CIDR/端口白名单；并发上限；流 TTL。
- 控制面部署：默认只监听 `127.0.0.1:18443`，通过 SSH 隧道或 Nginx 暴露；`/agent/ws` 单独端口，便于限流和禁公网。
- 备份：SQLite 用 `VACUUM INTO`；数据库与主密钥分开存放；录制目录单独备份策略。

---

## 11. 仓库与落地顺序

当前工作区所有文件为删除态，但 `HEAD` 完整。建议：

```sh
git restore .                 # 先恢复
git tag v0-python             # 旧版留档
git switch -c go-rewrite      # 新分支
```

- 旧 Python 代码留在 tag/分支，不直接删除。
- Go 重写在同一仓库继续，`docs/` 与历史保留。
- 若旧版仍在生产运行，用 `legacy/python` 分支维护，`main` 走 Go。
- 不建议在"空工作区"上新建目录，容易和恢复操作打架。

包结构沿用既定设计，新增：

```
internal/
├── vault/        信封加密、密钥轮换
├── credential/   CRUD + 使用票据
├── asset/        资产、路径解析、探测
├── forward/      端口/域名/隧道/P2P/代理条目、准入、端口池
├── node/         节点能力、域名策略、限流与配额
├── traffic/      日流量聚合、排行、配额判断
├── relay/        yamux 流、SSH 桥、DB/HTTP dialer
├── sessions/     会话与录制
├── check/        检查项、执行器、结果
├── inspection/   profile、运行、发现项
├── schedule/     到期扫描、租约、并发池
├── notify/       飞书/Webhook 渠道、卡片渲染、outbox、重试与去重
└── ai/           多模型、提示词、对话、工具与审批、长上下文、数据源
web/
├── embed.go
├── src/
└── dist/
```

现有 Python 模块的落点：

| Python | Go |
|---|---|
| `backend/jobs.py` | `internal/schedule` + `internal/check` |
| `backend/inspection.py` | `internal/inspection` + `internal/metrics` |
| `backend/public_probe.py` | `internal/check`（vantage 字段） |
| `backend/push.py` | `internal/notify` |
| `backend/sshops.py` | `internal/relay`（SSH 执行） |
| `backend/store.py` | `internal/store` |

---

## 12. 里程碑（在 Go 版 M0–M5 之上追加）

| 阶段 | 交付 | 增量工期 |
|---|---|---|
| M0 | frps/frpc 手工跑通 TCP/UDP、带宽、加密 | 1–2 天 |
| M1 | 控制面骨架、迁移、设备/节点、`/agent/ws`、心跳 | 3–4 天 |
| M2 | Agent + frptmpl + supervisor，全量下发/ack/哈希幂等 | 4–6 天 |
| M3 | 转发闭环、端口分配、rev 状态机、409 | 3–4 天 |
| M3.5 | **节点与条目管理：能力开关、端口池、域名策略、准入、流量统计** | 3–4 天 |
| M4 | 前端壳 + 登录 + 设备/转发与节点双端页面 | 4–5 天 |
| M5 | **凭据中心 + 审计 + 主密钥轮换** | 3–4 天 |
| M6 | **资产台账 + 路径解析 + Agent 探测 + 原生 SSH 编排** | 4–5 天 |
| M7 | **yamux 流 + Web 终端 + 会话录制** | 4–6 天 |
| M8 | **轮询与轮检：调度器、检查项、巡检 profile、告警** | 4–6 天 |
| M8.5 | **飞书推送：群 Webhook + 应用 Bot SDK、卡片、outbox/重试/去重、回调** | 2–3 天 |
| M9 | **服务器性能：采集、存储、降采样、图表、阈值** | 3–5 天 |
| M10 | **AI 接入：多模型、提示词、流式对话、上下文计量** | 5–7 天 |
| M11 | **AI 运维：只读工具、变更提案、diff 审批、策略与审计** | 5–7 天 |
| M12 | **长上下文：256K/1M、压缩、检索、用量与成本** | 3–5 天 |
| M13 | 观测、加固、集成测试、部署脚本 | 4–6 天 |

合计约 **55–79 天**（单人有前端经验）。可并行点：
- M3.5 可与 M4 前端壳并行，但表结构要在 M3 时留好；
- M4 前端壳可与 M2/M3 并行；
- M5 凭据中心是 M6–M12 的前置，建议提前；
- M8 复用 M6 的 Agent 流，建议紧跟 M6；
- M8.5 只依赖 M8 的告警事件，可与 M9 并行；
- M9 复用 M8 的结果/告警底座，建议紧跟 M8；
- M10 依赖 M5 凭据与 M7 会话底座；
- M11 依赖 M6 资产与 M8 检查工具；
- M12 可与 M11 并行。

建议的发布切片：

- **P1（可用隧道）**：M0–M4（含 M3.5 节点与条目管理）。
- **P2（可管凭据与资产）**：+M5–M6。
- **P3（手机可运维）**：+M7。
- **P4（可观测）**：+M8–M9、M8.5（飞书推送）。
- **P5（AI 对话与多模型）**：+M10。
- **P6（AI 运维与审批）**：+M11。
- **P7（长上下文与交付）**：+M12–M13。

---

## 13. 验收清单

功能：
- [ ] 创建 SSH 凭据 → 重启控制面 → reveal 成功；错误主密钥给出明确错误且不部分解密。
- [ ] 凭据列表/日志/前端包中 grep 不到任何明文。
- [ ] reveal 超过 5 次/分钟被限流，且每次都有审计。
- [ ] 资产绑定跳板 → 探测成功/失败都能显示原因；跳板离线时连接按钮禁用。
- [ ] 原生 SSH 命令可直接连通；主机指纹变化被阻断。
- [ ] Web 终端在手机浏览器可用，旋转屏幕后 resize 正常，空闲 30 分钟断开。
- [ ] 局域网 MySQL 通过跳板测试连接成功；`UPDATE` 被拒绝；查询有审计。
- [ ] RDP 通过 `ssh -L` 命令可连（人工验证一次）。
- [ ] 节点能力关闭后，对应类型条目创建被拒绝；端口段外无法分配；清理端口池不误删在用端口。
- [ ] 域名/私有隧道/P2P/准入按 15.11 节逐项通过；日流量与节点侧计数误差 < 1%。
- [ ] 30s 检查连续跑 1 小时无重叠、无漏跑，延迟入库正确。
- [ ] 停服务 → 达到阈值 → down + 告警；恢复 → recovery。
- [ ] Agent 断网 → 检查 stale，不误报 down。
- [ ] 控制面重启 → 调度恢复，无重复执行。
- [ ] 原始结果 7 天清理、5 分钟 rollup 正确。
- [ ] 巡检报告含证据、可导出；同一指纹跨轮次去重。
- [ ] 巡检 warning/critical 自动推送飞书卡片；ok 只发每日汇总；同一 run 不重复。
- [ ] 飞书测试发送 3 秒内到达；失败重试 3 次并记录 `last_error`，不阻塞巡检。
- [ ] 应用 Bot 卡片按钮回调验签通过，确认动作写入审计。
- [ ] SSH 检查只读账号；任意命令被拒绝。
- [ ] 采集值与 `top/free/df/ss` 对照，CPU/内存/磁盘误差 < 5%。
- [ ] 15s 采样连续 1 小时图表无断点；断线 10 分钟后重连能补传。
- [ ] 磁盘/网卡/进程维度归属正确；Top N 不超过 10。
- [ ] 阈值持续 5 分钟触发告警，恢复自动关闭。
- [ ] Agent 采集开销 < 1% CPU / 20MB 内存。
- [ ] 原始 7 天、1min 30 天、5min 180 天清理正确。
- [ ] 移动端性能页无横向滚动，图表可读。
- [ ] 多模型切换与回退生效；256K/1M 上下文窗口显示与预算一致。
- [ ] 提示词模板变量渲染正确；模板变更进入审计。
- [ ] 只读工具可调用；写工具只生成提案，未审批无法执行。
- [ ] 审批页展示目标、diff、影响面、回滚；高风险要求 TOTP。
- [ ] 上下文到 80% 自动压缩后，关键结论仍保留；token/成本记录准确。
- [ ] 任意 shell 调用被拒绝；日志中的提示注入不能触发工具。
- [ ] 1M 模型切换有成本提示；本地模型不出网。
- [ ] 20 并发创建同一端口 → 恰好 1 个 201，其余 409。
- [ ] 控制面重启 → 已建隧道不断；Agent 自动重连，`applied_rev` 不倒退。
- [ ] 360px 宽无横向滚动；桌面 1440px 布局正常；PC 可切手机视图。
- [ ] PWA 可安装，离线只显示壳页面。

安全：
- [ ] 未登录访问所有 `/api/v1/*` 返回 401。
- [ ] 跨站 POST 被 `Origin`/CSRF 拦截。
- [ ] 终端 WS 校验同源与会话归属。
- [ ] Agent 拒绝 `169.254.169.254` 与未授权 CIDR。
- [ ] 会话录制文件不可通过静态路径直接下载，必须走鉴权接口。
- [ ] 飞书 webhook/app_secret 加密存储；卡片、日志、报错中无明文。
- [ ] 非白名单域名 / HTTP 明文 webhook 被拒绝；回调签名错误或时间戳过期被拒绝。

---

## 14. 待确认

1. **"AI 数据库配置"**：是"给 AI 用的数据源"（本文按此展开），还是"AI 功能自身的存储配置"？两者可同时存在，但优先级不同。
2. **浏览器 RDP/VNC** 是否在近期范围内？若必须，需单列 Guacamole 评估（部署重、资源占用高）。
3. **是否需要多用户/RBAC**？v1 默认单管理员；若团队使用，`users/roles/asset_grants` 要在 M5 前定，否则后期改表成本高。
4. **会话录制**是否涉及合规留存（等保/审计）？影响保留期、加密与导出方式。
5. **轮询间隔**：默认 60s、最小 30s 是否合适？高频检查会放大 Agent 和 SQLite 写入压力。
6. **告警渠道**：已定站内 + 飞书（群 Webhook / 应用 Bot）+ 通用 Webhook；是否还需邮件/企业微信/钉钉？
7. **数据保留**：原始结果 7 天、5 分钟 rollup 90 天、巡检报告 180 天是否合适？
8. **巡检自定义脚本**：建议 v1 不允许，只做 profile + 白名单命令；确有需要再加受限脚本沙箱。
9. **性能采样间隔**：默认 15s 是否合适？设备数超过 100 时建议 30–60s。
10. **性能维度**：是否需要每核 CPU、GPU、温度、SMART？这些会增加采集复杂度和权限要求。
11. **性能数据保留**：原始 7 天、1min 30 天、5min 180 天是否合适？
12. **实时 tail**：性能页是否需要 WS 实时曲线，还是聚合图表就够？
13. **首批模型**：必须接入哪些提供商？OpenAI 兼容 / Anthropic / Gemini / 本地 vLLM，优先级如何？
14. **写操作边界**：v1 是否只做"提案 + 人工审批"？低风险操作是否允许自动执行？
15. **成本预算**：长上下文默认 256K、1M 按需切换，是否需要每日 token/成本上限？
16. **提示词归属**：管理员统一维护，还是允许每个用户自建模板？
17. **数据不出网**：是否有硬性要求？如果有，本地模型与本地检索要前置。
18. **仓库选择**：同仓新分支（推荐）/ 新仓库 / 全新目录。
19. **飞书接入模式**：v1 只做群 Webhook（最简单）是否够？应用 Bot 需要自建应用与审核，是否同步排期？
20. **飞书卡片交互**：是否需要"确认/静默"按钮？这要求应用 Bot + 公网可达的回调地址；纯 Webhook 只能跳转链接。
21. **飞书推送范围**：只推巡检与告警，还是 AI 审批、部署结果、凭据查看也推？
22. **代理隧道**：gostc-open 的 HTTP/SOCKS 代理是否纳入 v1？它和"端口转发"是两种产品能力，建议 v2。
23. **流量配额**：是否需要按节点/条目做上行、下行、总量配额与重置日？若只做观测不做限制，可省掉执行侧逻辑。
24. **多租户**：是否需要"节点绑定用户、用户自助开通"？这会把单管理员模型升级为 RBAC + 配额，工期显著增加。

---

## 15. 附：gostc-open 后台管理内容对照

参考 [gostc-open](https://github.com/SianHH/gostc-open) 的**管理内容与数据维度**，不参考其布局、配色、组件和图标。下面的取舍以本项目"单管理员运维平台"为前提。

### 15.1 借鉴原则

- 只对齐"管什么"，界面仍用本项目的科技感设计系统。
- 保留其成熟的状态与维度：启用/停用、节点/客户端归属、流量方向、端口池、准入白名单、7/30 天统计。
- 去掉商用多租户部分：积分、余额、签到、套餐计费、发卡支付、开放注册。
- 所有节点密钥、访问密钥、代理认证进 Vault；列表只显示末四位。
- 生命周期沿用本项目状态机 `saved → applied → observed`，不引入其"套餐到期"。

### 15.2 管理内容映射

| gostc-open 菜单 | 其管理内容 | 本项目落点 | 取舍 |
|---|---|---|---|
| 全站统计 | 节点/客户端/用户/条目计数；今日上下行；7/30 天流量排行 | 概览 + 转发与节点页 | 借计数与流量，去用户/积分 |
| 系统配置 | 标题、favicon、BaseURL、功能开关、注册/签到、邮件 | 设置 → 基础 / 功能开关 | 借站点与开关，去注册签到 |
| 通知管理 | 公告标题、内容、隐藏、排序、发布时间 | 设置 → 公告；可同步飞书 | 借公告，去邮件营销 |
| 用户管理 | 账号、邮箱、角色、积分、流量、注册时间 | `users/roles` 预留，v1 单管理员 | 只留表结构 |
| 节点管理 | 地址、协议、功能开关、域名策略、端口段、配额、限流、标签 | 转发与节点 → 节点 | 全借，去计费 |
| 节点底层/套餐 | 计费方式、周期、积分、速率、并发、连接数 | 节点策略：速率/并发/连接数 | 借限流，去积分 |
| 客户端 | 名称、用户、连接密钥 | 设备/Agent + 设备密钥 | 借密钥模型 |
| 域名解析 | 目标、域名前缀、自定义域名/证书、强制 HTTPS、泛域名 | 转发 kind=`host` | 全借 |
| 端口转发 | 目标、公网端口、代理协议、匹配规则、加密/压缩/复用 | 转发 kind=`port` | 全借 |
| 私有隧道 | 目标、访问密钥、无延迟、加密/压缩/复用 | 转发 kind=`tunnel` | 全借 |
| P2P 隧道 | 目标、访问密钥、是否中继 | 转发 kind=`p2p` | 全借 |
| 代理隧道 | 协议、端口、认证用户/密码 | 转发 kind=`proxy` | 建议 v2 |
| 准入控制 | 白名单开关 + 列表 | `forward_admissions` | 全借 |
| 日志 | 节点日志、客户端日志 | 审计 + 节点日志页 | 借过滤与保留 |
| 关于/资源下载 | 版本、下载地址 | 设置 → 构建信息 | 只留版本 |

### 15.3 节点管理字段

| 分组 | 字段 | 说明 |
|---|---|---|
| 基本 | `name` / `remark` / `tags` / `index_value` | 名称、介绍、标签、排序 |
| 接入 | `protocol` / `address` / `http_port` / `replace_address` | TCP/KCP/QUIC/WS/WSS；替换地址一般留空 |
| 能力 | `feature_web` / `feature_forward` / `feature_tunnel` / `feature_p2p` / `feature_proxy` | 每节点独立开关，未开启的条目不可创建 |
| 端口 | `forward_ports` / `node_ports` | 允许端口段，如 `10001-11000,20000`；端口池可清理 |
| 域名 | `domain` / `url_tpl` / `deny_domain_prefix` / `allow_wildcard` | 基础域名、展示模板、禁止前缀、是否允许泛域名 |
| 连接 | `max_pool_count` / `p2p_disable_forward` / `rules` | 最大复用连接、禁用 P2P 中继、allow/deny 规则 |
| 配额 | `limit_total_gb` / `limit_kind` / `limit_reset_day` | 总量/上行/下行；0 表示不循环 |
| 限流 | `limiter_mbps` / `concurrency` / `connections` | 速率、并发、连接数；0 为不限 |
| 运行 | `status` / `last_seen_at` / `version` / `last_error` | 在线、心跳、Agent 版本、错误 |

节点密钥：创建时只显示一次，支持轮换；旧密钥保留 24h 便于滚动。

### 15.4 条目管理字段

公共字段：

`name`、`kind`、`node_id`、`client_id`、`target_ip`、`target_port`、`enabled`、`status`、`use_encryption`、`use_compression`、`pool_count`、`last_error`。

分类型字段：

| kind | 专有字段 |
|---|---|
| `port` | `public_port`、`proxy_protocol`、`matcher_enable`、`matcher`（TCP/SSH） |
| `host` | `domain_prefix`、`custom_domain`、`custom_cert_ct`、`custom_key_ct`、`force_https`、`allow_wildcard` |
| `tunnel` | `vkey_ct`（访问密钥）、`no_delay` |
| `p2p` | `vkey_ct`、`relay`（是否中继） |
| `proxy` | `protocol`、`auth_user`、`auth_password_ct` |

准入控制（按客户端或条目）：

`white_enable`、`white_list`（CIDR/IP，逗号分隔）。

操作：新建、编辑、启用/停用、应用、续期、迁移节点、删除（二次确认，提示影响范围）。

### 15.5 流量与统计

- 统计粒度：节点、客户端、条目、隧道、代理；方向分上行/下行。
- 时间维度：今日、最近 7 天、最近 30 天；按天聚合。
- 展示：概览卡片（今日 IN/OUT）、Top N 排行、条目详情里的 7/30 天曲线。
- 口径：在节点入口计数，避免控制面与 Agent 重复统计；时区固定 `Asia/Shanghai`。
- 保留：日聚合 400 天；原始计数不落库，只保留日增量。
- 配额执行（可选）：达到 `limit_total_gb` 后停用条目并推送飞书；仅观测模式只告警不阻断。

### 15.6 系统管理

- **基础配置**：站点标题、favicon、`public_base_url`、默认时区、功能总开关。
- **公告**：标题、内容、隐藏、排序、发布时间；可勾选同步飞书。
- **日志**：节点日志、客户端日志，按节点/级别/关键字/时间过滤；日志脱敏后入库，保留 30 天。
- **用户**：v1 单管理员；保留 `users/roles` 表，不做积分与注册。
- **关于**：版本、提交、Go/frpc 版本、许可证。

### 15.7 明确不借鉴

- 视觉：布局、配色、图标、SVG、组件风格。
- 商业化：积分、余额、签到、套餐计费、发卡网、易支付。
- 开放注册、邮箱绑定、用户自助改密（单管理员场景只保留 TOTP 与 CLI 重置）。
- 用户流量排行、节点-用户绑定（多租户）——表结构可预留，v1 不做。
- 其 `dist.zip` 内置前端与默认账号体系；本项目仍走自建 Vue + 单管理员。

### 15.8 数据表增量

```sql
-- 现有 nodes / forwards 增补；只列缺失列，迁移脚本需先查 PRAGMA table_info 再决定是否 ADD
-- 现有 nodes 增补（SQLite 逐列 ADD COLUMN）
ALTER TABLE nodes ADD COLUMN remark TEXT;
ALTER TABLE nodes ADD COLUMN tags TEXT NOT NULL DEFAULT '';
ALTER TABLE nodes ADD COLUMN index_value INTEGER NOT NULL DEFAULT 1000;
ALTER TABLE nodes ADD COLUMN protocol TEXT NOT NULL DEFAULT 'tcp';   -- tcp|kcp|quic|ws|wss
ALTER TABLE nodes ADD COLUMN http_port INTEGER;
ALTER TABLE nodes ADD COLUMN replace_address TEXT;
ALTER TABLE nodes ADD COLUMN forward_ports TEXT NOT NULL DEFAULT ''; -- 10001-11000,20000
ALTER TABLE nodes ADD COLUMN domain TEXT;
ALTER TABLE nodes ADD COLUMN url_tpl TEXT;
ALTER TABLE nodes ADD COLUMN deny_domain_prefix TEXT;
ALTER TABLE nodes ADD COLUMN allow_wildcard INTEGER NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN feature_web INTEGER NOT NULL DEFAULT 1;
ALTER TABLE nodes ADD COLUMN feature_forward INTEGER NOT NULL DEFAULT 1;
ALTER TABLE nodes ADD COLUMN feature_tunnel INTEGER NOT NULL DEFAULT 1;
ALTER TABLE nodes ADD COLUMN feature_p2p INTEGER NOT NULL DEFAULT 1;
ALTER TABLE nodes ADD COLUMN feature_proxy INTEGER NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN max_pool_count INTEGER NOT NULL DEFAULT 5;
ALTER TABLE nodes ADD COLUMN p2p_disable_forward INTEGER NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN rules TEXT NOT NULL DEFAULT '{}';
ALTER TABLE nodes ADD COLUMN limit_total_gb INTEGER NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN limit_kind TEXT NOT NULL DEFAULT 'all';  -- all|in|out
ALTER TABLE nodes ADD COLUMN limit_reset_day INTEGER NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN limiter_mbps INTEGER NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN concurrency INTEGER NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN connections INTEGER NOT NULL DEFAULT 0;

-- 现有 forwards 增补
ALTER TABLE forwards ADD COLUMN kind TEXT NOT NULL DEFAULT 'port';    -- port|host|tunnel|p2p|proxy
ALTER TABLE forwards ADD COLUMN target_ip TEXT;
ALTER TABLE forwards ADD COLUMN target_port INTEGER;
ALTER TABLE forwards ADD COLUMN node_id TEXT;
ALTER TABLE forwards ADD COLUMN client_id TEXT;
ALTER TABLE forwards ADD COLUMN domain_prefix TEXT;
ALTER TABLE forwards ADD COLUMN custom_domain TEXT;
ALTER TABLE forwards ADD COLUMN custom_cert_ct BLOB;
ALTER TABLE forwards ADD COLUMN custom_key_ct BLOB;
ALTER TABLE forwards ADD COLUMN force_https INTEGER NOT NULL DEFAULT 0;
ALTER TABLE forwards ADD COLUMN vkey_ct BLOB;
ALTER TABLE forwards ADD COLUMN relay INTEGER NOT NULL DEFAULT 0;
ALTER TABLE forwards ADD COLUMN matcher TEXT;
ALTER TABLE forwards ADD COLUMN use_encryption INTEGER NOT NULL DEFAULT 1;
ALTER TABLE forwards ADD COLUMN use_compression INTEGER NOT NULL DEFAULT 1;
ALTER TABLE forwards ADD COLUMN pool_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE forwards ADD COLUMN proxy_protocol TEXT;
ALTER TABLE forwards ADD COLUMN auth_user TEXT;
ALTER TABLE forwards ADD COLUMN auth_password_ct BLOB;

-- 端口池（替代仅靠唯一索引的隐式占用）
CREATE TABLE node_ports (
  node_id    TEXT NOT NULL,
  port       INTEGER NOT NULL,
  forward_id TEXT,
  allocated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (node_id, port)
);

-- 准入白名单
CREATE TABLE forward_admissions (
  id           TEXT PRIMARY KEY,
  client_id    TEXT,
  forward_id   TEXT,
  white_enable INTEGER NOT NULL DEFAULT 0,
  white_list   TEXT NOT NULL DEFAULT '',
  updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 日流量
CREATE TABLE traffic_daily (
  day          TEXT NOT NULL,             -- YYYY-MM-DD，Asia/Shanghai
  origin_kind  TEXT NOT NULL,             -- node|client|forward|tunnel|proxy
  origin_id    TEXT NOT NULL,
  input_bytes  INTEGER NOT NULL DEFAULT 0,
  output_bytes INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (day, origin_kind, origin_id)
);

-- 系统设置与公告
CREATE TABLE system_settings (
  kind  TEXT NOT NULL,                    -- base|feature|retention
  key   TEXT NOT NULL,
  value TEXT NOT NULL,
  value_ct BLOB,                          -- 敏感项走密文
  key_id TEXT,
  PRIMARY KEY (kind, key)
);

CREATE TABLE notices (
  id           TEXT PRIMARY KEY,
  title        TEXT NOT NULL,
  content      TEXT NOT NULL,
  hidden       INTEGER NOT NULL DEFAULT 0,
  index_value  INTEGER NOT NULL DEFAULT 1000,
  published_at DATETIME,
  created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

迁移注意：`forwards` 原唯一索引 `ux_forwards_port` 需改为部分索引，避免域名/隧道条目被端口唯一约束误伤：

```sql
DROP INDEX IF EXISTS ux_forwards_port;
CREATE UNIQUE INDEX ux_forwards_public ON forwards(node_id, public_port)
  WHERE kind = 'port' AND public_port IS NOT NULL;
```

### 15.9 API 增量

```
GET/POST/PATCH/DELETE /api/v1/nodes
POST   /api/v1/nodes/{id}/test
POST   /api/v1/nodes/{id}/rotate-key
GET    /api/v1/nodes/{id}/ports
POST   /api/v1/nodes/{id}/ports/clean
GET/POST/PATCH/DELETE /api/v1/forwards
POST   /api/v1/forwards/{id}/apply
POST   /api/v1/forwards/{id}/enable
POST   /api/v1/forwards/{id}/disable
POST   /api/v1/forwards/{id}/migrate
GET/POST/PATCH/DELETE /api/v1/admissions
GET    /api/v1/traffic?from=&to=&groupBy=node|client|forward|tunnel|proxy
GET    /api/v1/traffic/top?window=7d|30d&kind=
GET/PATCH /api/v1/settings
GET/POST/PATCH/DELETE /api/v1/notices
GET    /api/v1/logs?source=node|client&level=&q=
```

### 15.10 UI 落点

- **转发与节点**（原"端口转发"页升级）：
  - 顶部统计：今日下行/上行、节点在线、规则总数、异常条目。
  - 节点条：节点卡片显示地址、协议、能力徽标、端口段、配额进度、在线状态。
  - Tab：端口转发 / 域名解析 / 私有隧道 / P2P / 代理。
  - 表格列：名称、客户端、节点、目标、公开入口、状态、7 天流量、配额、操作。
  - Drawer：条目详情（加密/压缩/复用/匹配/准入/最近错误）。
  - 节点管理 Drawer：能力开关、端口池与清理、域名策略、限流与配额、密钥轮换。
- **概览**：增加"今日流量"和"7/30 天 Top 条目"两块。
- **设置**：基础配置、公告、日志、功能开关。
- **手机**：节点卡片横滑；条目表转卡片；操作收进"更多"。

### 15.11 工期与验收

- 新增 **M3.5 节点与条目管理**：3–4 天（在 M3 转发闭环之后、M4 前端之前或并行）。
- 总工期调整为 **55–79 天**。

验收：

- [ ] 节点能力关闭后，对应类型条目创建被拒绝并提示原因。
- [ ] 端口段之外的端口无法分配；清理端口池后已用端口不被误删。
- [ ] 域名条目支持自定义域名/证书/强制 HTTPS/泛域名开关。
- [ ] 私有隧道/P2P 的访问密钥只显示一次，可轮换。
- [ ] 准入白名单外来源被拒绝，白名单内可连通。
- [ ] 今日/7 天/30 天流量与节点侧计数误差 < 1%；无重复计数。
- [ ] 节点限速/并发/连接数生效，0 表示不限。
- [ ] 删除节点前提示影响的条目数量，并要求二次确认。
