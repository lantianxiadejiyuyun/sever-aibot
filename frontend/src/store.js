import { reactive, computed } from 'vue'
export const s = reactive({
  auth: null,
  csrf: '',
  error: '',
  notice: '',
  busy: {},
  hosts: [],
  services: [],
  models: [],
  chats: [],
  creds: [],
  tasks: [],
  messages: [],
  host: '',
  service: '',
  model: '',
  mode: 'inspect',
  chat: '',
  view: 'chat',
  dialog: '',
  mobile: 'hosts',
  draft: '',
  reportRef: null,
  version: 0,
})
export const host = computed(() => s.hosts.find((x) => x.id === s.host))
export const service = computed(() =>
  s.services.find((x) => x.id === s.service),
)
export const chat = computed(() => s.chats.find((x) => x.id === s.chat))
export const status = {
  awaiting_confirmation: '等待确认',
  queued: '已排队',
  running: '执行中',
  succeeded: '任务完成',
  failed: '失败',
  stale: '已失效',
  unknown: '待核实',
  saved: '配置已保存',
  pending_apply: '待应用',
  revoked: '已撤销',
  pending: '待应用',
  applied: '配置已应用',
  online: '在线',
  offline: '离线',
  unverified: '未验证',
  normal: '正常',
  ok: '正常',
  abnormal: '异常',
  warning: '需关注',
  disabled: '已停用',
  pending_revoke: '待撤销',
}
export const label = (v) => status[v] || v || '未确认'
export const when = (v) =>
  v
    ? new Intl.DateTimeFormat('zh-CN', {
        month: '2-digit',
        day: '2-digit',
        hour: '2-digit',
        minute: '2-digit',
        hour12: false,
      }).format(new Date(v))
    : '未记录'
export const requestId = () =>
  Array.from(crypto.getRandomValues(new Uint8Array(16)), (b) =>
    b.toString(16).padStart(2, '0'),
  ).join('')
