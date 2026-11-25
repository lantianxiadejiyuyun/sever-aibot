# AI 模型、上下文与审批

本功能通过 `Server.MountAI(r)` 挂载，接口前缀 `/api/v1/ai`。AI 页面提供服务商/模型/模板配置、持久会话、流式对话、审批和用量。当前实现以文本运维闭环为范围；模型生成的提案不会自动执行。

## 配置与协议

1. 在凭据页登记 `api_token`。AI 服务商只保存 `credentialId`，沿用现有 vault 加密；API 不返回解密后的密钥。被引用的凭据不能直接删除。
2. 创建服务商，填写协议、base URL 和凭据引用。服务商地址由控制面访问，本地服务可不配置鉴权；原生 Anthropic 要求密钥。
3. 创建模型，填写服务端模型 ID、上下文窗口、输出上限、是否支持工具、可选故障回退模型及输入/输出价格。
4. 执行连接测试。测试会向所选服务商发出一个最小真实生成请求，因此应先核实端点及费用。自动化测试只使用本地 fake SSE，不连接真实外部模型。

| 协议 | base URL 示例 | 说明 |
| --- | --- | --- |
| `openai_compatible` | `https://api.openai.com/v1` | Chat Completions SSE，支持符合此接口的 vLLM、Ollama `/v1` 端点 |
| `anthropic` | `https://api.anthropic.com` | 官方 Go SDK，原生 Messages SSE、system 和 tool_use |
| `gemini` | `https://generativelanguage.googleapis.com/v1beta` | 原生 streamGenerateContent SSE、systemInstruction 和 functionCall |

不将其他协议假定为 OpenAI-compatible。当前不处理图片、音频或模型内部思维内容。端点重定向被拒绝，服务商错误响应正文不会直接返回浏览器。

配置增删改及连接测试要求 CSRF 和有效管理员二次验证。配置模型的回退链表示允许将相同上下文发送到回退服务商。仅在尚未输出内容/工具调用时，对网络错误、HTTP 408/429/5xx 回退，最多三跳；鉴权、格式错误、已输出的残缺响应不回退。每次回退按实际模型重新计算预算，费用记录实际服务模型。

## 会话、模板和预算

会话和消息持久化，按管理员所有权隔离。模板修改保留历史版本，恢复历史内容会创建新版本；会话固定保存创建时的模板版本和内容快照。模板目前是静态文本，不支持变量替换。

预算使用 UTF-8 字节数加消息 framing 开销进行保守估算，另计注册工具 schema 大小，并预留最大输出。它不是服务商 tokenizer 的精确计数。系统提示和本轮用户消息不能静默丢弃，超出预算直接拒绝。

历史压缩在可用窗口约 80% 时尝试移除完整旧轮次；被移除部分中最近四条消息各保留最多 240 UTF-8 字节的摘录，标明来源、截断及不可信属性，作为用户级数据发送。加入摘录后再次校验完整窗口；装不下则明确退回整轮裁剪。原始历史仍在数据库中。此自动预算机制是有损摘录，**不是语义摘要**，可能遗漏关键事实。另有显式授权的语义摘要和固定事实功能，见下文。

SSE `budget` 返回 `estimatedInput`、`outputReserve`、`contextWindow`、`droppedMessages`、`method`，存在摘录时另含 `summaryMethod`、`summaryBytes`。页面同步展示裁剪/摘录情况。每个会话同一时间只允许一次生成，每个挂载运行时最多四次并发；生成最长三分钟。

## 任务默认模型、固定事实和语义摘要

每位管理员可为 `chat`、`inspection`、`log_summary`、`change` 设置默认模型；保存需要二次验证。创建会话时显式 `modelId` 优先，否则解析所选任务（默认 chat）的配置。未配置、模型或服务商禁用时明确拒绝。会话保存具体模型 ID，后续路由修改不改变已有会话；任务名称不授予工具或写操作权限。

