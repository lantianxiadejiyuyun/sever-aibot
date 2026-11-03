# 平台 API v1（前端契约）
统一前缀 /api，成功直接返回 JSON 对象或数组；错误为 {"detail":"中文说明"}。同源 fetch credentials:same-origin。写请求 Content-Type: application/json。GET /api/session 返回 {authenticated,csrf}；登录后所有非 GET 请求携带 X-CSRF-Token: csrf。POST /api/login {password} 返回相同结构；POST /api/logout {}。ID 均为字符串，时间为 UTC ISO8601。

| 路由（省略 /api） | 请求与结果 |
|---|---|
| GET/POST /hosts | 列表；创建 {name,address,port:22,environment:"test"}，返回主机对象 |
| PATCH /hosts/{id} | 可改 name,address,port,environment,jump_host_id（跳板须先独立绑定巡检账号和钉住指纹）；改连接目标清除信任 |
| POST /hosts/{id}/fingerprint | {} 只握手，返回 {fingerprint,trusted}；POST /hosts/{id}/trust {fingerprint} 手动核对并固定 |
| GET/POST /credentials | 脱敏列表；导入 {name,purpose:"inspect"或"deploy",private_key,passphrase?} 返回 {id,name,purpose,fingerprint,enabled}；POST /credentials/{id}/disable {} |
| POST /hosts/{id}/bind | {purpose,username,credential_id} 绑定或替换；POST /hosts/{id}/test {purpose:"inspect"} |
| GET/POST /services | GET ?host_id=...；创建 {host_id,name,directory?,compose_service?,image_variable?,health_url?,ports?,config_path?,public_probe?}；PATCH /services/{id} 同样字段但不可换主机 |
| GET /monitor | ?host_id=... 返回 {latest,history,stale}；POST /inspections {host_id,service_id?} 返回后台任务 |
| GET /reports | ?host_id=&service_id= 列表（最新在前，含 items:[{name,status,evidence,source,time}]、changes）；GET /reports/{id} |
| GET /tasks | ?host_id=&service_id= 列表；GET /tasks/{id}；POST /tasks/{id}/confirm {} 仅确认该任务；状态 awaiting_confirmation,queued,running,succeeded,failed,stale,unknown；计划在 plan，对应结果在 result |
| POST /deployments | {host_id,service_id,image,chat_id?,request_id?} 创建 Compose 部署预览，返回任务；回退同一接口提交此前记录的 image |
| GET /tunnels/{kind} | kind=forward,p2p,node,client；?host_id=&service_id=；返回数组，每项含 id、data（上游配置）、host_id、service_id、status |
| POST /tunnels/{kind}/{action} | action=create,update,enable,delete,check；{host_id,service_id?,id?,data:{上游字段},chat_id?,request_id?}，返回任务；check 只读，其他动作须 confirm。节点/客户端 create 同样须确认。上游字段见 tunnel/README.md。 |
| GET/POST /models | 列表；创建 {name,base_url,model,api_key?,capacity:16384,tools:true,is_default:false}，PATCH /models/{id} 可改；POST /models/{id}/test {} |
| GET/POST /chats | 列表 ?q=；创建 {title?,host_id?,service_id?,model_id?,mode:"inspect"或"change"}，返回对象；PATCH /chats/{id} 同字段另支持 exclude_sources:["report","history"]；切换目标或降为 inspect 作废原确认 |
| GET /chats/{id}/messages | 消息列表 {id,role,content,model_id,created_at,task_id?}；POST 同路径 {content,report_id?,request_id?} 返回 {message,task}，task 可为空 |
| GET /chats/{id}/context | {sources:[{name,time,data}],capacity,reserved,mode,...}；DELETE /chats/{id} 仅清理会话 |
| GET/PUT /hosts/{id}/schedule | {interval_minutes:0}，0 关闭；GET /notifications 返回故障与恢复事件列表 |

public_probe 可配置 {protocol:"tcp"或"udp"或"http"或"https",host,port,path?,request?,expect?}，仅管理员预先登记的地址可探测。外部探测需运维显式启用。Compose 使用已存在的目录、配置、服务与镜像环境变量（默认 IMAGE）；ports 为数字数组，health_url 为目标机 HTTP(S) 地址。后端不接收 shell 命令。服务含 expected、verified、state；任务有 id,kind,action,host_id,service_id,status,plan,result,created_at,updated_at。所有表单字段为 snake_case。

运行：见 backend/README.md。代理 /api 到单 worker uvicorn，浏览器通过 HTTPS 访问。
Web Push（手机 PWA 关闭后）：GET /push/key 返回 {enabled,public_key,reason}，public_key 为无填充 Base64URL 编码的 VAPID P-256 公钥，未启用为 null。POST /push/subscriptions 接收 PushSubscription.toJSON() 结构 {endpoint,expirationTime?,keys:{p256dh,auth}}；DELETE 同路径提交 {endpoint}，仅影响当前订阅。成功均返回 {subscribed:true|false}。同样需要管理员会话和 CSRF。通知 payload 为 {id,title,body,url:"/",tag,host_id,service_id,report_id}，点击打开同源面板；不含原始报告、凭据或订阅地址。服务端设置 AIBOT_VAPID_SUBJECT=mailto:管理员地址（或 HTTPS 联系地址）启用，未设置时仅保留平台通知列表。