export const enc = encodeURIComponent
export const query = (o) => {
  const vals = Object.entries(o).filter(([, v]) => v !== '' && v != null)
  return vals.length ? '?' + new URLSearchParams(vals) : ''
}
export const scope = () => ({ host_id: s.host, service_id: s.service || null })
export async function api(path, method = 'GET', data) {
  const headers = { Accept: 'application/json' }
  if (method !== 'GET') {
    headers['Content-Type'] = 'application/json'
    if (s.csrf) headers['X-CSRF-Token'] = s.csrf
  }
  let r
  try {
    r = await fetch('/api' + path, {
      method,
      headers,
      credentials: 'same-origin',
      cache: 'no-store',
      body: data === undefined ? undefined : JSON.stringify(data),
    })
  } catch {
    throw Error('无法连接控制服务，请检查网络后重试。')
  }
  const j = await r.json().catch(() => null)
  if (!r.ok || j === null) {
    if (r.status === 401 && s.auth) reset()
    throw Error(
      typeof j?.detail === 'string'
        ? j.detail
        : '请求失败（' + r.status + '），请检查控制服务。',
    )
  }
  return j
}
export async function run(key, fn) {
  if (s.busy[key]) return
  s.busy[key] = true
  s.error = ''
  try {
    return await fn()
  } catch (e) {
    s.error = e.message || '操作失败'
  } finally {
    s.busy[key] = false
  }
}
export function reset() {
  Object.assign(s, {
    auth: false,
    csrf: '',
    hosts: [],
    services: [],
    models: [],
    chats: [],
    creds: [],
    tasks: [],
    messages: [],
    host: '',
    service: '',
    model: '',
    chat: '',
    draft: '',
    reportRef: null,
    mode: 'inspect',
    view: 'chat',
    dialog: '',
    mobile: 'hosts',
    notice: '',
    version: s.version + 1,
  })
}
export async function session() {
  const j = await api('/session')
  s.csrf = j.csrf
  s.auth = j.authenticated
  if (s.auth) await init()
}
export async function login(password) {
  const j = await api('/login', 'POST', { password })
  s.csrf = j.csrf
  s.auth = j.authenticated
  if (s.auth) await init()
}
export async function catalog() {
  const [hosts, models, chats, creds] = await Promise.all(
    ['/hosts', '/models', '/chats', '/credentials'].map((p) => api(p)),
  )
  if (s.auth) Object.assign(s, { hosts, models, chats, creds })
}
async function init() {
  await catalog()
  if (s.chats.length) await openChat(s.chats[0])
  else {
    s.host = s.hosts[0]?.id || ''
    s.model = (s.models.find((x) => x.is_default) || s.models[0])?.id || ''
    await loadTarget()
  }
  s.mobile = 'hosts'
}
export async function syncTasks() {
  const stamp = s.version
  if (!s.host) {
    s.tasks = []
    return
  }
  const t = await api('/tasks' + query(scope()))
  if (s.auth && stamp === s.version) s.tasks = t
}
export async function loadTarget() {
  const token = ++s.version
  s.services = []
  s.tasks = []
  if (!s.host) return
  const [ss, tt] = await Promise.all([
    api('/services' + query({ host_id: s.host })),
    api('/tasks' + query(scope())),
  ])
  if (token !== s.version || !s.auth) return
  s.services = ss
  s.tasks = tt
}
export async function patchChat(p) {
  if (!s.chat) return
  const obj = await api('/chats/' + enc(s.chat), 'PATCH', p)
  s.chats = s.chats.map((x) => (x.id === obj.id ? obj : x))
  return obj
}
export async function changeTarget(h, v = '') {
  if (h === s.host && v === s.service) return
  await patchChat({ host_id: h || null, service_id: v || null })
  s.host = h
  s.service = v
  s.draft = ''
  s.reportRef = null
  s.dialog = ''
  await loadTarget()
}
export async function openChat(c) {
  Object.assign(s, {
    chat: c.id,
    host: c.host_id || '',
    service: c.service_id || '',
    model:
      c.model_id ||
      (s.models.find((x) => x.is_default) || s.models[0])?.id ||
      '',
    mode: c.mode || 'inspect',
    messages: [],
    draft: '',
    reportRef: null,
    dialog: '',
    view: 'chat',
    mobile: 'desk',
  })
  const id = c.id
  const [ms] = await Promise.all([
    api('/chats/' + enc(id) + '/messages'),
    loadTarget(),
  ])
  if (s.chat === id) s.messages = ms
}
export async function newChat(title) {
  const c = await api('/chats', 'POST', {
    ...scope(),
    host_id: s.host || null,
    model_id: s.model || null,
    title: title || '新会话',
    mode: s.mode,
  })
  s.chats = [c, ...s.chats]
  await openChat(c)
  return c
}
export async function setChatOption(key, val) {
  await patchChat({ [key]: val })
  s[key === 'model_id' ? 'model' : key] = val
  if (key === 'mode') await syncTasks()
}
export async function send() {
  const content = s.draft.trim(),
    report_id = s.reportRef?.id
  if (!content) return
  if (!s.host || !s.model) throw Error('请先选择服务器和模型。')
  if (!s.chat) await newChat(content.slice(0, 28))
  else {
    // The same conversation can change targets on another device.
    const previous = chat.value
    const chats = await api('/chats')
    const current = chats.find((c) => c.id === s.chat)
    s.chats = chats
    if (!current) throw Error('此会话已删除，请新建会话后发送。')
    if (
      (current.host_id || '') !== s.host ||
      (current.service_id || '') !== s.service ||
      current.mode !== s.mode ||
      current.generation !== previous?.generation ||
      current.model_id !== previous?.model_id
    ) {
      await openChat(current)
      throw Error('会话已在其他设备变更。已恢复最新目标和设置，本条消息未发送。')
    }
    if (current.model_id !== s.model) await patchChat({ model_id: s.model })
  }
  const id = s.chat
  s.draft = ''
  s.reportRef = null
  try {
    await api('/chats/' + enc(id) + '/messages', 'POST', {
      content,
      report_id,
      request_id: requestId(),
    })
  } finally {
    if (s.chat === id) {
      s.messages = await api('/chats/' + enc(id) + '/messages')
      await syncTasks()
    }
  }
}
export function handoff(draft, ref = null) {
  s.draft = draft
  s.reportRef = ref
  s.view = 'chat'
  s.mobile = 'desk'
  s.dialog = ''
}
export function changed(msg) {
  s.notice = msg
}
export function focusTask(t) {
  s.tasks = [t, ...s.tasks.filter((x) => x.id !== t.id)]
  s.dialog = 'task:' + t.id
}
export async function ensureChat() {
  if (s.chat) return
  const c = await api('/chats', 'POST', {
    ...scope(),
    title: '运维会话',
    model_id: s.model || null,
    mode: s.mode,
  })
  s.chats = [c, ...s.chats]
  s.chat = c.id
  s.messages = []
}
