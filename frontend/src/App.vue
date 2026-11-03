<script setup>
import { ref, computed, watch, nextTick, onMounted, onUnmounted } from 'vue'
import {
  Smartphone,
  Monitor as Desktop,
  Server,
  Terminal,
  MessagesSquare,
  Activity,
  Workflow,
  ClipboardList,
  KeyRound,
  Settings2,
  Plus,
  Search,
  ChevronRight,
  ArrowUpRight,
  ArrowUp,
  Layers3,
  ShieldCheck,
  Menu,
  X,
  LogOut,
  Command,
  LockKeyhole,
  ListChecks,
  RefreshCw,
  Paperclip,
  Download,
} from 'lucide-vue-next'
import {
  s,
  host,
  chat,
  session,
  login,
  reset,
  api,
  run,
  openChat,
  newChat,
  changeTarget,
  setChatOption,
  send,
  when,
  label,
  syncTasks,
  handoff,
} from './store'
import Connection from './Connection.vue'
import Setup from './Setup.vue'
import Monitor from './Monitor.vue'
import Tunnels from './Tunnels.vue'
import Context from './Context.vue'
import Task from './Task.vue'
const phone = ref(localStorage.getItem('relay-layout') === 'phone')
function toggleLayout() {
  phone.value = !phone.value
  localStorage.setItem('relay-layout', phone.value ? 'phone' : 'desktop')
  s.mobile = phone.value ? 'hosts' : 'desk'
}
const password = ref(''),
  search = ref(''),
  feed = ref(null),
  install = ref(null)
