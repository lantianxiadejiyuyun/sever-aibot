<script setup>
import { ref, computed } from 'vue'
import {
  KeyRound,
  Server,
  Link2,
  ShieldCheck,
  Upload,
  Plus,
  PlugZap,
} from 'lucide-vue-next'
import { s, host, api, run, changed, enc, catalog } from './store'
import Evidence from './Evidence.vue'
const purpose = ref('inspect'),
  user = ref(''),
  cred = ref(''),
  name = ref(''),
  file = ref(null),
  fileInput = ref(null),
  pass = ref(''),
  kind = ref('inspect'),
  fp = ref(null),
  test = ref(null),
  ack = ref(false),
  disableId = ref('')
const eligible = computed(() =>
  s.creds.filter((x) => x.enabled && x.purpose === purpose.value),
)
const bound = computed(() => host.value?.bindings || {})
async function upload() {
  const f = file.value
  if (!f) throw Error('请选择要导入的私钥文件。')
  try {
    const c = await api('/credentials', 'POST', {
      name: name.value.trim(),
      purpose: kind.value,
      private_key: await f.text(),
      passphrase: pass.value || undefined,
    })
    await catalog()
    purpose.value = c.purpose
    cred.value = c.id
    name.value = ''
    changed('已导入凭据，可在下方绑定。')
  } finally {
    pass.value = ''
    file.value = null
    if (fileInput.value) fileInput.value.value = ''
  }
}
async function bind() {
  await api('/hosts/' + enc(s.host) + '/bind', 'POST', {
    purpose: purpose.value,
    username: user.value.trim(),
    credential_id: cred.value,
  })
  await catalog()
  test.value = null
  changed('凭据绑定已保存。')
}
async function probe() {
  fp.value = null
  ack.value = false
  fp.value = await api('/hosts/' + enc(s.host) + '/fingerprint', 'POST', {})
}
async function trust() {
  if (!ack.value || !fp.value) return
  await api('/hosts/' + enc(s.host) + '/trust', 'POST', {
    fingerprint: fp.value.fingerprint,
  })
  await catalog()
  changed('主机指纹已固定。')
}
async function testConn() {
  test.value = null
  const selected = purpose.value
  const result = await api('/hosts/' + enc(s.host) + '/test', 'POST', {
    purpose: selected,
  })
  if (selected === purpose.value) test.value = result
}
async function disable() {
  await api('/credentials/' + enc(disableId.value) + '/disable', 'POST', {})
  disableId.value = ''
  await catalog()
  changed('凭据已停用。')
}
</script>
<template>
  <div class="right-body">
    <small class="eyebrow">CONNECTION PROFILE</small>
    <h2>SSH 连接</h2>
    <p class="hint">管理当前服务器的身份与访问凭据。</p>
    <div class="host-id">
      <span class="mini-icon"><Server /></span>
      <div>
        <strong>{{ host?.name || '未选择服务器' }}</strong
        ><small>{{
          host ? host.address + ':' + host.port : '请先在工作台选择目标'
        }}</small>
      </div>
    </div>
    <template v-if="host"
      ><div class="kv">
        <span>连接方式</span
        ><b>{{ host.jump_host_id ? '经跳板机' : '直接连接' }}</b>
      </div>
      <div v-if="host.jump_host_id" class="kv">
        <span>跳板</span
        ><b>{{
          s.hosts.find((h) => h.id === host.jump_host_id)?.name ||
          host.jump_host_id
        }}</b>
      </div></template
    >
    <div class="section-divider"></div>
    <h3>
      <KeyRound />凭据库 <span class="count">{{ s.creds.length }}</span>
    </h3>
    <details class="import-box">
      <summary><Upload />导入 SSH 私钥 <Plus /></summary>
      <form @submit.prevent="run('import', upload)">
        <label
          >私钥文件<input
            ref="fileInput"
            type="file"
            required
            @change="
              (e) => {
                file = e.target.files[0]
                if (!name && file) name = file.name
              }
            " /></label
        ><label>凭据名称<input v-model="name" required /></label
        ><label
          >用途<select v-model="kind">
            <option value="inspect">只读巡检</option>
            <option value="deploy">部署</option>
          </select></label
        ><label
          >私钥口令（如有）<input
            type="password"
            v-model="pass"
            autocomplete="new-password" /></label
        ><button class="primary" :disabled="s.busy.import">导入凭据</button>
      </form>
    </details>
    <p v-if="!s.creds.length" class="hint tiny">暂无已导入的凭据。</p>
    <details v-for="c in s.creds" :key="c.id">
      <summary>
        <KeyRound />{{ c.name }}
        <small>{{
          c.enabled ? (c.purpose === 'inspect' ? '巡检' : '部署') : '已停用'
        }}</small>
      </summary>
      <code>{{ c.fingerprint }}</code
      ><template v-if="c.enabled"
        ><button
          v-if="disableId !== c.id"
          class="small danger"
          @click="disableId = c.id"
        >
          停用凭据
        </button>
        <div v-else>
          <p class="hint">停用后，绑定此凭据的主机无法用它发起新连接。</p>
          <button
            class="small danger"
            :disabled="s.busy.disable"
            @click="run('disable', disable)"
          >
            确认停用
          </button>
          <button class="small" @click="disableId = ''">取消</button>
        </div></template
      >
    </details>
    <div class="section-divider"></div>
    <h3><Link2 />绑定与测试</h3>
    <div v-if="!host" class="muted-box">
      选择服务器后可绑定凭据并核对主机指纹。
    </div>
    <template v-else
      ><form @submit.prevent="run('bind', bind)">
        <label
          >连接用途<select
            v-model="purpose"
            @change="
              cred = ''; user = ''; test = null
            "
          >
            <option value="inspect">只读巡检</option>
            <option value="deploy">服务部署</option>
          </select></label
        >
        <div v-if="bound[purpose]" class="hint">
          当前：{{ bound[purpose].username }} /
          {{
            s.creds.find((c) => c.id === bound[purpose].credential_id)?.name ||
            '已绑定'
          }}
        </div>
        <label
          >Linux 账号<input
            v-model="user"
            required
            placeholder="输入远程用户名"
            autocomplete="off" /></label
        ><label
          >已导入的凭据<select v-model="cred" required>
            <option disabled value="">选择对应用途的凭据</option>
            <option v-for="c in eligible" :key="c.id" :value="c.id">
              {{ c.name }}
            </option>
          </select></label
        ><button :disabled="s.busy.bind">绑定或替换凭据</button>
      </form>
      <div class="section-divider"></div>
      <h3><ShieldCheck />主机身份</h3>
      <code v-if="host.fingerprint">{{ host.fingerprint }}</code
      ><button class="small" :disabled="s.busy.fp" @click="run('fp', probe)">
        获取主机指纹
      </button>
      <div v-if="fp" class="muted-box">
        <code>{{ fp.fingerprint }}</code>
        <p v-if="fp.trusted">与信任记录一致</p>
        <template v-else
          ><label class="check"
            ><input
              type="checkbox"
              v-model="ack"
            />已通过独立渠道核对指纹</label
          ><button :disabled="!ack || s.busy.fp" @click="run('fp', trust)">
            信任此指纹
          </button></template
        >
      </div>
      <button
        class="primary wide"
        :disabled="s.busy.test"
        @click="run('test', testConn)"
      >
        <PlugZap />{{ s.busy.test ? '正在测试…' : '测试 SSH 连接' }}</button
      ><Evidence v-if="test" :value="test"
    /></template>
  </div>
</template>
