<script setup>
import { ref, computed, onMounted, watch } from 'vue'
import {
  Workflow,
  Plus,
  RefreshCw,
  ArrowUpRight,
  Server,
} from 'lucide-vue-next'
import {
  s,
  host,
  api,
  run,
  query,
  scope,
  focusTask,
  handoff,
  when,
  label,
  setChatOption,
  changeTarget,
  ensureChat,
  requestId,
} from './store'
import Evidence from './Evidence.vue'
const kind = ref('forward'),
  rows = ref([]),
  nodes = ref([]),
  clients = ref([]),
  editing = ref(false),
  id = ref(''),
  f = ref({}),
  loaded = ref(false)
const kinds = {
    forward: '端口转发',
    p2p: 'P2P 隧道',
    node: '公网节点',
    client: '内网客户端',
  },
  rule = computed(() => ['forward', 'p2p'].includes(kind.value))
let seq = 0
function select(k) {
  kind.value = k
  rows.value = []
  editing.value = false
  run('tunnels', load)
}
async function load() {
  if (!s.host) return
  const n = ++seq,
    k = kind.value
  loaded.value = false
  const all = await Promise.all(
    [...new Set([k, 'node', 'client'])].map(async (t) => [
      t,
      await api(
        '/tunnels/' +
          t +
          query(t === 'node' || t === 'client' ? { host_id: s.host } : scope()),
      ),
    ]),
  )
  if (n === seq && k === kind.value) {
    const sets = Object.fromEntries(all)
    rows.value = sets[k]
    nodes.value = sets.node
    clients.value = sets.client
    loaded.value = true
  }
}
onMounted(() => run('tunnels', load))
watch(
  () => s.dialog,
  (now, old) => {
    if (!now && old?.startsWith('task:')) run('tunnels', load)
  },
)
function form(r = null) {
  id.value = r?.id || ''
  let d = r?.data || {}
  const base =
    kind.value === 'client'
      ? { name: '' }
      : kind.value === 'node'
        ? { name: '', address: '', forward_ports: '', disable_relay: false }
        : {
            name: '',
            node_id: '',
            client_id: '',
            target_host: '',
            target_port: 80,
            ...(kind.value === 'forward'
              ? { public_port: 18080 }
              : { allow_relay: false }),
          }
  f.value = Object.fromEntries(
    Object.entries(base).map(([k, v]) => [k, d[k] ?? v]),
  )
  editing.value = true
}
async function action(a, r, data = {}) {
  if (a !== 'check' && s.mode !== 'change')
    throw Error('请先切换到允许确认变更模式。')
  if (rule.value && r?.service_id && s.service !== r.service_id)
    await changeTarget(s.host, r.service_id)
  await ensureChat()
  const t = await api('/tunnels/' + kind.value + '/' + a, 'POST', {
    ...scope(),
    id: r?.id || undefined,
    data,
    chat_id: s.chat,
    request_id: requestId(),
  })
  editing.value = false
  focusTask(t)
}
async function editRow(r) {
  if (rule.value && r.service_id && s.service !== r.service_id) {
    await changeTarget(s.host, r.service_id)
    return
  }
  form(r)
}
async function ai(r) {
  if (r.service_id && r.service_id !== s.service)
    await changeTarget(s.host, r.service_id)
  handoff(
    '请检查' +
      kinds[kind.value] +
      '「' +
      r.data.name +
      '」（编号 ' +
      r.id +
      '），区分配置、应用回执与实际连接证据。',
  )
}
function receipt(d) {
  if (d.applied === 'applied' && d.enabled) return '配置已应用'
  if (d.applied === 'revoked' && !d.enabled) return '配置已撤销'
  return d.enabled ? '待应用' : '待撤销'
}
function endpoint(arr, id) {
  return (
    arr.find((x) => String(x.id) === String(id))?.data?.name || id || '未记录'
  )
}
</script>
<template>
  <section class="work-panel">
    <div class="panel-head">
      <div class="row">
        <span class="mini-icon"><Workflow /></span>
        <h2>端口转发与 P2P</h2>
      </div>
      <button
        class="plain small"
        @click="run('tunnels', load)"
        :disabled="s.busy.tunnels || !host"
      >
        <RefreshCw />刷新
      </button>
    </div>
    <div class="panel-inner">
      <div v-if="!host" class="empty-state">
        <Server />
        <h3>尚未选择服务器</h3>
        <p>请选择要管理的服务器。</p>
      </div>
      <template v-else
        ><div class="segmented">
          <button
            v-for="(v, k) in kinds"
            :disabled="s.busy.tunnels"
            :class="{ active: kind === k }"
            @click="select(k)"
          >
            {{ v }}
          </button>
        </div>
        <div class="row split wrap">
          <div>
            <small class="eyebrow">NETWORK CONTROL</small>
            <h3>
              {{ kinds[kind] }}
              <span class="count" v-if="loaded">{{ rows.length }}</span>
            </h3>
          </div>
          <button
            :disabled="s.mode === 'inspect' || (rule && !s.service) || !loaded"
            @click="form()"
          >
            <Plus />新增
          </button>
        </div>
        <p v-if="s.mode === 'inspect'" class="muted-box">
          仅检查模式 · 可读取配置和检查证据。
          <button
            class="plain small"
            @click="run('mode', () => setChatOption('mode', 'change'))"
          >
            允许确认变更 <ArrowUpRight />
          </button>
        </p>
        <p v-if="rule && !s.service" class="hint">
          选定关联服务后可以创建规则。
        </p>
        <form
          v-if="editing"
          class="form-grid rule-form"
          @submit.prevent="
            run('rule', () =>
              action(
                id ? 'update' : 'create',
                id ? rows.find((x) => x.id === id) : null,
                f,
              ),
            )
          "
        >
          <h3 class="span2">{{ id ? '修改' : '新增' }}{{ kinds[kind] }}</h3>
          <label class="span2">名称<input v-model="f.name" required /></label
          ><template v-if="kind === 'node'"
            ><label
              >节点地址（含 FRP 端口）<input
                v-model="f.address"
                placeholder="域名:7000"
                required /></label
            ><label
              >允许转发的端口范围<input
                v-model="f.forward_ports"
                placeholder="18080-18090"
                required /></label
            ><label class="check span2"
              ><input type="checkbox" v-model="f.disable_relay" />禁止 P2P
              中继</label
            ></template
          ><template v-if="rule"
            ><label
              >公网节点<select v-model="f.node_id" :disabled="!!id" required>
                <option disabled value="">选择节点</option>
                <option v-for="n in nodes" :value="n.data.id">
                  {{ n.data.name }}
                </option>
              </select></label
            ><label
              >内网客户端<select
                v-model="f.client_id"
                :disabled="!!id"
                required
              >
                <option disabled value="">选择客户端</option>
                <option v-for="n in clients" :value="n.data.id">
                  {{ n.data.name }}
                </option>
              </select></label
            ><label
              >目标 IPv4 地址<input
                v-model="f.target_host"
                placeholder="192.168.1.20"
                required /></label
            ><label
              >目标端口<input
                v-model.number="f.target_port"
                type="number"
                min="1"
                max="65535"
                required /></label
            ><label v-if="kind === 'forward'"
              >公网端口 · TCP + UDP<input
                v-model.number="f.public_port"
                type="number"
                min="1"
                max="65535"
                required /></label
            ><label v-else class="check"
              ><input type="checkbox" v-model="f.allow_relay" />允许 STCP
              回退（受节点限制）</label
            ></template
          >
          <div class="actions span2">
            <button class="primary" :disabled="s.busy.rule">预览变更计划</button
            ><button type="button" @click="editing = false">取消</button>
          </div>
        </form>
        <div v-if="s.busy.tunnels" class="hint">正在读取配置…</div>
        <p v-else-if="loaded && !rows.length && !editing" class="muted-box">
          当前范围内没有{{ kinds[kind] }}。
        </p>
        <div v-else-if="!loaded" class="muted-box">
          未取得配置，请检查管理服务后刷新。
        </div>
        <article v-for="r in rows" :key="r.id" class="rule-card">
          <div class="row split">
            <h3>{{ r.data.name }}</h3>
            <span class="pill">{{
              rule ? receipt(r.data) : r.data.online ? '在线' : '离线'
            }}</span>
          </div>
          <small class="hint"
            >gostc · #{{ r.id }} · 版本 {{ r.data.version || '—' }}</small
          ><template v-if="rule"
            ><p>
              <b
                >{{ endpoint(nodes, r.data.node_id)
                }}{{ kind === 'forward' ? ':' + r.data.public_port : '' }}</b
              >
              → {{ endpoint(clients, r.data.client_id) }} /
              {{ r.data.target_host }}:{{ r.data.target_port }}
            </p>
            <small
              >{{
                s.services.find((x) => x.id === r.service_id)?.name ||
                r.service_id
              }}
              · {{ kind === 'forward' ? 'TCP + UDP' : 'TCP / XTCP' }} ·
              {{ r.data.enabled ? '配置启用' : '配置停用' }}</small
            >
            <p v-if="kind === 'p2p'" class="hint">
              实际路径：{{
                {
                  direct: '直连',
                  relay: '中继',
                  failed: '失败',
                  waiting: '等待连接',
                }[r.data.path] || '路径未确认'
              }}
              · {{ r.data.allow_relay ? '隧道允许中继' : '隧道禁止中继' }}
            </p>
            <div class="sample-time">
              节点：{{ r.data.node_online ? '在线' : '离线' }} · 客户端：{{
                r.data.client_online ? '在线' : '离线'
              }}<br />客户端回执：{{ label(r.data.applied) }} ·
              {{ when(r.data.applied_at) }}
            </div>
            <p v-if="r.data.warning" class="hint">{{ r.data.warning }}</p>
            <div class="sample-time">
              最近公网验证：{{ when(r.last_check?.time) }} ·
              {{ r.last_check?.evidence || '公网未验证' }}
            </div></template
          ><Evidence v-else :value="r.data" />
          <div class="actions">
            <button
              class="small"
              :disabled="s.busy.rule"
              @click="run('rule', () => action('check', r))"
            >
              检查</button
            ><template v-if="rule && s.mode === 'change'"
              ><button class="small" @click="run('rule', () => editRow(r))">
                {{ !s.service ? '选择服务' : '修改' }}</button
              ><button
                class="small"
                :disabled="s.busy.rule"
                @click="
                  run('rule', () =>
                    action('enable', r, { enabled: !r.data.enabled }),
                  )
                "
              >
                {{ r.data.enabled ? '停用' : '启用' }}</button
              ><button
                class="small danger"
                :disabled="s.busy.rule"
                @click="run('rule', () => action('delete', r))"
              >
                删除
              </button></template
            ><button class="small plain" @click="run('ai', () => ai(r))">
              交给 AI <ArrowUpRight />
            </button>
          </div></article
      ></template>
    </div>
  </section>
</template>