const filtered = computed(() =>
  s.chats.filter((c) =>
    [c.title, s.hosts.find((h) => h.id === c.host_id)?.name]
      .join(' ')
      .toLowerCase()
      .includes(search.value.toLowerCase()),
  ),
)
const tabs = [
  ['chat', 'AI 会话', MessagesSquare],
  ['monitor', '服务监控', Activity],
  ['tunnels', '网络规则', Workflow],
  ['reports', '巡检报告', ClipboardList],
]
const prompts = [
  ['运行概况', '检查当前服务器的运行状态和服务。', Activity],
  [
    '入口检查',
    '检查当前服务的公网入口，分别列出配置、回执和实测结果。',
    Workflow,
  ],
  ['巡检解读', '解读当前服务器最近一次巡检的异常和未验证项。', ClipboardList],
]
const pending = computed(() =>
  s.tasks.filter((t) =>
    ['awaiting_confirmation', 'running', 'queued'].includes(t.status),
  ),
)
const locked = computed(() =>
  Object.entries(s.busy).some(([k, v]) => v && !['poll', 'tasks'].includes(k)),
)
async function authenticate() {
  try {
    await login(password.value)
  } finally {
    password.value = ''
  }
}
async function logout() {
  await api('/logout', 'POST', {})
  reset()
}
function tab(v) {
  s.view = v
  s.mobile = 'desk'
  s.dialog = ''
}
const onInstall = (e) => {
  e.preventDefault()
  install.value = e
}
function key(e) {
  if (e.key === 'Escape' && !locked.value) {
    s.dialog = ''
    s.error = ''
    s.notice = ''
  }
}
async function installApp() {
  if (install.value) {
    await install.value.prompt()
    await install.value.userChoice
    install.value = null
  } else s.notice = '在浏览器菜单中选择「安装应用」或「添加到主屏幕」。'
}
let timer, noticeTimer
onMounted(() => {
  window.addEventListener('keydown', key)
  window.addEventListener('beforeinstallprompt', onInstall)
  run('init', session)
  timer = setInterval(() => {
    if (
      s.auth &&
      !document.hidden &&
      !locked.value &&
      (s.view === 'chat' || s.dialog.startsWith('task'))
    )
      run('poll', syncTasks)
  }, 12000)
})
onUnmounted(() => {
  clearInterval(timer)
  clearTimeout(noticeTimer)
  window.removeEventListener('keydown', key)
  window.removeEventListener('beforeinstallprompt', onInstall)
})
watch(
  () => s.messages.length,
  async () => {
    await nextTick()
    if (feed.value) feed.value.scrollTop = feed.value.scrollHeight
  },
)
watch(
  () => s.notice,
  (v) => {
    clearTimeout(noticeTimer)
    if (v) noticeTimer = setTimeout(() => (s.notice = ''), 4500)
  },
)
</script>
<template>
  <div v-if="s.auth !== true" class="login-page">
    <div class="login-art">
      <div class="brand">
        <img src="/mark.svg" alt="" /><strong>端序</strong
        ><small>RELAY OPS</small>
      </div>
      <div class="login-graphic" aria-hidden="true">
        <div class="topoline"></div>
        <div class="ring r1"></div>
        <div class="ring r2"></div>
        <div class="ring r3"></div>
        <div class="core"><Terminal /></div>
        <div class="sat a"><Server /></div>
        <div class="sat b"><Activity /></div>
        <div class="sat c"><Workflow /></div>
      </div>
      <div class="art-copy">
        <small>OPERATIONS WORKSPACE</small>
        <h1>服务器运维工作台</h1>
        <p>查看服务状态、检查网络入口，确认每一次变更。</p>
      </div>
      <div class="art-foot">RELAY / CONTROL CONSOLE</div>
    </div>
    <main class="login-form">
      <small class="eyebrow"
        ><span class="pip"></span> ADMINISTRATOR ACCESS</small
      >
      <h2>登录控制台</h2>
      <p>使用管理员密码进入运维工作台。</p>
      <form
        v-if="s.auth === false"
        @submit.prevent="run('login', authenticate)"
      >
        <label
          >管理员密码<input
            type="password"
            v-model="password"
            autofocus
            required
            autocomplete="current-password"
            placeholder="输入管理员密码" /></label
        ><button class="primary" :disabled="s.busy.login">
          {{ s.busy.login ? '正在登录…' : '进入工作台' }}<ArrowUpRight />
        </button>
      </form>
      <p v-else-if="!s.error">正在连接控制服务…</p>
      <button v-else @click="run('init', session)">
        <RefreshCw />重新连接
      </button>
      <p v-if="s.error" class="error-inline" role="alert">{{ s.error }}</p>
      <footer><LockKeyhole /> 单管理员访问</footer>
    </main>
  </div>
  <div
    v-else
    class="shell"
    :class="['mobile-' + s.mobile, { 'phone-view': phone }]"
  >
    <aside class="rail">
      <div class="brand">
        <img src="/mark.svg" alt="" /><strong>端序</strong
        ><small>RELAY OPS</small>
      </div>
      <button
        class="new-chat"
        :disabled="locked"
        @click="run('new', () => newChat())"
      >
        <Plus />新建会话
      </button>
      <div class="rail-label">工作区 <span>WORKSPACE</span></div>
      <button
        class="rail-nav"
        :class="{ active: s.mobile !== 'history' }"
        @click="tab('chat')"
      >
        <Terminal />运维工作台<ChevronRight class="trail" />
      </button>
      <div class="rail-label history-label">
        历史会话 <span>{{ s.chats.length.toString().padStart(2, '0') }}</span>
      </div>
      <label class="rail-search"
        ><Search /><input
          aria-label="搜索会话"
          v-model="search"
          placeholder="搜索会话"
      /></label>
      <div class="history">
        <button
          v-for="c in filtered"
          :key="c.id"
          :disabled="locked"
          :class="{ selected: s.chat === c.id }"
          @click="run('open', () => openChat(c))"
        >
          <MessagesSquare /><span
            >{{ c.title || '未命名会话'
            }}<small
              >{{
                s.hosts.find((h) => h.id === c.host_id)?.name || '未选择服务器'
              }}
              · {{ when(c.updated_at || c.created_at) }}</small
            ></span
          >
        </button>
        <p v-if="!filtered.length">
          {{ search ? '没有匹配的会话' : '暂无历史会话' }}
        </p>
      </div>
      <div class="rail-bottom">
        <button @click="s.dialog = 'settings'"><Settings2 />工作台设置</button
        ><button class="admin" @click="s.dialog = 'account'">
          <span>AD</span><b>管理员<small>工作区所有者</small></b
          ><ChevronRight />
        </button>
      </div>
    </aside>
    <section class="desk">
      <header class="topbar">
        <div class="row">
          <span class="desktop-only"><Command /></span
          ><button
            class="icon mobile-only"
            aria-label="会话历史"
            @click="s.mobile = 'history'"
          >
            <Menu /></button
          ><span>工作空间</span><ChevronRight /><b>运维工作台</b>
        </div>
        <div class="row">
          <button class="plain layout-toggle" @click="toggleLayout">
            <component :is="phone ? Desktop : Smartphone" />{{
              phone ? '桌面视图' : '手机视图'
            }}</button
          ><button class="plain task-link" @click="s.dialog = 'tasks'">
            <ListChecks />任务记录 <span>{{ pending.length }}</span>
          </button>
        </div>
      </header>
      <section class="mobile-homes">
        <div class="eyebrow">HOST DIRECTORY</div>
        <h1>服务器</h1>
        <p class="hint">选择目标进入运维工作台。</p>
        <button
          v-for="h in s.hosts"
          :key="h.id"
          class="host-card"
          @click="
            run('target', async () => {
              if (h.id !== s.host) await changeTarget(h.id)
              tab('chat')
            })
          "
        >
          <Server /><span
            ><b>{{ h.name }}</b
            ><small>{{ h.address }} · {{ h.environment }}</small></span
          ><ChevronRight /></button
        ><button @click="s.dialog = 'hosts'"><Plus />添加服务器</button>
      </section>
      <div class="desk-body">
        <div class="heading">
          <div>
            <small class="eyebrow"
              ><span class="pip"></span> OPERATIONS WORKSPACE</small
            >
            <h1>运维工作台</h1>
          </div>
          <button class="small" @click="s.dialog = 'hosts'">
            <Server />管理资产
          </button>
        </div>
        <div class="targetbar">
          <label
            ><small>服务器</small
            ><select
              :value="s.host"
              :disabled="locked"
              @change="(e) => run('target', () => changeTarget(e.target.value))"
            >
              <option value="" disabled>选择服务器</option>
              <option v-for="h in s.hosts" :key="h.id" :value="h.id">
                {{ h.name }}
              </option>
            </select></label
          >
          <div class="env">
            <small>环境</small><b>{{ host?.environment || '—' }}</b>
          </div>
          <label
            ><small>服务</small
            ><select
              :value="s.service"
              :disabled="!s.host || locked"
              @change="
                (e) => run('target', () => changeTarget(s.host, e.target.value))
              "
            >
              <option value="">全部服务</option>
              <option v-for="a in s.services" :key="a.id" :value="a.id">
                {{ a.name }}
              </option>
            </select></label
          ><label
            ><small>模型</small
            ><select
              :value="s.model"
              :disabled="locked"
              @change="
                (e) =>
                  run('model', () => setChatOption('model_id', e.target.value))
              "
            >
              <option disabled value="">选择模型</option>
              <option v-for="m in s.models" :key="m.id" :value="m.id">
                {{ m.name }}
              </option>
            </select></label
          >
        </div>
        <nav class="tabs" aria-label="工作台视图">
          <button
            v-for="[v, title, I] in tabs"
            :key="v"
            :class="{ active: s.view === v }"
            @click="tab(v)"
          >
            <component :is="I" />{{ title }}
          </button>
        </nav>
        <main v-if="s.view === 'chat'" class="chat-panel">
          <div class="panel-head">
            <div class="row">
              <span class="mini-icon"><Terminal /></span>
              <h2>{{ chat?.title || 'AI 会话' }}</h2>
            </div>
            <button class="plain small" @click="s.dialog = 'context'">
              <Layers3 />上下文
            </button>
          </div>
          <div ref="feed" class="feed">
            <div v-if="!s.messages.length" class="empty-chat">
              <div class="assistant-emblem"><Terminal /></div>
              <small class="eyebrow">AI OPERATIONS</small>
              <h2>从一次检查开始</h2>
              <p>选择服务器和模型，向 AI 描述要检查的服务或入口。</p>
              <div class="prompt-grid">
                <button
                  v-for="[title, txt, I] in prompts"
                  :key="title"
                  @click="handoff(txt)"
                >
                  <component :is="I" /><span>{{ title }}</span
                  ><ArrowUpRight />
                </button>
              </div>
            </div>
            <article
              v-for="m in s.messages"
              :key="m.id"
              class="message"
              :class="m.role"
            >
              <small
                >{{
                  m.role === 'user'
                    ? '管理员'
                    : m.role === 'assistant'
                      ? '运维助手'
                      : m.role
                }}
                · {{ when(m.created_at) }}
                <template v-if="m.model_id">
                  ·
                  {{
                    s.models.find((x) => x.id === m.model_id)?.name ||
                    m.model_id
                  }}</template
                ></small
              >
              <p>{{ m.content }}</p>
              <button v-if="m.task_id" @click="s.dialog = 'task:' + m.task_id">
                <ListChecks />查看关联任务
              </button>
            </article>
            <p v-if="s.busy.send" class="hint">正在等待控制服务返回结果…</p>
          </div>
          <div v-if="pending.length" class="pending">
            <button @click="s.dialog = 'tasks'">
              <ListChecks />当前目标有 {{ pending.length }} 项待处理任务
              <ChevronRight />
            </button>
          </div>
          <div class="composer">
            <div v-if="s.reportRef" class="row hint">
              <Paperclip />已附巡检报告
              <button
                class="icon"
                aria-label="移除报告"
                @click="s.reportRef = null"
              >
                <X />
              </button>
            </div>
            <textarea
              v-model="s.draft"
              aria-label="会话消息"
              placeholder="描述需要检查的问题，或拟定一项变更计划…"
              rows="2"
              @keydown.ctrl.enter.prevent="!locked && run('send', send)"
            ></textarea>
            <div class="row split">
              <label class="mode"
                ><ShieldCheck /><select
                  aria-label="操作模式"
                  :value="s.mode"
                  :disabled="locked"
                  @change="
                    (e) =>
                      run('mode', () => setChatOption('mode', e.target.value))
                  "
                >
                  <option value="inspect">仅检查</option>
                  <option value="change">允许确认变更</option>
                </select></label
              ><button
                class="send"
                :disabled="locked || !s.draft.trim() || !s.model || !s.host"
                @click="run('send', send)"
              >
                发送 <ArrowUp />
              </button>
            </div>
          </div>
          <small class="chat-foot">{{
            !s.host
              ? '请先添加或选择服务器。'
              : !s.model
                ? '请在工作台设置中添加模型。'
                : '变更操作需要确认具体计划后执行。'
          }}</small>
        </main>
        <Monitor
          v-else-if="s.view === 'monitor' || s.view === 'reports'"
          :key="s.view + ':' + s.version"
          :reports="s.view === 'reports'"
        /><Tunnels v-else :key="s.version" />
      </div>
    </section>
    <aside class="right">
      <div class="inspector-top">
        <KeyRound /><span>连接管理</span><small>SSH</small>
      </div>
      <Connection :key="s.host" />
    </aside>
    <button v-if="phone" class="exit-phone" @click="toggleLayout">
      <Desktop />返回桌面视图
    </button>
    <nav class="mobile-nav">
      <button @click="s.mobile = 'hosts'"><Server />服务器</button
      ><button @click="s.mobile = 'desk'"><Terminal />工作台</button
      ><button @click="s.mobile = 'right'"><KeyRound />连接</button
      ><button @click="s.dialog = 'settings'"><Settings2 />设置</button>
    </nav>
  </div>
  <div
    v-if="s.auth && s.dialog"
    class="backdrop"
    @click.self="!locked && (s.dialog = '')"
  >
    <section
      class="modal"
      role="dialog"
      aria-modal="true"
      aria-label="工作台详情"
      @keydown.esc.stop="!locked && (s.dialog = '')"
    >
      <button
        class="modal-close icon"
        aria-label="关闭"
        @click="s.dialog = ''"
        :disabled="locked"
      >
        <X /></button
      ><Setup
        v-if="['hosts', 'settings'].includes(s.dialog)"
        :key="s.dialog"
        :section="s.dialog"
      /><Context v-else-if="s.dialog === 'context'" /><Task
        v-else-if="s.dialog.startsWith('task:')"
        :id="s.dialog.slice(5)"
      /><template v-else-if="s.dialog === 'tasks'"
        ><div class="section-title">
          <ListChecks />
          <div>
            <small>ACTIVITY</small>
            <h2>当前目标的任务</h2>
          </div>
        </div>
        <button class="small" @click="run('tasks', syncTasks)">
          <RefreshCw />刷新</button
        ><button
          v-for="t in s.tasks"
          :key="t.id"
          class="list-row"
          @click="s.dialog = 'task:' + t.id"
        >
          <span
            ><b>{{ t.kind }} / {{ t.action }}</b
            ><small>{{ when(t.updated_at) }}</small></span
          ><span class="pill">{{ label(t.status) }}</span>
        </button>
        <p v-if="!s.tasks.length" class="hint">暂无任务记录。</p></template
      ><template v-else
        ><h2>管理员账户</h2>
        <button @click="installApp"><Download />安装到此设备</button>
        <button @click="run('logout', logout)">
          <LogOut />退出登录
        </button></template
      >
    </section>
  </div>
  <div
    v-if="s.auth && (s.error || s.notice)"
    class="toast"
    role="status"
    :class="{ bad: s.error }"
  >
    <span>{{ s.error || s.notice }}</span
    ><button
      aria-label="关闭提示"
      @click="
        s.error = ''; s.notice = ''
      "
    >
      <X />
    </button>
  </div>
</template>
