<script setup>
import { ref, onMounted } from 'vue'
import { Server, SlidersHorizontal, Plus, Save, Play } from 'lucide-vue-next'
import {
  s,
  host,
  api,
  run,
  enc,
  catalog,
  loadTarget,
  changeTarget,
  setChatOption,
  changed,
  focusTask,
  scope,
  ensureChat,
  requestId,
} from './store'
import PushSettings from './PushSettings.vue'
import Evidence from './Evidence.vue'
const props = defineProps({ section: String })
const tab = ref(props.section === 'hosts' ? 'host' : 'model'),
  id = ref(''),
  f = ref({}),
  probe = ref(false),
  pf = ref({}),
  ports = ref(''),
  interval = ref(0),
  test = ref(null),
  image = ref('')
const hp = {
    name: '',
    address: '',
    port: 22,
    environment: 'test',
    jump_host_id: '',
  },
  sp = {
    name: '',
    directory: '',
    compose_service: '',
    config_path: 'compose.yaml',
    image_variable: 'IMAGE',
    health_url: '',
  },
  mp = {
    name: '',
    base_url: '',
    model: '',
    api_key: '',
    capacity: 16384,
    tools: true,
    is_default: false,
  }
function edit(obj = null) {
  id.value = obj?.id || ''
  test.value = null
  const base = tab.value === 'host' ? hp : tab.value === 'service' ? sp : mp
  f.value = Object.fromEntries(
    Object.keys(base).map((k) => [
      k,
      k === 'api_key' ? '' : (obj?.[k] ?? base[k]),
    ]),
  )
  ports.value = (obj?.ports || []).join(', ')
  probe.value = !!obj?.public_probe
  pf.value = {
    protocol: 'http',
    host: '',
    port: 80,
    path: '/',
    request: '',
    expect: '',
    ...obj?.public_probe,
  }
  image.value = ''
}
async function schedule() {
  if (s.host)
    interval.value = (
      await api('/hosts/' + enc(s.host) + '/schedule')
    ).interval_minutes
}
function selectTab(t) {
  tab.value = t
  edit()
  if (t === 'schedule') run('sched', schedule)
}
onMounted(() => edit())
async function save() {
  const editing = id.value
  let obj
  if (tab.value === 'host') {
    const { jump_host_id, ...other } = f.value
    obj = await api(
      '/hosts' + (editing ? '/' + enc(editing) : ''),
      editing ? 'PATCH' : 'POST',
      editing ? { ...other, jump_host_id: jump_host_id || null } : other,
    )
    await catalog()
    if (!editing || editing === s.host) {
      await changeTarget(obj.id)
      s.mobile = 'desk'
    }
  }
  if (tab.value === 'service') {
    const data = {
      ...f.value,
      ports: ports.value.trim()
        ? ports.value
            .trim()
            .split(/[,，\s]+/)
            .map(Number)
        : [],
      public_probe: probe.value
        ? Object.fromEntries(
            Object.entries(pf.value).filter(([, v]) => v !== ''),
          )
        : null,
    }
    if (!editing) data.host_id = s.host
    obj = await api(
      '/services' + (editing ? '/' + enc(editing) : ''),
      editing ? 'PATCH' : 'POST',
      data,
    )
    await loadTarget()
  }
  if (tab.value === 'model') {
    const data = { ...f.value }
    if (!data.api_key) delete data.api_key
    try {
      obj = await api(
        '/models' + (editing ? '/' + enc(editing) : ''),
        editing ? 'PATCH' : 'POST',
        data,
      )
    } finally {
      f.value.api_key = ''
    }
    await catalog()
    if (!s.model) await setChatOption('model_id', obj.id)
  }
  edit(obj)
  changed('配置已保存。')
}
async function saveSchedule() {
  await api('/hosts/' + enc(s.host) + '/schedule', 'PUT', {
    interval_minutes: interval.value,
  })
  await schedule()
  changed('巡检策略已保存。')
}
async function deploy() {
  if (s.service !== id.value) await changeTarget(s.host, id.value)
  await ensureChat()
  const t = await api('/deployments', 'POST', {
    ...scope(),
    image: image.value.trim(),
    chat_id: s.chat || undefined,
    request_id: requestId(),
  })
  focusTask(t)
}
</script>
<template>
  <div class="section-title">
    <component :is="section === 'hosts' ? Server : SlidersHorizontal" />
    <div>
      <small>{{
        section === 'hosts' ? 'ASSET INVENTORY' : 'PREFERENCES'
      }}</small>
      <h2>{{ section === 'hosts' ? '资产管理' : '工作台设置' }}</h2>
    </div>
  </div>
  <div class="segmented">
    <button
      v-for="[key, title] in section === 'hosts'
        ? [
            ['host', '服务器'],
            ['service', '服务档案'],
          ]
        : [
            ['model', '模型接口'],
            ['schedule', '巡检策略'],
          ]"
      :class="{ active: tab === key }"
      @click="selectTab(key)"
    >
      {{ title }}
    </button>
  </div>
  <template v-if="tab === 'schedule'"
    ><h3>定时巡检</h3>
    <p class="hint">
      {{ host ? '目标服务器：' + host.name : '请先选择服务器。' }}
    </p>
    <form v-if="host" @submit.prevent="run('sched', saveSchedule)">
      <label
        >巡检间隔（分钟，0 表示关闭）<input
          v-model.number="interval"
          required
          type="number"
          min="0" /></label
      ><button class="primary" :disabled="s.busy.sched">保存策略</button>
    </form>
    <PushSettings /></template
  >
  <template v-else
    ><p v-if="tab === 'service'" class="hint">
      当前服务器：{{ host?.name || '未选择' }}
    </p>
    <div class="chip-list">
      <button
        v-for="x in tab === 'host'
          ? s.hosts
          : tab === 'model'
            ? s.models
            : s.services"
        :key="x.id"
        :class="{ chosen: id === x.id }"
        @click="edit(x)"
      >
        {{ x.name }}<small v-if="x.is_default">默认</small></button
      ><button @click="edit()" :class="{ chosen: !id }"><Plus />新增</button>
    </div>
    <form class="form-grid" @submit.prevent="run('setup', save)">
      <label class="span2">名称<input v-model="f.name" required /></label
      ><template v-if="tab === 'host'"
        ><label
          >SSH 地址<input
            v-model="f.address"
            required
            placeholder="域名或 IP" /></label
        ><label
          >SSH 端口<input
            v-model.number="f.port"
            type="number"
            min="1"
            max="65535"
            required /></label
        ><label>环境<input v-model="f.environment" required /></label
        ><label v-if="id"
          >跳板主机<select v-model="f.jump_host_id">
            <option value="">直连</option>
            <option
              v-for="h in s.hosts.filter((x) => x.id !== id)"
              :value="h.id"
            >
              {{ h.name }}
            </option>
          </select></label
        ></template
      >
      <template v-if="tab === 'model'"
        ><label class="span2"
          >兼容接口地址<input
            v-model="f.base_url"
            type="url"
            required
            placeholder="https://…/v1" /></label
        ><label>模型标识<input v-model="f.model" required /></label
        ><label
          >上下文容量<input
            v-model.number="f.capacity"
            type="number"
            min="4096"
            max="2097152"
            required /></label
        ><label class="span2"
          >API Key<input
            v-model="f.api_key"
            type="password"
            autocomplete="new-password"
            :placeholder="
              id ? '留空保留现有密钥' : '输入密钥（如需）'
            " /></label
        ><label class="check"
          ><input v-model="f.tools" type="checkbox" />支持工具调用</label
        ><label class="check"
          ><input v-model="f.is_default" type="checkbox" />默认模型</label
        ></template
      >
      <template v-if="tab === 'service'"
        ><label
          >部署目录<input v-model="f.directory" placeholder="/srv/app" /></label
        ><label>Compose 服务名<input v-model="f.compose_service" /></label
        ><label
          >配置路径<input
            v-model="f.config_path"
            placeholder="compose.yaml" /></label
        ><label>镜像变量<input v-model="f.image_variable" /></label
        ><label
          >服务端口<input
            v-model="ports"
            placeholder="多个端口用逗号分隔" /></label
        ><label>健康检查 URL<input type="url" v-model="f.health_url" /></label
        ><label class="check span2"
          ><input v-model="probe" type="checkbox" />登记公网探测入口</label
        ><template v-if="probe"
          ><label>公网地址<input v-model="pf.host" required /></label
          ><label
            >端口<input
              type="number"
              v-model.number="pf.port"
              min="1"
              max="65535"
              required /></label
          ><label
            >协议<select v-model="pf.protocol">
              <option v-for="p in ['http', 'https', 'tcp', 'udp']">
                {{ p }}
              </option>
            </select></label
          ><label v-if="['http', 'https'].includes(pf.protocol)"
            >路径<input v-model="pf.path" required /></label
          ><label v-else>请求报文<input v-model="pf.request" /></label
          ><label>预期响应内容<input v-model="pf.expect" /></label></template
      ></template>
      <div class="actions span2">
        <button
          class="primary"
          :disabled="s.busy.setup || (tab === 'service' && !s.host)"
        >
          <Save />保存{{ id ? '修改' : '配置' }}</button
        ><button
          v-if="id && tab === 'model'"
          type="button"
          :disabled="s.busy.testModel"
          @click="
            run('testModel', async () => {
              test = null
              test = await api('/models/' + enc(id) + '/test', 'POST', {})
            })
          "
        >
          <Play />测试接口
        </button>
      </div>
    </form>
    <Evidence v-if="test" :value="test" />
    <template v-if="id && tab === 'service'"
      ><h3>服务现场与验证记录</h3>
      <Evidence
        :value="
          Object.fromEntries(
            ['expected', 'verified', 'state'].map((k) => [
              k,
              s.services.find((x) => x.id === id)?.[k],
            ]),
          )
        "
      />
      <form @submit.prevent="run('deploy', deploy)">
        <label
          >部署或回退到镜像<input
            v-model="image"
            placeholder="镜像名称与固定版本"
            required /></label
        ><button :disabled="s.mode === 'inspect' || s.busy.deploy">
          生成部署确认计划</button
        ><small v-if="s.mode === 'inspect'" class="hint"
          >先将会话操作模式切换为「允许确认变更」。</small
        >
      </form></template
    ></template
  >
</template>
