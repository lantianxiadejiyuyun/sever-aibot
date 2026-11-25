<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import PageHeader from '@/components/ui/PageHeader.vue'
import BaseModal from '@/components/ui/BaseModal.vue'
import FormField from '@/components/ui/FormField.vue'
import StepUpDialog from '@/components/ui/StepUpDialog.vue'
import { assetsApi, credentialsApi, credentialKinds, credentialKindLabel, operationError, probeMessage, type Asset, type Credential } from '@/api/assetManagement'
import { confirmAction } from '@/composables/useConfirm'
import { session } from '@/stores/session'
const items = ref<Credential[]>([]), assets = ref<Asset[]>([]), query = ref(''), loading = ref(false), busy = ref(false), error = ref(''), assetError = ref('')
const editor = ref(false), editing = ref<Credential | null>(null), formError = ref('')
const form = reactive({ name: '', kind: credentialKinds[0].value, username: '', secret: '', publicMeta: '{}' })
const selected = ref<Credential | null>(null), operation = ref<'reveal' | 'test'>('reveal'), stepUp = ref(false), testOpen = ref(false), assetId = ref('')
const revealed = ref(''), revealOpen = ref(false), result = ref(''), resultTitle = ref('')
let revealTimer: ReturnType<typeof setTimeout> | undefined
let controller: AbortController | null = null
let active = true
const filtered = computed(() => items.value.filter(c => [c.name, c.username, credentialKindLabel(c.kind)].join(' ').toLowerCase().includes(query.value.toLowerCase())))
const boundAssets = computed(() => assets.value.filter(a => a.credentialId === selected.value?.id))
async function load() {
  loading.value = true; error.value = ''; assetError.value = ''
  const [c, a] = await Promise.allSettled([credentialsApi.list(), assetsApi.list()])
  if (c.status === 'fulfilled') items.value = c.value.items || []; else error.value = operationError(c.reason)
  if (a.status === 'fulfilled') assets.value = a.value.items || []; else { assets.value = []; assetError.value = operationError(a.reason) }
  loading.value = false
}
function edit(c: Credential | null) {
  editing.value = c; formError.value = ''
  Object.assign(form, { name: c?.name || '', kind: c?.kind || credentialKinds[0].value, username: c?.username || '', secret: '', publicMeta: JSON.stringify(c?.publicMeta || {}, null, 2) })
  editor.value = true
}
function closeEditor() { if (!busy.value) { form.secret = ''; editor.value = false } }
async function save() {
  formError.value = ''
  let publicMeta: Record<string, unknown>
  try {
    const parsed: unknown = JSON.parse(form.publicMeta)
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) throw new Error()
    publicMeta = parsed as Record<string, unknown>
  } catch { formError.value = '公开元数据必须是 JSON 对象。'; return }
  if (!form.name.trim() || (!editing.value && !form.secret)) { formError.value = '请填写名称和秘密内容。'; return }
  busy.value = true
  try {
    await credentialsApi.save(editing.value?.id || '', { name: form.name.trim(), username: form.username.trim(), publicMeta, ...(!editing.value ? { kind: form.kind } : {}), ...(form.secret ? { secret: form.secret } : {}) })
    editor.value = false; await load()
  } catch (e) { formError.value = operationError(e) }
  finally { form.secret = ''; busy.value = false }
}
async function remove(c: Credential) {
  if (!await confirmAction({ title: '删除凭据', message: `确认删除「${c.name}」？`, detail: '删除不可撤销。仍被资产引用的凭据需先解除绑定。', danger: true, confirmText: '删除' })) return
  busy.value = true; error.value = ''
  try { await credentialsApi.remove(c.id); await load() } catch (e) { error.value = operationError(e) } finally { busy.value = false }
}
function startReveal(c: Credential) { clearReveal(); selected.value = c; operation.value = 'reveal'; stepUp.value = true }
function startTest(c: Credential) { selected.value = c; assetId.value = ''; operation.value = 'test'; result.value = ''; testOpen.value = true }
function verifyTest() { testOpen.value = false; stepUp.value = true }
function clearReveal() { revealed.value = ''; revealOpen.value = false; if (revealTimer) clearTimeout(revealTimer); controller?.abort(); controller = null }
async function verified() {
  stepUp.value = false
  if (!selected.value) return
  const credential = selected.value
  busy.value = true; error.value = ''; result.value = ''; resultTitle.value = `${credential.name} · ${operation.value === 'reveal' ? '查看凭据' : '凭据测试'}`
  try {
    if (operation.value === 'reveal') {
      const pending = new AbortController()
      controller = pending
      const response = await credentialsApi.reveal(credential.id, pending.signal)
      if (!active || !session.user || pending.signal.aborted) return
      revealed.value = response.secret; revealOpen.value = true
      revealTimer = setTimeout(clearReveal, 30_000)
    } else {
      const response = await credentialsApi.test(credential.id, assetId.value)
      if (active) result.value = probeMessage(response)
    }
  } catch (e) { if (active && !(e instanceof DOMException && e.name === 'AbortError')) error.value = operationError(e) }
  finally { busy.value = false }
}
function clearSensitive() { clearReveal(); form.secret = ''; stepUp.value = false; editor.value = false }
function hidden() { if (document.hidden) clearSensitive() }
watch(() => session.user, user => { if (!user) clearSensitive() })
onMounted(() => { void load(); document.addEventListener('visibilitychange', hidden) })
onBeforeUnmount(() => { active = false; clearSensitive(); document.removeEventListener('visibilitychange', hidden) })
</script>
<template>
  <PageHeader eyebrow="MANAGE / CREDENTIALS" title="凭据中心"><template #actions><button class="btn" :disabled="loading || busy" @click="load">刷新</button><button class="btn btn-primary" :disabled="busy" @click="edit(null)">新建凭据</button></template></PageHeader>
  <p class="notice">列表仅显示元数据。秘密在控制面加密存储；查看需二次验证，SSH 私钥仅支持替换。</p>
  <p v-if="error" class="notice notice-danger" role="alert">{{ error }}</p>
  <p v-if="assetError" class="notice notice-warning" role="alert">无法读取测试目标：{{ assetError }}</p>
  <p v-if="result" class="notice" role="status">{{ resultTitle }}：{{ result }}</p>
  <section class="panel"><div class="filter-bar"><input v-model="query" class="input" type="search" placeholder="搜索名称、类型、用户名" aria-label="搜索凭据" /></div>
    <p v-if="loading" class="panel-body" role="status">正在加载凭据…</p><p v-else-if="!error && !filtered.length" class="panel-body">{{ items.length ? '没有匹配的凭据。' : '尚无凭据，创建后可在资产编辑中绑定。' }}</p>
    <div v-else class="table-wrap mobile-cards"><table class="data-table"><thead><tr><th>凭据</th><th>类型 / 用户</th><th>公开元数据</th><th>最后使用</th><th>操作</th></tr></thead><tbody><tr v-for="c in filtered" :key="c.id">
      <td data-label="凭据"><strong>{{ c.name }}</strong><div class="cell-sub mono">{{ c.id }}</div></td><td data-label="类型 / 用户">{{ credentialKindLabel(c.kind) }}<div class="cell-sub">{{ c.username || '无用户名' }}</div></td>
      <td data-label="公开元数据"><details><summary>查看元数据</summary><pre style="white-space: pre-wrap; overflow-wrap: anywhere">{{ JSON.stringify(c.publicMeta, null, 2) }}</pre><p class="cell-sub">加密密钥 ID：{{ c.keyId }}</p></details></td><td data-label="最后使用">{{ c.lastUsedAt ? new Date(c.lastUsedAt).toLocaleString() : '尚未使用' }}</td>
      <td data-label="操作"><div class="action-row"><button class="btn btn-sm" :disabled="busy" @click="edit(c)">编辑 / 替换</button><button class="btn btn-sm" :disabled="busy || c.kind === 'ssh_key'" :title="c.kind === 'ssh_key' ? 'SSH 私钥禁止导出，仅支持替换' : '二次验证后显示 30 秒'" @click="startReveal(c)">{{ c.kind === 'ssh_key' ? '私钥不可导出' : '查看秘密' }}</button><button class="btn btn-sm" :disabled="busy || loading || !!assetError" @click="startTest(c)">测试</button><button class="btn btn-sm btn-danger" :disabled="busy" @click="remove(c)">删除</button></div></td>
    </tr></tbody></table></div>
  </section>
  <BaseModal :open="editor" :title="editing ? '编辑 / 替换凭据' : '新建凭据'" wide @close="closeEditor">
    <form autocomplete="off" @submit.prevent="save"><fieldset :disabled="busy" style="border: 0; padding: 0; margin: 0"><div class="form-grid">
      <FormField label="名称" for-id="credential-name" required><input id="credential-name" v-model="form.name" class="input" required /></FormField>
      <FormField label="类型" for-id="credential-kind" hint="创建后不可修改类型。"><select id="credential-kind" v-model="form.kind" class="select" :disabled="!!editing"><option v-for="k in credentialKinds" :key="k.value" :value="k.value">{{ k.label }}</option></select></FormField>
      <FormField label="用户名" for-id="credential-user"><input id="credential-user" v-model="form.username" class="input" autocomplete="off" /></FormField>
      <FormField :label="editing ? '替换秘密（留空保留原值）' : '秘密内容'" for-id="credential-secret" :required="!editing" span2 hint="提交后清空输入。不要在公开元数据中填写秘密。">
        <textarea v-if="form.kind === 'ssh_key'" id="credential-secret" v-model="form.secret" class="textarea mono" rows="5" :required="!editing" autocomplete="off" spellcheck="false" placeholder="粘贴 PEM 私钥"></textarea>
        <input v-else id="credential-secret" v-model="form.secret" class="input" :type="'pass' + 'word'" :required="!editing" :autocomplete="'new-pass' + 'word'" />
      </FormField>
      <FormField label="公开元数据（JSON 对象）" for-id="credential-meta" span2 hint="仅填写非秘密信息，例如 fingerprint、panelUrl、scope。列表会展示这些字段。"><textarea id="credential-meta" v-model="form.publicMeta" class="textarea mono" rows="4" spellcheck="false"></textarea></FormField>
    </div></fieldset><p v-if="formError" class="notice notice-danger" role="alert">{{ formError }}</p><div class="form-actions"><button type="button" class="btn" :disabled="busy" @click="closeEditor">取消</button><button class="btn btn-primary" :disabled="busy">{{ busy ? '保存中…' : '保存凭据' }}</button></div></form>
  </BaseModal>
  <BaseModal :open="testOpen" title="选择凭据测试目标" @close="testOpen = false"><form @submit.prevent="verifyTest"><p>只允许测试已绑定「{{ selected?.name }}」的资产。SSH 测试需已固定可信主机指纹。</p><FormField label="目标资产" for-id="credential-test-asset"><select id="credential-test-asset" v-model="assetId" class="select" required><option value="" disabled>选择资产</option><option v-for="a in boundAssets" :key="a.id" :value="a.id" :disabled="!a.enabled">{{ a.name }} · {{ a.protocol }}{{ a.enabled ? '' : '（已禁用）' }}</option></select></FormField><p v-if="!boundAssets.length" class="notice notice-warning">没有绑定此凭据的资产，请先在资产编辑中绑定。</p><div class="form-actions"><button class="btn btn-primary" :disabled="!assetId">验证身份并测试</button></div></form></BaseModal>
  <StepUpDialog v-if="stepUp" :title="operation === 'reveal' ? '查看秘密前验证' : '测试凭据前验证'" @verified="verified" @close="stepUp = false" />
  <BaseModal :open="revealOpen" :title="`${selected?.name || ''} · 凭据明文`" subtitle="30 秒后自动隐藏；切换标签页、关闭或离页立即清理" @close="clearReveal"><p class="notice notice-warning">请确保周围环境安全。明文不会写入浏览器持久存储。</p><pre class="mono" style="white-space: pre-wrap; overflow-wrap: anywhere; user-select: text">{{ revealed }}</pre><template #footer><button class="btn btn-primary" @click="clearReveal">立即隐藏</button></template></BaseModal>
</template>
