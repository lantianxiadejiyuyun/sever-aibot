<script setup lang="ts">
import { onMounted, onBeforeUnmount, ref } from 'vue'
import PageHeader from '@/components/ui/PageHeader.vue'
import StepUpDialog from '@/components/ui/StepUpDialog.vue'
import { request, getCsrfToken, errorMessage, ApiError } from '@/api/client'

type Config = { id: string; name: string; [key: string]: string | number | boolean | null }
type Conversation = { id: string; title: string; modelId: string; promptId: string }
type Message = { id?: number; role: string; content: string; status: string }
type AICall = { id: number; messageId: number; attempt: number; toolRound: number; servedModelId: string; providerModel: string; status: string; finishReason: string; tokensIn: number | null; tokensOut: number | null; cost: number | null }
type ConversationDetail = { messages: Message[]; calls: AICall[] }
const calls = ref<AICall[]>([])
async function refreshConversation(id: string) {
  const result = await request<ConversationDetail>(`/ai/conversations/${id}`)
  if (!alive || selected.value !== id) return
  messages.value = result.messages; calls.value = result.calls || []; live.value = ''
}
function callStatus(status: string) {
  return ({ completed: '已完成', error: '失败', cancelled: '已取消', interrupted: '结果未确认' } as Record<string, string>)[status] || status
}
type Proposal = { id: string; conversationId: string; target: string; reason: string; risk: string; diff: string; impact: string; rollback: string; verification: string; status: string; result: string }
const taskOptions = [{id:'chat',name:'普通对话'},{id:'inspection',name:'巡检分析'},{id:'log_summary',name:'日志归纳'},{id:'change',name:'变更讨论'}]
const task = ref('chat'), taskRoutes = ref<Record<string,string>>({}), includeFacts = ref(false)
const facts = ref({content:'',revision:0})
const summary = ref({content:'',throughMessage:0}), includeSummary=ref(false), summaryLogs=ref(''), summaryConsent=ref(false)
async function loadSummary(id:string) {const result=await request<{content:string;throughMessage:number}>(`/ai/conversations/${id}/summary`);if(selected.value===id)summary.value=result}
async function summarize(source:'history'|'logs') {
 if(!selected.value||generating.value||!summaryConsent.value)return
 const id=selected.value;generating.value=true;error.value='';controller=new AbortController()
 try {
  const response=await fetch(`/api/v1/ai/conversations/${id}/summarize`,{method:'POST',credentials:'same-origin',signal:controller.signal,headers:{'Content-Type':'application/json','X-CSRF-Token':getCsrfToken()},body:JSON.stringify({source,logs:source==='logs'?summaryLogs.value:'',consent:true})})
  const result=await response.json();if(!response.ok)throw new Error(result.error?.message||'归纳失败')
  info.value=`归纳完成，共 ${result.calls.length} 次调用；结果有损，请核对原始证据。`;summaryLogs.value='';await loadSummary(id)
 } catch (e) {
   if (alive) error.value = errorMessage(e)
 } finally {
   generating.value = false; controller = null
   if (alive) {
     try {
       await refreshConversation(id)
       await load()
     } catch (e) { error.value = errorMessage(e) }
   }
 }
}
async function loadRoutes() { const result=await request<{items:{task:string;modelId:string}[]}>('/ai/task-routes'); taskRoutes.value=Object.fromEntries(result.items.map(x=>[x.task,x.modelId])) }
async function saveRoute(key:string) { await action(async()=>{await request('/ai/task-routes',{method:'PUT',body:{task:key,modelId:taskRoutes.value[key]||''}});info.value='默认模型已保存，只影响新建会话'}) }
async function loadFacts(id:string) { const result=await request<{content:string;revision:number}>(`/ai/conversations/${id}/facts`);if(selected.value===id)facts.value=result }
async function saveFacts() { const id=selected.value;await action(async()=>{const result=await request<{content:string;revision:number}>(`/ai/conversations/${id}/facts`,{method:'PUT',body:facts.value});if(selected.value===id)facts.value=result;info.value='固定事实已保存；发送消息时可选择附加'}) }
const tab = ref('chat'), error = ref(''), info = ref(''), busy = ref(false), generating = ref(false)
const providers = ref<Config[]>([]), models = ref<Config[]>([]), prompts = ref<Config[]>([]), conversations = ref<Conversation[]>([]), proposals = ref<Proposal[]>([])
const credentials = ref<Array<{ id: string; name: string; kind: string }>>([])
const selectedModel = ref(''), selectedPrompt = ref(''), selected = ref(''), messages = ref<Message[]>([]), content = ref(''), includeContext = ref(false), budget = ref(''), live = ref('')
type UsageDay = { day: string; modelId: string; requests: number; reportedRequests: number; unknownUsageRequests: number; tokensIn: number | null; tokensOut: number | null; estimatedCost: number | null; pricedRequests: number; unknownCostRequests: number; unknownPriceRequests: number }
const usage = ref<UsageDay[]>([]), usageDays = ref(30), usageFrom = ref(''), usageThrough = ref('')
async function loadUsage() {
  const result = await request<{ items: UsageDay[]; fromDay: string; throughDay: string }>(`/ai/usage/daily?days=${usageDays.value}`)
  if (!alive) return
  usage.value = result.items; usageFrom.value = result.fromDay; usageThrough.value = result.throughDay
}
const kind = ref('providers'), editing = ref(''), form = ref<Record<string, string | number | boolean | null>>({})
const stepUp = ref(false)
const versions = ref<Config[]>([])
async function showVersions(row: Config) { await action(async () => { versions.value = (await request<{ items: Config[] }>(`/ai/prompts/${row.id}/versions`)).items }) }
async function restore(row: Config) { await action(async () => { await request(`/ai/prompts/${row.id}`, { method: 'PATCH', body: { name: row.name, systemPrompt: row.systemPrompt } }); await load(); versions.value = []; info.value = '已将历史模板复制为新版本，现有会话保持原版本' }) }
let pending: (() => Promise<void>) | null = null
let controller: AbortController | null = null
let alive = true
async function action(fn: () => Promise<void>) {
  error.value = ''; busy.value = true
  try { await fn() } catch (e) { if (e instanceof ApiError && e.code === 'step_up_required') { pending = fn; stepUp.value = true } else error.value = errorMessage(e) }
  finally { busy.value = false }
}
async function verified() { stepUp.value = false; const fn = pending; pending = null; if (fn) await action(fn) }
async function load() {
  const [p, m, t, c, a, cr] = await Promise.all([
    request<{ items: Config[] }>('/ai/providers'), request<{ items: Config[] }>('/ai/models'), request<{ items: Config[] }>('/ai/prompts'),
    request<{ items: Conversation[] }>('/ai/conversations'), request<{ items: Proposal[] }>('/ai/tool-calls'),
    request<{ items: typeof credentials.value }>('/credentials/'), loadUsage(), loadRoutes(),
  ])
  if (!alive) return
  providers.value = p.items; models.value = m.items; prompts.value = t.items; conversations.value = c.items; proposals.value = a.items; credentials.value = cr.items.filter(x => x.kind === 'api_token')
  // An empty selection explicitly uses the task's saved default.
}
function edit(k: string, value?: Config) {
  kind.value = k; editing.value = value?.id || ''
  form.value = value ? { ...value } : k === 'providers' ? { name: '', kind: 'openai_compatible', baseUrl: 'http://localhost:8000/v1', credentialId: '', enabled: true } : k === 'models' ? { name: '', providerId: providers.value[0]?.id || '', modelId: '', contextWindow: 32768, maxOutput: 2048, supportsTools: false, enabled: true, fallbackModelId: '', inputPrice: '', outputPrice: '' } : { name: '', systemPrompt: '' }
}
async function save() { await action(async () => { await request(`/ai/${kind.value}${editing.value ? '/' + editing.value : ''}`, { method: editing.value ? 'PATCH' : 'POST', body: kind.value === 'models' ? { ...form.value, inputPrice: form.value.inputPrice === '' || form.value.inputPrice == null ? null : Number(form.value.inputPrice), outputPrice: form.value.outputPrice === '' || form.value.outputPrice == null ? null : Number(form.value.outputPrice) } : form.value }); await load(); edit(kind.value); info.value = '配置已保存' }) }
async function remove(k: string, row: Config) { if (!window.confirm(`删除 ${row.name}？被引用的配置无法删除。`)) return; await action(async () => { await request(`/ai/${k}/${row.id}`, { method: 'DELETE' }); await load() }) }
async function test(row: Config) { await action(async () => { const result = await request<{ latencyMs: number }>(`/ai/models/${row.id}/test`, { body: {} }); info.value = `连接测试通过：${result.latencyMs} ms` }) }
async function open(id: string) {
  if (generating.value) return
  await action(async () => {
    selected.value = id; messages.value = []; calls.value = []; live.value = ''; budget.value = ''
    includeFacts.value=false; includeSummary.value=false; summaryConsent.value=false; summaryLogs.value=''
    facts.value={content:'',revision:0}; summary.value={content:'',throughMessage:0}
    await Promise.all([refreshConversation(id), loadFacts(id), loadSummary(id)])
  })
}
async function create() { await action(async () => { const result = await request<Conversation>('/ai/conversations', { body: { modelId: selectedModel.value, task:task.value, promptId: selectedPrompt.value, title: taskOptions.find(x=>x.id===task.value)?.name || '运维对话' } }); selected.value = result.id; facts.value={content:'',revision:0}; includeFacts.value=false; includeSummary.value=false; summaryConsent.value=false; summaryLogs.value=''; summary.value={content:'',throughMessage:0}; messages.value = []; calls.value = []; live.value = ''; await load() }) }
async function cancel() { if (selected.value) await action(async () => { await request(`/ai/conversations/${selected.value}/cancel`, { body: {} }) }) }
async function send() {
  if (!selected.value || !content.value.trim() || generating.value) return
  const text = content.value, id = selected.value; generating.value = true; error.value = ''; live.value = ''; controller = new AbortController(); let terminal = false
  messages.value.push({ role: 'user', content: text, status: 'completed' }); content.value = ''
  try {
    const response = await fetch(`/api/v1/ai/conversations/${id}/messages`, { method: 'POST', credentials: 'same-origin', signal: controller.signal, headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': getCsrfToken() }, body: JSON.stringify({ content: text, includeContext: includeContext.value, includeFacts: includeFacts.value, includeSummary: includeSummary.value }) })
    if (!response.ok) { const payload = await response.json(); throw new Error(payload.error?.message || '生成请求失败') }
    if (!response.body) throw new Error('浏览器不支持流式响应')
    const reader = response.body.getReader(), decoder = new TextDecoder(); let buffer = ''
    for (;;) {
      const { done, value } = await reader.read(); buffer += decoder.decode(value, { stream: !done }); let end: number
      while ((end = buffer.indexOf('\n\n')) !== -1) {
        const event = buffer.slice(0, end); buffer = buffer.slice(end + 2)
        const name = event.split('\n').find(x => x.startsWith('event:'))?.slice(6).trim()
        const raw = event.split('\n').filter(x => x.startsWith('data:')).map(x => x.slice(5).trimStart()).join('\n')
        if (!raw) continue
        const data = JSON.parse(raw)
        if (name === 'token') live.value += data.content
        if (name === 'budget') budget.value = `保守估算输入 ${data.estimatedInput} / 上下文 ${data.contextWindow}；预留输出 ${data.outputReserve}；裁剪旧消息 ${data.droppedMessages} 条；${data.summaryMethod ? `保留有损历史摘录 ${data.summaryBytes} 字节（非语义摘要）` : '整轮裁剪，无摘要'}`
        if (name === 'approval_required') proposals.value.unshift(data)
        if (name === 'fallback') info.value = `服务商暂时不可用，已回退到 ${models.value.find(x => x.id === data.toModelId)?.name || data.toModelId}`
        if (name === 'error') { terminal = true; error.value = data.message }
        if (name === 'done') { terminal = true; info.value = data.finishReason === 'length' ? '已达到输出上限，内容可能不完整' : '生成完成' }
      }
      if (done) break
    }
    if (!terminal) throw new Error('连接提前结束，生成状态未确认')
  } catch (e) { if (alive) error.value = errorMessage(e) }
  finally {
    generating.value = false; controller = null
    if (alive) { try { await refreshConversation(id); await load() } catch (e) { error.value = errorMessage(e) } }
  }
}
async function decide(p: Proposal, approve: boolean) {
  if (approve && !window.confirm(`确认执行提案，目标 ${p.target}？\n${p.impact}\n${p.verification}`)) return
  const otp = approve && p.risk === 'high' ? window.prompt('高风险操作需要新的 6 位 TOTP 验证码（需先在设置启用）') : ''
  if (otp === null) return
  await action(async () => { await request(`/ai/tool-calls/${p.id}/${approve ? 'approve' : 'reject'}`, { body: approve ? { confirm: true, otp } : {} }); await load(); if (selected.value === p.conversationId) await open(selected.value) })
}
onMounted(() => { edit('providers'); void action(load) })
onBeforeUnmount(() => { alive = false; controller?.abort() })
</script>

<template>
  <PageHeader eyebrow="OPERATIONS / AI" title="AI 运维" />
  <div class="stack">
    <p class="notice">支持 OpenAI-compatible（含 vLLM / Ollama）、原生 Anthropic 和 Gemini 文本接口。附加上下文会发送到所选模型及其已配置回退服务商；写工具仅生成 allowlist 审批提案，管理员确认前不执行。</p>
    <p v-if="error" class="notice notice-danger" role="alert">{{ error }}</p>
    <p v-if="info" class="notice" role="status">{{ info }}</p>
    <div class="tabs"><button v-for="t in [{ id: 'chat', label: '对话与审批' }, { id: 'config', label: '服务商 / 模型 / 模板' }, { id: 'usage', label: '用量' }]" :key="t.id" class="btn" :class="{ 'btn-primary': tab === t.id }" @click="tab = t.id">{{ t.label }}</button></div>
    <div v-if="tab === 'chat'" class="ai-layout">
      <section class="panel"><div class="panel-body stack">
        <label>任务<select v-model="task" class="input"><option v-for="t in taskOptions" :key="t.id" :value="t.id">{{ t.name }}</option></select></label>
        <label>模型<select v-model="selectedModel" class="input"><option value="">使用任务默认模型</option><option v-for="m in models.filter(x => x.enabled)" :key="m.id" :value="m.id">{{ m.name }}</option></select></label>
        <label>提示词模板<select v-model="selectedPrompt" class="input"><option value="">默认只读运维助手</option><option v-for="p in prompts" :key="p.id" :value="p.id">{{ p.name }}</option></select></label>
        <button class="btn btn-primary" :disabled="busy || generating || (!selectedModel && !taskRoutes[task])" @click="create">新建会话</button>
        <button v-for="c in conversations" :key="c.id" class="btn session-row" :class="{ 'btn-primary': selected === c.id }" :disabled="generating || busy" @click="open(c.id)">{{ c.title }} · {{ c.id.slice(-6) }}</button>
      </div></section>
      <section class="panel"><div class="panel-body stack">
        <p v-if="selected" class="muted">当前会话模型：{{ models.find(m=>m.id===conversations.find(c=>c.id===selected)?.modelId)?.name || conversations.find(c=>c.id===selected)?.modelId }}</p>
        <p v-if="!selected" class="muted">先配置服务商和模型，再创建会话。历史会话绑定创建时选择的模型与模板。</p>
        <details v-if="selected"><summary>本会话固定事实</summary><label>人工确认的背景事实<textarea v-model="facts.content" class="input" rows="4" maxlength="8000" :disabled="generating || busy" /></label><p class="muted">只保存在本会话，不从模型输出自动写入。清空后保存可删除当前事实；已发送的历史消息仍保留当时内容。</p><button class="btn" :disabled="busy || generating" @click="saveFacts">保存事实</button></details>
        <label v-if="selected"><input v-model="includeFacts" type="checkbox" :disabled="generating" /> 本轮附加已保存事实，允许发送到会话模型及其回退服务商</label>
        <details v-if="selected"><summary>语义摘要与长日志归纳</summary><p class="muted">向当前会话模型发送历史或粘贴的日志，分段归纳后合并。最多 32 次调用、3 分钟、256 KiB 输入（小上下文模型限额更低）；不会调用工具或自动回退。结果可能遗漏事实，不能替代原始证据。</p><label><input v-model="summaryConsent" type="checkbox" :disabled="generating" /> 确认将所选内容发送至当前会话模型并承担多次调用费用</label><button class="btn" :disabled="generating || busy || !summaryConsent" @click="summarize('history')">生成历史语义摘要</button><label>待归纳日志<textarea v-model="summaryLogs" class="input" rows="5" :disabled="generating" placeholder="请先移除口令、密钥与不应发送的内容" /></label><button class="btn" :disabled="generating || busy || !summaryConsent || !summaryLogs.trim()" @click="summarize('logs')">分段归纳日志</button><article v-if="summary.content" class="message"><strong>已保存的历史摘要 · 有损</strong><pre>{{ summary.content }}</pre></article></details>
        <label v-if="summary.content"><input v-model="includeSummary" type="checkbox" :disabled="generating" /> 本轮用已保存语义摘要替代其覆盖的历史，后续消息仍保留</label>
        <details v-if="selected" :key="selected" class="message">
          <summary>普通对话逐次调用证据 · {{ calls.length }} 次</summary>
          <p class="muted">每次回退尝试和工具续答分别记录；失败或取消也可能产生费用。未知用量与费用不会当作零。摘要调用保留在下方独立记录中。</p>
          <div v-if="calls.length" class="table-scroll"><table>
            <thead><tr><th scope="col">消息 / 尝试</th><th scope="col">实际选用模型</th><th scope="col">工具轮次</th><th scope="col">结果</th><th scope="col">输入 / 输出 token</th><th scope="col">估算费用 USD</th></tr></thead>
            <tbody><tr v-for="call in calls" :key="call.id">
              <td>#{{ call.messageId }} / 第 {{ call.attempt }} 次</td>
              <td>{{ models.find(m => m.id === call.servedModelId)?.name || call.servedModelId || '未记录模型' }}<small class="usage-detail muted">{{ call.servedModelId }} · {{ call.providerModel }}</small></td>
              <td>{{ call.toolRound === 0 ? '初次请求 / 回退' : `第 ${call.toolRound} 轮续答` }}</td>
              <td>{{ callStatus(call.status) }}<small v-if="call.finishReason" class="usage-detail muted">{{ call.finishReason }}</small></td>
              <td>{{ call.tokensIn == null ? '未知' : call.tokensIn.toLocaleString() }} / {{ call.tokensOut == null ? '未知' : call.tokensOut.toLocaleString() }}</td>
              <td>{{ call.cost == null ? '未知' : call.cost.toFixed(6) }}</td>
            </tr></tbody>
          </table></div>
          <p v-else class="muted">暂无逐次调用证据；旧会话消息不会补写推测数据。</p>
        </details>
        <p v-if="budget" class="muted">{{ budget }}</p>
        <template v-for="(m, index) in messages" :key="m.id || index">
          <details v-if="m.status === 'summary_call'" class="message"><summary>摘要调用记录 · 用量与执行结果</summary><pre>{{ m.content }}</pre></details>
          <article v-else class="message"><strong>{{ m.role === 'user' ? '你' : m.role === 'tool' ? '只读工具' : 'AI' }}</strong><span class="muted"> · {{ m.status }}</span><pre>{{ m.content }}</pre></article>
        </template>
        <article v-if="generating" class="message" aria-live="polite"><strong>AI · 生成中</strong><pre>{{ live || '等待服务商响应…' }}</pre></article>
        <form class="stack" @submit.prevent="send"><label><input v-model="includeContext" type="checkbox" :disabled="generating" /> 授权本轮运维上下文与只读工具（含登记数据源 SQL，需有效二次验证；查询过滤不能保证识别任意敏感值，数据会发送至所选模型及回退服务商）</label><label>消息<textarea v-model="content" class="input" rows="4" :disabled="!selected || generating" placeholder="例如：分析当前设备状态；需要重推时说明目标转发 ID" /></label><div class="tabs"><button class="btn btn-primary" :disabled="!selected || generating || !content.trim()">发送</button><button v-if="generating" type="button" class="btn" @click="cancel">取消生成</button></div></form>
        <article v-for="p in proposals.filter(x => x.conversationId === selected)" :key="p.id" class="message"><strong>审批提案 · {{ p.risk }} · {{ p.status }}</strong><p>目标：{{ p.target }}</p><p>{{ p.reason }}</p><p>变更：{{ p.diff }}</p><p>影响：{{ p.impact }}</p><p>回滚：{{ p.rollback }}</p><p>验证：{{ p.verification }}</p><p v-if="p.result">结果：{{ p.result }}</p><div v-if="p.status === 'proposed'" class="tabs"><button class="btn btn-primary" :disabled="busy || generating" @click="decide(p, true)">确认并验证身份</button><button class="btn" :disabled="busy" @click="decide(p, false)">拒绝</button></div></article>
      </div></section>
    </div>
    <section v-if="tab === 'config'" class="panel"><div class="panel-body stack">
      <details><summary>按任务设置默认模型</summary><p class="muted">仅用于未明确选择模型的新会话，现有会话不会自动切换服务商。保存需要二次验证。</p><div v-for="t in taskOptions" :key="t.id" class="config-row"><label>{{ t.name }}<select v-model="taskRoutes[t.id]" class="input"><option value="">不设置默认</option><option v-for="m in models.filter(x=>x.enabled)" :key="m.id" :value="m.id">{{ m.name }}</option></select></label><button class="btn" :disabled="busy" @click="saveRoute(t.id)">保存 {{ t.name }}</button></div></details>
      <div class="tabs"><button v-for="k in [{ id: 'providers', label: '服务商' }, { id: 'models', label: '模型' }, { id: 'prompts', label: '模板' }]" :key="k.id" class="btn" @click="edit(k.id)">{{ k.label }}</button></div>
      <div v-for="row in (kind === 'providers' ? providers : kind === 'models' ? models : prompts)" :key="row.id" class="config-row"><span>{{ row.name }} <small v-if="kind === 'prompts'">v{{ row.version }}</small></span><div class="tabs"><button class="btn" @click="edit(kind, row)">编辑</button><button v-if="kind === 'models'" class="btn" :disabled="busy" @click="test(row)">连接测试</button><button v-if="kind === 'prompts'" class="btn" :disabled="busy" @click="showVersions(row)">历史版本</button><button class="btn" :disabled="busy" @click="remove(kind, row)">删除</button></div></div>
      <details v-for="v in versions" :key="String(v.version)" class="message"><summary>{{ v.name }} · v{{ v.version }}</summary><pre>{{ v.systemPrompt }}</pre><button class="btn" :disabled="busy" @click="restore(v)">复制为新版本</button></details>
      <form class="stack" @submit.prevent="save"><h2 class="panel-title">{{ editing ? '编辑' : '新增' }}{{ kind === 'providers' ? '服务商' : kind === 'models' ? '模型' : '模板' }}</h2>
        <label>名称<input v-model="form.name" class="input" required maxlength="120" /></label>
        <template v-if="kind === 'providers'"><label>协议<select v-model="form.kind" class="input"><option value="openai_compatible">OpenAI-compatible / vLLM / Ollama</option><option value="anthropic">Anthropic Messages（原生）</option><option value="gemini">Gemini（原生）</option></select></label><label>API base URL<input v-model="form.baseUrl" class="input" required type="url" /></label><p class="muted">OpenAI: https://api.openai.com/v1；Ollama: http://ollama:11434/v1；Anthropic: https://api.anthropic.com；Gemini: https://generativelanguage.googleapis.com/v1beta。端点由控制面访问；连接测试发送最小请求。</p><label>API token 凭据<select v-model="form.credentialId" class="input"><option value="">无需鉴权（本地服务）</option><option v-for="c in credentials" :key="c.id" :value="c.id">{{ c.name }}</option></select></label><p class="muted">请先在凭据页创建 API token；此页不读取或展示密钥。</p></template>
        <template v-if="kind === 'models'"><label>服务商<select v-model="form.providerId" class="input" required><option v-for="p in providers" :key="p.id" :value="p.id">{{ p.name }}</option></select></label><label>服务端模型 ID<input v-model="form.modelId" class="input" required /></label><label>上下文窗口<input v-model.number="form.contextWindow" class="input" type="number" min="256" max="2000000" required /></label><label>最大输出 token<input v-model.number="form.maxOutput" class="input" type="number" min="1" required /></label><label><input v-model="form.supportsTools" type="checkbox" /> 此模型支持工具调用（启用只读工具和审批提案）</label><label>故障回退模型<select v-model="form.fallbackModelId" class="input"><option value="">不回退</option><option v-for="m in models.filter(x => x.id !== editing)" :key="m.id" :value="m.id">{{ m.name }}</option></select></label><p class="muted">仅在尚未输出内容时，针对网络故障、408、429 或 5xx 回退；最多 3 跳，各模型重新计算上下文预算。配置跨服务商回退表示允许向该服务商发送同一上下文。</p><label>输入价格（USD / 百万 token，留空表示未知）<input v-model="form.inputPrice" class="input" type="number" min="0" step="any" /></label><label>输出价格（USD / 百万 token）<input v-model="form.outputPrice" class="input" type="number" min="0" step="any" /></label></template>
        <template v-if="kind === 'prompts'"><label>系统提示词<textarea :value="String(form.systemPrompt || '')" class="input" rows="7" @input="form.systemPrompt = ($event.target as HTMLTextAreaElement).value" /></label><p class="muted">静态模板，保存时保留历史版本；恢复会创建新版本。会话固定使用创建时的模板快照，更新模板不会改变现有会话。当前不做变量替换。</p></template>
        <label v-if="kind !== 'prompts'"><input v-model="form.enabled" type="checkbox" /> 启用</label><div class="tabs"><button class="btn btn-primary" :disabled="busy">保存（需要二次验证）</button><button type="button" class="btn" @click="edit(kind)">清空表单</button></div>
      </form>
    </div></section>
    <section v-if="tab === 'usage'" class="panel"><div class="panel-body stack">
      <div class="tabs"><label>统计范围（含今天，UTC）<select v-model.number="usageDays" class="input" :disabled="busy" @change="action(loadUsage)"><option :value="7">最近 7 天</option><option :value="30">最近 30 天</option><option :value="90">最近 90 天</option><option :value="366">最近 366 天</option></select></label><button class="btn" :disabled="busy" @click="action(loadUsage)">刷新用量</button></div>
      <p v-if="usageFrom" class="muted">{{ usageFrom }} 至 {{ usageThrough }} · UTC · 按调用记录日期和实际选用模型分组</p>
      <p>费用使用生成时保存的单价与成本快照，修改模型价格不会重算历史。未配置价格或未返回 usage 的请求费用未知；明确配置的零价格显示为 0。估算不含缓存折扣，不等于服务商账单。</p>
      <p class="muted">普通对话的每次回退尝试和工具续答分别计数，归入该次选用的模型；无输出、失败、取消和中断也计入。token 和费用仅汇总已知部分，存在未知调用时只是部分小计。已有逐次证据的父消息不重复统计；摘要及旧消息继续按各自记录计入，历史未知模型单独显示。普通调用按调用前记录日期归组，摘要按落库日期归组。</p>
      <div class="table-scroll"><table><thead><tr><th scope="col">日期（UTC）</th><th scope="col">实际模型</th><th scope="col">请求数</th><th scope="col">用量报告</th><th scope="col">输入 token</th><th scope="col">输出 token</th><th scope="col">已知费用小计 USD</th><th scope="col">费用覆盖</th></tr></thead><tbody>
        <tr v-for="u in usage" :key="`${u.day}/${u.modelId}`"><td>{{ u.day }}</td><td>{{ u.modelId ? (models.find(m => m.id === u.modelId)?.name || u.modelId) : '未记录实际模型' }}<small v-if="u.modelId" class="usage-detail muted">{{ u.modelId }}</small></td><td>{{ u.requests }}</td><td>{{ u.reportedRequests }} / {{ u.requests }}<small v-if="u.unknownUsageRequests" class="usage-detail muted">{{ u.unknownUsageRequests }} 次用量未知</small></td><td>{{ u.tokensIn == null ? '未知' : u.tokensIn.toLocaleString() }}</td><td>{{ u.tokensOut == null ? '未知' : u.tokensOut.toLocaleString() }}</td><td>{{ u.estimatedCost == null ? '未知' : u.estimatedCost.toFixed(6) }}<small v-if="u.estimatedCost != null && u.unknownCostRequests" class="usage-detail muted">部分估算</small></td><td>{{ u.pricedRequests }} / {{ u.requests }}<small v-if="u.unknownCostRequests" class="usage-detail muted">{{ u.unknownCostRequests }} 次费用未知</small><small v-if="u.unknownPriceRequests" class="usage-detail muted">{{ u.unknownPriceRequests }} 次无完整价格快照</small></td></tr>
        <tr v-if="!usage.length"><td colspan="8">所选 UTC 日期范围内暂无生成记录</td></tr>
      </tbody></table></div>
    </div></section>
  </div>
  <StepUpDialog v-if="stepUp" title="验证管理员身份" @verified="verified" @close="stepUp = false; pending = null" />
</template>

<style scoped>
.ai-layout { display: grid; grid-template-columns: 240px minmax(0, 1fr); gap: 16px; align-items: start; }
.tabs { display: flex; flex-wrap: wrap; gap: 8px; }
.stack { display: grid; gap: 14px; }
label { display: grid; gap: 7px; }
label:has(input[type=checkbox]) { display: block; }
.message { padding: 14px; border: 1px solid var(--border); border-radius: 8px; overflow-wrap: anywhere; }
.message pre { white-space: pre-wrap; font: inherit; line-height: 1.65; margin: 10px 0 0; }
.session-row { justify-content: flex-start; overflow-wrap: anywhere; }
.config-row { display: flex; justify-content: space-between; gap: 12px; flex-wrap: wrap; align-items: center; }
.usage-detail { display: block; margin-top: 4px; }
.table-scroll { overflow-x: auto; } table { width: 100%; text-align: left; } td, th { padding: 10px; }
@media (max-width: 780px) { .ai-layout { grid-template-columns: 1fr; } }
</style>
