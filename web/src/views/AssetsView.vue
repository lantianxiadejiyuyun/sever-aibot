<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import PageHeader from '@/components/ui/PageHeader.vue'
import BaseModal from '@/components/ui/BaseModal.vue'
import StepUpDialog from '@/components/ui/StepUpDialog.vue'
import AssetForm from '@/components/forms/AssetForm.vue'
import { assetsApi, credentialsApi, operationError, probeMessage, type Asset, type AssetInput, type Credential, type AssetRoute } from '@/api/assetManagement'
import { devicesApi } from '@/api/endpoints'
import { ApiError } from '@/api/client'
import type { Device } from '@/api/types'
import { confirmAction } from '@/composables/useConfirm'
import { copyText } from '@/utils/clipboard'
const assets = ref<Asset[]>([]), credentials = ref<Credential[]>([]), devices = ref<Device[]>([])
const loading = ref(false), busy = ref(false), error = ref(''), referenceError = ref(''), query = ref(''), tag = ref(''), device = ref('')
const editorOpen = ref(false), editing = ref<Asset | null>(null), formError = ref('')
const detail = ref<Asset | null>(null), detailError = ref(''), route = ref<AssetRoute | null>(null), routeError = ref(''), config = ref(''), result = ref('')
const observed = ref<{ fingerprint: string; algorithm: string } | null>(null), stepUpOpen = ref(false)
const pendingAction = ref<'check' | 'config' | 'discover' | 'pin'>('pin')
const filtered = computed(() => assets.value.filter(a => (!query.value || [a.name, a.address, a.notes].join(' ').toLowerCase().includes(query.value.toLowerCase())) && (!tag.value || a.tags.includes(tag.value)) && (!device.value || a.viaDeviceId === device.value)))
async function load() {
  loading.value = true; error.value = ''; referenceError.value = ''
  const results = await Promise.allSettled([assetsApi.list(), credentialsApi.list(), devicesApi.list()])
  const [a, c, d] = results
  if (a.status === 'fulfilled') assets.value = a.value.items || []; else error.value = operationError(a.reason)
  if (c.status === 'fulfilled') credentials.value = c.value.items || []; else referenceError.value += `凭据列表不可用：${operationError(c.reason)} `
  if (d.status === 'fulfilled') devices.value = d.value.items || []; else referenceError.value += `设备列表不可用：${operationError(d.reason)}`
  loading.value = false
}
function edit(asset: Asset | null) { editing.value = asset; formError.value = ''; editorOpen.value = true }
async function save(input: AssetInput) {
  busy.value = true; formError.value = ''
  try { await assetsApi.save(editing.value?.id || '', input); editorOpen.value = false; await load() }
  catch (e) { formError.value = operationError(e) } finally { busy.value = false }
}
async function remove(a: Asset) {
  if (!await confirmAction({ title: '删除资产', message: `确认删除「${a.name}」？`, detail: '被其他资产用作跳板时，服务端会拒绝删除。', danger: true, confirmText: '删除' })) return
  busy.value = true; error.value = ''
  try { await assetsApi.remove(a.id); await load() } catch (e) { error.value = operationError(e) } finally { busy.value = false }
}
async function show(a: Asset) {
  detail.value = a; detailError.value = ''; route.value = null; routeError.value = ''; config.value = ''; result.value = ''; observed.value = null; busy.value = true
  const [fresh, path] = await Promise.allSettled([assetsApi.get(a.id), assetsApi.route(a.id)])
  if (fresh.status === 'fulfilled') detail.value = fresh.value; else detailError.value = operationError(fresh.reason)
  if (path.status === 'fulfilled') route.value = path.value; else routeError.value = operationError(path.reason)
  busy.value = false
}
async function action(kind: 'check' | 'config' | 'discover' | 'pin') {
  if (!detail.value) return
  busy.value = true; detailError.value = ''; result.value = ''
  try {
    const id = detail.value.id
    if (kind === 'check') result.value = probeMessage(await assetsApi.check(id))
    if (kind === 'config') config.value = await assetsApi.config(id)
    if (kind === 'discover') observed.value = await assetsApi.discover(id)
    if (kind === 'pin' && observed.value) { await assetsApi.pin(id, observed.value.fingerprint); detail.value = await assetsApi.get(id); observed.value = null; result.value = '主机指纹已固定'; await load() }
  } catch (e) {
    detailError.value = operationError(e)
    if (e instanceof ApiError && e.code === 'step_up_required') { pendingAction.value = kind; stepUpOpen.value = true }
  } finally { busy.value = false }
}
async function copyConfig() { result.value = await copyText(config.value) ? 'SSH config 已复制' : '复制失败，请手动选择配置文本。' }
function closeDetail() { if (!busy.value) { detail.value = null; observed.value = null } }
function verifiedAction() { stepUpOpen.value = false; void action(pendingAction.value) }
function confirmPin() { pendingAction.value = 'pin'; stepUpOpen.value = true }
onMounted(load)
</script>
<template>
  <PageHeader eyebrow="MANAGE / ASSETS" title="资产"><template #actions><button class="btn" :disabled="loading || busy" @click="load">刷新</button><RouterLink to="/credentials" class="btn">凭据中心</RouterLink><button class="btn btn-primary" :disabled="loading || busy" @click="edit(null)">新建资产</button></template></PageHeader>
  <p v-if="error" class="notice notice-danger" role="alert">{{ error }}</p>
  <p v-if="referenceError" class="notice notice-warning" role="alert">{{ referenceError }}<button class="btn btn-sm" :disabled="loading" @click="load">重试</button></p>
  <section class="panel">
    <div class="filter-bar"><input v-model="query" class="input" type="search" placeholder="搜索名称、地址、备注" aria-label="搜索资产" /><input v-model="tag" class="input" placeholder="筛选标签" aria-label="筛选标签" /><select v-model="device" class="select" aria-label="入口设备"><option value="">所有入口设备</option><option v-for="d in devices" :key="d.id" :value="d.id">{{ d.name }}</option></select></div>
    <p v-if="loading" class="panel-body" role="status">正在加载资产…</p>
    <p v-else-if="!error && !filtered.length" class="panel-body">{{ assets.length ? '没有匹配的资产，请调整筛选条件。' : '尚无资产，点击“新建资产”登记访问目标。' }}</p>
    <div v-else class="table-wrap mobile-cards"><table class="data-table"><thead><tr><th>资产</th><th>目标</th><th>状态 / 标签</th><th>绑定凭据</th><th>操作</th></tr></thead><tbody><tr v-for="a in filtered" :key="a.id">
      <td data-label="资产"><button class="btn btn-sm" :disabled="busy" @click="show(a)">{{ a.name }}</button></td><td data-label="目标">{{ a.protocol }} · {{ a.address }}:{{ a.port }}</td><td data-label="状态 / 标签">{{ a.enabled ? '启用' : '禁用' }}<div class="cell-sub">{{ a.tags || '无标签' }}</div></td><td data-label="绑定凭据">{{ a.credentialId ? credentials.find(c => c.id === a.credentialId)?.name || `${a.credentialId}（不可用）` : '未绑定' }}</td>
      <td data-label="操作"><div class="action-row"><button class="btn btn-sm" :disabled="busy" @click="show(a)">详情 / 检查</button><button class="btn btn-sm" :disabled="busy" @click="edit(a)">编辑</button><button class="btn btn-sm btn-danger" :disabled="busy" @click="remove(a)">删除</button></div></td>
    </tr></tbody></table></div>
  </section>
  <BaseModal :open="editorOpen" :title="editing ? '编辑资产' : '新建资产'" wide @close="!busy && (editorOpen = false)"><AssetForm v-if="editorOpen" :initial="editing" :assets="assets" :devices="devices" :credentials="credentials" :busy="busy" :error="formError" @save="save" @cancel="editorOpen = false" /></BaseModal>
  <BaseModal :open="!!detail && !stepUpOpen" title="资产详情与访问路径" wide @close="closeDetail">
    <div v-if="detail" class="stack">
      <h3>{{ detail.name }}</h3><p class="mono">{{ detail.protocol }} · {{ detail.address }}:{{ detail.port }}</p><p>{{ detail.notes || '无备注' }}</p>
      <p>凭据：{{ credentials.find(c => c.id === detail?.credentialId)?.name || detail.credentialId || '未绑定' }}</p>
      <p>主机指纹：<code>{{ detail.hostKey || '尚未固定，SSH 认证不可用' }}</code></p>
      <p v-if="busy" role="status">正在处理…</p><p v-if="detailError" class="notice notice-danger" role="alert">{{ detailError }}</p><p v-if="routeError" class="notice notice-warning" role="alert">路径不可用：{{ routeError }}</p>
      <div v-if="route"><h4>解析后的访问顺序</h4><ol><li>控制面{{ route.viaDeviceId ? ` → Agent ${devices.find(d => d.id === route?.viaDeviceId)?.name || route.viaDeviceId}` : '直连' }}</li><li v-for="hop in route.hops || []" :key="hop.id">{{ hop.name }} · {{ hop.address }}:{{ hop.port }}</li><li>目标 {{ route.endpoint.address }}:{{ route.endpoint.port }}</li></ol></div>
      <div class="action-row"><button class="btn" :disabled="busy || !detail.enabled" @click="action('check')">连通检查</button><button class="btn" :disabled="busy || detail.protocol !== 'ssh'" @click="action('config')">生成 SSH config</button><button class="btn" :disabled="busy || detail.protocol !== 'ssh' || !detail.enabled" @click="action('discover')">发现主机指纹</button><RouterLink v-if="detail.protocol === 'ssh' && detail.enabled" :to="`/terminal/${encodeURIComponent(detail.id)}`" class="btn">打开终端</RouterLink></div>
      <p v-if="!detail.enabled" class="form-hint">资产已禁用，无法执行连通检查或连接。</p>
      <div v-if="observed" class="notice notice-warning"><div><p>观察到 {{ observed.algorithm }}：<code>{{ observed.fingerprint }}</code></p><p>请通过可信渠道核对指纹。发现结果不会自动建立信任。</p><button class="btn" :disabled="busy" @click="confirmPin">已核对，验证身份并固定指纹</button></div></div>
      <p v-if="result" class="notice" role="status">{{ result }}</p>
      <div v-if="config"><h4>SSH config</h4><p class="form-hint">不含口令或私钥。请按配置准备本地密钥；Agent 入口注释不等于本地已有可用代理。</p><pre style="white-space: pre-wrap; overflow-wrap: anywhere">{{ config }}</pre><button class="btn" @click="copyConfig">复制配置</button></div>
    </div>
  </BaseModal>
  <StepUpDialog v-if="stepUpOpen" title="访问资产前验证身份" @verified="verifiedAction" @close="stepUpOpen = false" />
</template>
