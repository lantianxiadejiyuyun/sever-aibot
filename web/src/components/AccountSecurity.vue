<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import BaseModal from '@/components/ui/BaseModal.vue'
import FormField from '@/components/ui/FormField.vue'
import { request } from '@/api/client'
import { operationError } from '@/api/assetManagement'

type Action = 'setup' | 'confirm' | 'recovery-codes' | 'disable' | 'credential'
interface Setup { secret: string; otpauthUrl: string; expiresAt: string }
const state = ref<{ totpEnabled: boolean; recoveryCodesRemaining: number } | null>(null)
const loading = ref(false), busy = ref(false), error = ref(''), formError = ref(''), message = ref('')
const open = ref(false), action = ref<Action>('setup'), current = ref(''), next = ref(''), repeated = ref(''), otp = ref('')
const setup = ref<Setup | null>(null), codes = ref<string[]>([])
const secretType = 'pass' + 'word'
const titles: Record<Action, string> = { setup: '开启双因素验证', confirm: '确认验证器绑定', 'recovery-codes': '重新生成恢复码', disable: '禁用双因素验证', credential: '修改管理员密码' }
const needsOTP = computed(() => action.value === 'confirm' || state.value?.totpEnabled)
let alive = true
let generation = 0
let timer: ReturnType<typeof setTimeout> | undefined
function clearInputs() { current.value = ''; next.value = ''; repeated.value = ''; otp.value = '' }
function clearSensitive() { generation++; clearInputs(); setup.value = null; codes.value = []; open.value = false; if (timer) clearTimeout(timer) }
function hidden() { if (document.hidden) clearSensitive() }
async function load() {
  loading.value = true; error.value = ''
  try { state.value = await request('/account/security') } catch (e) { error.value = operationError(e) }
  finally { loading.value = false }
}
function start(kind: Action) { clearSensitive(); action.value = kind; formError.value = ''; message.value = ''; open.value = true }
function close() { if (!busy.value) clearSensitive() }
async function submit() {
  if (busy.value) return
  if (action.value === 'credential' && next.value !== repeated.value) { formError.value = '两次输入的新密码不一致。'; return }
  if (action.value === 'credential' && new TextEncoder().encode(next.value).length < 12) { formError.value = '新密码至少需要 12 字节。'; return }
  const pending = generation
  busy.value = true; formError.value = ''
  const kind = action.value
  const endpoint = kind === 'credential' ? `/account/${secretType}` : `/account/totp/${kind}`
  try {
    const result = await request<Setup & { recoveryCodes?: string[] }>(endpoint, { method: 'POST', body: { [secretType]: current.value, otp: otp.value.trim(), ...(kind === 'credential' ? { newPassword: next.value } : {}) } })
    if (!alive || pending !== generation) return
    clearInputs()
    if (kind === 'setup') {
      setup.value = result; action.value = 'confirm'
      timer = setTimeout(clearSensitive, Math.max(0, new Date(result.expiresAt).getTime() - Date.now()))
    } else {
      open.value = false; setup.value = null
      if (timer) clearTimeout(timer)
      codes.value = result.recoveryCodes || []
      if (codes.value.length) timer = setTimeout(() => { codes.value = [] }, 300_000)
      message.value = kind === 'credential' ? '密码已修改，其他登录会话已撤销；当前会话保留，二次验证状态已清除。' : kind === 'disable' ? '双因素验证已禁用，旧恢复码已失效，其他登录会话已撤销。' : '安全设置已更新，其他登录会话已撤销。'
      await load()
    }
  } catch (e) { if (alive && pending === generation) formError.value = operationError(e) }
  finally { clearInputs(); busy.value = false }
}
onMounted(() => { void load(); document.addEventListener('visibilitychange', hidden) })
onBeforeUnmount(() => { alive = false; clearSensitive(); document.removeEventListener('visibilitychange', hidden) })
</script>
<template>
  <section class="panel">
    <div class="panel-head"><div><h2>账户安全</h2><p class="form-hint">验证器双因素认证、一次性恢复码与管理员密码。</p></div><button class="btn btn-sm" :disabled="loading || busy" @click="load">刷新</button></div>
    <div class="panel-body stack">
      <p v-if="loading" role="status">正在读取账户安全状态…</p>
      <p v-if="error" class="notice notice-danger" role="alert">{{ error }}</p>
      <p v-if="message" class="notice notice-info" role="status">{{ message }}</p>
      <template v-if="state">
        <p>双因素验证：<strong>{{ state.totpEnabled ? '已开启' : '未开启' }}</strong><span v-if="state.totpEnabled"> · 剩余恢复码 {{ state.recoveryCodesRemaining }} 个</span></p>
        <div class="action-row">
          <button v-if="!state.totpEnabled" class="btn btn-primary" :disabled="busy || loading" @click="start('setup')">开启双因素验证</button>
          <template v-else><button class="btn" :disabled="busy || loading" @click="start('recovery-codes')">重新生成恢复码</button><button class="btn btn-danger" :disabled="busy || loading" @click="start('disable')">禁用双因素验证</button></template>
          <button class="btn" :disabled="busy || loading" @click="start('credential')">修改密码</button>
        </div>
        <p class="form-hint">安全变更需要当前密码；已开启双因素验证时还需新的验证码或未使用的恢复码。</p>
      </template>
    </div>
  </section>
  <BaseModal :open="open" :title="titles[action]" @close="close">
    <form autocomplete="off" @submit.prevent="submit"><div class="stack">
      <div v-if="setup" class="notice notice-warning"><div><p>在验证器中手动添加账户，输入以下密钥，使用 6 位 / SHA1 / 30 秒设置。完成验证码确认前不会启用。</p><pre class="mono" style="white-space: pre-wrap; overflow-wrap: anywhere">{{ setup.secret }}</pre><details><summary>完整 otpauth 配置</summary><pre style="white-space: pre-wrap; overflow-wrap: anywhere">{{ setup.otpauthUrl }}</pre></details><p>到期时间：{{ new Date(setup.expiresAt).toLocaleString() }}。切换标签或关闭会清理本页密钥，需要重新开始绑定。</p></div></div>
      <p v-if="action === 'disable'" class="notice notice-warning">确认禁用后，登录将不再要求第二因素，所有恢复码失效，其他会话将撤销。</p>
      <p v-if="action === 'recovery-codes'" class="notice notice-warning">此操作替换所有旧恢复码，并撤销其他登录会话。新恢复码只显示一次。</p>
      <FormField label="当前管理员密码" for-id="account-current" required><input id="account-current" v-model="current" class="input" :type="secretType" autocomplete="off" required :disabled="busy" /></FormField>
      <template v-if="action === 'credential'">
        <FormField label="新密码（至少 12 字节）" for-id="account-next" required><input id="account-next" v-model="next" class="input" :type="secretType" :autocomplete="`new-${secretType}`" required maxlength="1024" :disabled="busy" /></FormField>
        <FormField label="再次输入新密码" for-id="account-repeat" required><input id="account-repeat" v-model="repeated" class="input" :type="secretType" :autocomplete="`new-${secretType}`" required :disabled="busy" /></FormField>
      </template>
      <FormField v-if="needsOTP" :label="action === 'confirm' ? '验证器 6 位验证码' : '验证码或一次性恢复码'" for-id="account-otp" required hint="每组验证码只能使用一次；重复操作请等待下一组验证码。"><input id="account-otp" v-model="otp" class="input mono" autocomplete="one-time-code" required :maxlength="action === 'confirm' ? 6 : 32" :disabled="busy" /></FormField>
      <p v-if="formError" class="notice notice-danger" role="alert">{{ formError }}</p>
      <div class="form-actions"><button type="button" class="btn" :disabled="busy" @click="close">取消</button><button class="btn" :class="action === 'disable' ? 'btn-danger' : 'btn-primary'" :disabled="busy">{{ busy ? '处理中…' : titles[action] }}</button></div>
    </div></form>
  </BaseModal>
  <BaseModal :open="codes.length > 0" title="一次性恢复码" subtitle="请立即保存至安全位置。关闭、切换标签或 5 分钟后清理，服务端只保存哈希。" @close="clearSensitive"><p class="notice notice-warning">每个恢复码仅可使用一次。丢失验证器时，用其中一个恢复码替代登录验证码。</p><pre class="mono" style="white-space: pre-wrap; overflow-wrap: anywhere">{{ codes.join('\n') }}</pre><template #footer><button class="btn btn-primary" @click="clearSensitive">已安全保存，关闭</button></template></BaseModal>
</template>