固定事实仅限本会话，由用户手动保存，最多 8000 UTF-8 字节。更新携带 revision，过期版本返回 409，防止覆盖并发编辑。本轮勾选 `includeFacts` 才附加当前保存的事实，以不可信用户级数据发送，并计入预算。事实不会自动从模型输出中写入。清空事实不删除已经发送到历史消息的副本。

日志和历史摘要要求 `consent:true`，只调用当前会话模型，不执行工具、不自动回退。先按 UTF-8 安全边界分段归纳，再逐轮合并；最多 32 次调用、4 轮、3 分钟、256 KiB 输入，小上下文模型按窗口进一步收紧限额，无法容纳时拒绝。日志和中间摘要均标记为不可信数据；结果有损，需要核对原始证据。

历史摘要保存覆盖的最后消息 ID；本轮勾选 `includeSummary` 才用摘要替代被覆盖的历史，后续完整消息轮次继续保留。原始会话历史不删除。粘贴日志只保存最终摘要和提交标记，不保存原始日志；摘要内容仍可能包含日志中的敏感内容，发送前应移除不应外发的数据。

每次归纳尝试分别保存 `summary_call` 记录、实际模型、已报告 usage 和价格/费用快照。失败或取消仍保存已有调用证据，不把部分摘要当作完成结果，不覆盖已有历史摘要。记录在归纳返回后保存，进程突然退出可能丢失尚未落库的调用证据；此机制不等于供应商账单。普通对话通过 `ai_calls` 独立保存每次服务商尝试和工具续答：`attempt` 从 1 递增，`toolRound` 为 0 表示初次请求/回退，1–3 表示工具续答。每次调用前先保存 interrupted 记录及服务商、实际选用的配置模型 ID、发送给服务商的模型名称、预算和价格快照；返回后通过独立持久化上下文保存完成/失败/取消状态、finish reason、已知 usage 和费用。进程崩溃会留下未知用量的 interrupted 证据；调用前落库不代表服务商一定收到请求。`providerModel` 是实际发送的模型名称，不声称确认服务商内部别名所指向的模型版本。

## 只读工具及数据边界

模型支持工具时可调用以下固定工具，实际读取必须勾选本轮“附加受限运维上下文”。即使模型自行要求读取，未授权的调用也只得到拒绝结果。

- `list_devices`、`list_nodes`、`list_forwards`：受限 ID/状态投影。
- `get_device_status`、`get_metrics`：指定设备状态及最近一小时最多 60 条已存指标。
- `get_check_results`：最多 60 项登记检查的状态，不执行检查。
- `get_inspection_findings`：最多 30 条巡检状态及证据状态，不返回原始命令输出。
- `get_asset_route`：资产协议、启用状态及跳板 ID，不返回凭据或备注。
- `read_audit`：审计操作/状态/时间元数据，不含详细正文和来源 IP。
- `search_context`：在受限运维快照中匹配关键词，最多 30 项；不是全文数据库检索。
- `run_readonly_sql`：针对登记数据源的只读 SQL，额外要求当前有效二次验证。复用数据源 SQL guard、只读 driver、超时、并发限制和资产连接链；结果最多 50 行，并按敏感关键词/列名/值做保守过滤。**过滤不能保证识别任意字段里的未知秘密**，授权前应确认查询范围和模型接收方。

工具结果上限 16000 字节，超限返回缩小范围提示，不把超大内容直接注入上下文。每个模型响应最多三项工具，最多三轮工具续答。结果以不可信用户级上下文发送并单独持久化，审计 actor 为 `ai:<conversationId>`。工具数据不成为系统指令。SQL 和运维上下文可能包含业务数据；勾选授权同样适用于已配置的故障回退服务商。

## 写提案与管理员确认

固定 allowlist 包括：

- `propose_forward_reapply`：只对已激活端口转发重推现有配置，审批前校验目标状态与 revision。结果 `dispatched` 仅代表下发，不代表健康检查通过。
- `propose_forward_stop`：以 `forwardId`、`reason` 提议停止已有转发，高风险，必须显式批准并提交新鲜 TOTP。执行使用原子 revision 校验推进停止状态，复用节点/Agent 配置下发；`dispatched` 只表示已保存并尝试下发停止请求，实际资源关闭需匹配新 revision 的节点撤销回执。已停止或正在停止的目标拒绝提案。
- `propose_inspection_run`：以 `checkId`、`reason` 提议执行一次已登记检查，中风险。仅使用登记的 TCP、HTTP、指标或固定 SSH profile，不接受命令、地址或配置覆盖。审批保存检查定义及目标/资产路由摘要，执行在检查租约内复核，定义或路由变化时失败且不生成巡检报告。检查运行状态和最近检查时间变化不使提案失效；禁用定时调度的检查仍可手动执行。结果返回报告 ID 和状态，`succeeded` 表示报告已保存，并不表示目标健康；状态变化可能生成告警和通知。
- changes 模块通过 `RegisterAIAction` 登记的 `propose_service_restart`、`propose_config_change`、`propose_deploy`：仅支持模块明确允许的服务、配置路径和部署流程，不提供任意 shell。实际 SSH 写执行仍由 `AIBOT_ENABLE_SSH_CHANGES=1` 门控，执行前检查资产路由/配置快照。

每项提案保存目标、原因、风险、变更摘要、影响、回滚及验证说明。内部执行 Args 仅留数据库，不进入提案列表、SSE、助手历史或批准/拒绝响应。执行结果单独追加到会话。

审批流程：校验所有权和待审批状态 → CSRF → 有效二次验证 → 请求显式 `confirm:true` → 高风险提案额外验证新鲜 TOTP → 原子占用提案 → 校验目标快照并执行 → 保存状态/审计。TOTP 不记录到审计或提案；恢复码不能替代高风险 TOTP。重复批准/已拒绝提案不能再次执行。服务重启为中风险；配置修改和部署为高风险。模型文本中的“确认”不构成管理员批准。

没有通用 shell 工具。检查定义由执行租约防止并发修改；资产及跳板记录在执行前复核，但当前库存接口没有覆盖整个网络探测期间的路由锁，复核后仍可能发生独立的资产编辑。进程退出时未完成的执行不会自动重新执行，应由管理员结合审计核对实际状态。

## API 合同

所有接口均要求登录；修改类接口要求 CSRF。除上文额外规定的配置/批准/SQL步骤外，普通会话发送不要求二次验证。

| 方法和路径（相对 `/api/v1/ai`） | 行为 |
| --- | --- |
| GET/POST `/providers`、`/models`、`/prompts` | 列表/新增 |
| PATCH/DELETE `/providers/{id}`、`/models/{id}`、`/prompts/{id}` | 修改/删除；引用冲突返回 409 |
| POST `/models/{id}/test` | 连接测试，返回 latencyMs |
| GET `/prompts/{id}/versions` | 不可变历史模板版本 |
| GET/PUT `/task-routes` | 当前管理员默认模型；保存体 `{task,modelId}`，空 modelId 清除配置 |
| GET/POST `/conversations` | 列表/创建；创建体含可选 modelId、task、promptId、title |
| GET `/conversations/{id}` | 会话、消息及当前管理员可见的逐次调用 `calls` 证据 |
| GET/PUT `/conversations/{id}/facts` | 固定事实；保存体 `{content,revision}` |
| GET `/conversations/{id}/summary` | 当前历史语义摘要及覆盖消息 ID |
| POST `/conversations/{id}/summarize` | `{source:"logs"或"history",logs?,consent:true}`；返回摘要及逐次调用证据 |
| POST `/conversations/{id}/messages` | `{content,includeContext,includeFacts,includeSummary}`，返回 SSE |
| POST `/conversations/{id}/cancel` | 取消本会话正在执行的生成 |
| GET `/context` | 受限运维快照 |
| GET `/usage` | 兼容原有按模型聚合的累计用量接口 |
| GET `/usage/daily?days=30` | 按 UTC 日期及已记录实际服务模型汇总；days 为 1–366 整数，默认 30，含今天 |
| GET `/tool-calls` | 当前管理员提案列表 |
| POST `/tool-calls/{id}/approve` | `{confirm:true,otp?:string}` |
| POST `/tool-calls/{id}/reject` | 拒绝待审批提案 |

SSE 事件包括 `budget`、`token`、`fallback`、`tool_call`、`tool_result`、`approval_required`、`done`、`error`。取消与断线会传播到上游；已收到的部分回答及终态通过独立持久化上下文保存。不能把 HTTP 200 当作生成成功，需读取终结事件。输出上限终止会明确标记内容可能不完整。

费用只基于服务商返回的 token 用量及生成时配置的 USD/百万 token 单价快照。未知 usage 或未配置价格保持未知；明确配置为 0 的单价保留为已知零价格。即使无输出、请求失败或未返回 usage，也保存当时已知的服务模型和单价快照。助手消息仍只有工具各轮均返回 usage 才显示完整用量；每日/累计用量以逐次调用证据为准，即使某轮未知，也保留其他轮已知的小计，并显示未知调用数。已有调用证据的父消息不再参与汇总，避免重复计费；没有逐次证据的旧消息及独立摘要记录继续按原记录统计。费用为标价估计，不包含缓存折扣或服务商最终账单调整。

每日用量接口返回 `items`、`days`、`timezone:"UTC"`、`fromDay`、`throughDay`、`currency:"USD"`。窗口按请求记录创建日期计算，含今天和前 days−1 天，排除未来日期；空日期不补零。每天按持久化 `servedModelId` 分组，每次回退尝试归入该次实际选用的模型，普通调用日期按调用前落库时间计算；旧记录未保存实际模型时 `modelId` 为空，不猜测原请求模型。只统计当前管理员的生成请求，失败、取消、中断及空输出也计入 `requests`，审批结果等非生成消息不计入。

每行的 `reportedRequests` 为完整返回输入/输出 token 的请求数，`unknownUsageRequests` 为其余请求；`tokensIn`/`tokensOut` 仅加总已知值，全未知时为 `null`。`estimatedCost` 只累加已持久化成本，**不按当前模型单价重新计算历史**；全未知时为 `null`，已知零费用为 `0`。`pricedRequests`/`unknownCostRequests` 表示有/无已保存成本的请求数，存在未知费用时已知小计只是部分估算。`unknownPriceRequests` 表示没有完整单价快照且没有已保存成本的请求数，与未知用量计数可能重叠。旧记录已有成本但缺少单价元数据时保留原成本，不补写或推测单价。

AI 页用量表支持最近 7/30/90/366 天筛选，展示 UTC 日期、实际模型、请求数、token、已知费用小计及覆盖程度。尚未实现按会话分组账单和模型健康历史。摘要调用按独立记录计入每日用量；当前日期依据记录落库时间，跨 UTC 午夜的摘要可能归入完成日。

## 验证与迁移说明

本地 httptest 覆盖真流式到达、取消、异常 EOF/事件、原生协议、分片工具参数、故障回退和输出后不回退、实际模型费用、工具授权/续答/持久化、审批 CSRF/二次验证/重复执行防护，以及摘录预算和 UTF-8 边界。未执行真实外部 provider 验收。

开发期间 `0010_ai.sql` 曾扩展版本和费用字段，后续 `0016` 迁移按缺失列补齐，并通过历史数据保留和重复启动测试；缺失的历史模型、价格或提示词快照不推测补写。`0017` 增加任务路由、会话事实和摘要表；已有本地验收库升级及浏览器交互已通过。`0018_ai_calls.sql` 增加普通对话逐次调用证据表，不推测补写历史拆分数据。真实生产升级尚未执行。
