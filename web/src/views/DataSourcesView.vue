<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import PageHeader from '@/components/ui/PageHeader.vue'
import BaseModal from '@/components/ui/BaseModal.vue'
import StepUpDialog from '@/components/ui/StepUpDialog.vue'
import { ApiError, request, errorMessage } from '@/api/client'
import { assetsApi, type Asset } from '@/api/assetManagement'
import { confirmAction } from '@/composables/useConfirm'
interface Source { id: string; name: string; assetId: string; credentialId: string; database: string; tlsMode: string; enabled: boolean }
interface Result { columns: string[]; rows: unknown[][]; truncated: boolean; durationMs: number }
const sources = ref<Source[]>([]), assets = ref<Asset[]>([]), selected = ref<Source | null>(null)
const busy = ref(false), loading = ref(false), error = ref(''), verify = ref(false), editing = ref(false)
const draft = ref<Source>({ id: '', name: '', assetId: '', credentialId: '', database: '', tlsMode: 'verify-full', enabled: true })
const sql = ref('SELECT 1 AS connection_check'), result = ref<Result | null>(null), schema = ref<Result | null>(null)
const databaseAssets = computed(() => assets.value.filter(a => (a.protocol === 'mysql' || a.protocol === 'postgres') && a.enabled))
let retry: (() => Promise<void>) | undefined
async function load() { loading.value = true; error.value = ''; try { const [a, d] = await Promise.all([assetsApi.list(), request<{ items: Source[] }>('/datasources')]); assets.value = a.items; sources.value = d.items } catch (e) { error.value = errorMessage(e) } finally { loading.value = false } }
async function perform(action: () => Promise<void>) { busy.value = true; error.value = ''; try { await action() } catch (e) { if (e instanceof ApiError && e.code === 'step_up_required') { retry = action; verify.value = true } else error.value = errorMessage(e) } finally { busy.value = false } }
function verified() { verify.value = false; const action = retry; retry = undefined; if (action) void perform(action) }
function edit(source?: Source) { draft.value = source ? { ...source } : { id: '', name: '', assetId: '', credentialId: '', database: '', tlsMode: 'verify-full', enabled: true }; editing.value = true }
async function save() { const body = { ...draft.value, credentialId: assets.value.find(a => a.id === draft.value.assetId)?.credentialId || '' }; await perform(async () => { await request(`/datasources${body.id ? `/${body.id}` : ''}`, { method: body.id ? 'PATCH' : 'POST', body }); editing.value = false; await load() }) }
async function remove(source: Source) { if (!await confirmAction({ title: '删除数据源', message: `删除「${source.name}」？`, confirmText: '删除', danger: true })) return; await perform(async () => { await request(`/datasources/${source.id}`, { method: 'DELETE' }); if (selected.value?.id === source.id) selected.value = null; await load() }) }
function choose(source: Source) { selected.value = source; result.value = null; schema.value = null; error.value = '' }
async function query() { const id = selected.value?.id, statement = sql.value; if (!id) return; result.value = null; await perform(async () => { const answer = await request<Result>(`/datasources/${id}/query`, { method: 'POST', body: { sql: statement } }); if (selected.value?.id === id) result.value = answer }) }
async function inspectSchema() { const id = selected.value?.id; if (!id) return; schema.value = null; await perform(async () => { const answer = await request<Result>(`/datasources/${id}/schema`); if (selected.value?.id === id) schema.value = answer }) }
function cell(value: unknown) { return value === null ? 'NULL' : typeof value === 'object' ? JSON.stringify(value) : String(value) }
onMounted(load)
</script>
<template>
  <PageHeader eyebrow="ACCESS / DATABASE" title="数据源"><template #actions><button class="btn" :disabled="busy || loading" @click="load">刷新</button><button class="btn btn-primary" :disabled="busy" @click="edit()">登记数据源</button></template></PageHeader>
  <p v-if="error" class="notice notice-danger" role="alert">{{ error }}</p>
  <p class="notice">仅访问已登记的 PostgreSQL / MySQL 资产，使用数据库只读事务。请为源数据库准备专用只读账号。单次最多 200 行、2 MiB、100 列，8 秒超时。</p>
  <section class="panel"><p v-if="loading" class="panel-body" role="status">正在加载…</p><div v-else class="table-wrap"><table class="data-table"><thead><tr><th>名称</th><th>资产 / 数据库</th><th>TLS / 状态</th><th>操作</th></tr></thead><tbody><tr v-for="source in sources" :key="source.id"><td>{{ source.name }}</td><td>{{ assets.find(a => a.id === source.assetId)?.name || source.assetId }} · {{ source.database }}</td><td>{{ source.tlsMode }} · {{ source.enabled ? '启用' : '禁用' }}</td><td><div class="action-row"><button class="btn btn-sm" :disabled="busy || !source.enabled" @click="choose(source)">打开</button><button class="btn btn-sm" :disabled="busy" @click="edit(source)">编辑</button><button class="btn btn-sm btn-danger" :disabled="busy" @click="remove(source)">删除</button></div></td></tr><tr v-if="!sources.length"><td colspan="4">尚无数据源，请先在资产页登记 MySQL/PostgreSQL 目标并绑定数据库口令凭据。</td></tr></tbody></table></div></section>
  <section v-if="selected" class="panel datasource-workspace"><div class="panel-body stack"><h3>{{ selected.name }} · {{ selected.database }}</h3><p v-if="selected.tlsMode === 'disable'" class="notice notice-warning">此数据源已明确禁用数据库 TLS，请确认整个访问路径处于可信网络。</p><div><button class="btn" :disabled="busy" @click="inspectSchema">读取表结构</button></div><div v-if="schema" class="table-wrap"><table class="data-table"><thead><tr><th v-for="column in schema.columns" :key="column">{{ column }}</th></tr></thead><tbody><tr v-for="(row, index) in schema.rows" :key="index"><td v-for="(value, col) in row" :key="col">{{ cell(value) }}</td></tr></tbody></table><p v-if="schema.truncated" class="form-hint">表结构已截断到 200 行。</p></div><label for="readonly-sql">只读 SQL</label><textarea id="readonly-sql" v-model="sql" class="input mono sql-input" rows="7" maxlength="16384" spellcheck="false" :disabled="busy" /><p class="form-hint">支持单条 SELECT 和常用聚合/字符串函数。不支持多语句、注释、WITH、写操作、存储过程及任意函数。查询内容与结果不会写入审计日志。</p><div><button class="btn btn-primary" :disabled="busy || !sql.trim()" @click="query">{{ busy ? '请求中…' : '执行只读查询' }}</button></div><template v-if="result"><p role="status">返回 {{ result.rows.length }} 行 · {{ result.durationMs }} ms{{ result.truncated ? ' · 结果已达到限制并截断' : '' }}</p><div class="table-wrap"><table class="data-table"><thead><tr><th v-for="(column, index) in result.columns" :key="index">{{ column }}</th></tr></thead><tbody><tr v-for="(row, index) in result.rows" :key="index"><td v-for="(value, col) in row" :key="col"><span class="database-cell">{{ cell(value) }}</span></td></tr><tr v-if="!result.rows.length"><td :colspan="result.columns.length || 1">查询成功，无返回行。</td></tr></tbody></table></div></template></div></section>
  <BaseModal :open="editing && !verify" :title="draft.id ? '编辑数据源' : '登记数据源'" @close="!busy && (editing = false)"><form class="stack" @submit.prevent="save"><label>名称<input v-model="draft.name" class="input" required maxlength="200" /></label><label>数据库资产<select v-model="draft.assetId" class="select" required><option disabled value="">选择已启用的数据库资产</option><option v-for="asset in databaseAssets" :key="asset.id" :value="asset.id">{{ asset.name }} · {{ asset.protocol }}{{ asset.credentialId ? '' : '（未绑定凭据）' }}</option></select></label><label>数据库名<input v-model="draft.database" class="input" required maxlength="128" /></label><label>TLS<select v-model="draft.tlsMode" class="select"><option value="verify-full">启用并验证服务器证书（默认）</option><option value="disable">明确禁用 TLS（仅可信网络）</option></select></label><p class="form-hint">使用资产当前绑定的数据库口令凭据，用户名必须具有目标数据库只读权限。TLS 验证使用系统信任根和资产主机名。</p><label><input v-model="draft.enabled" type="checkbox" /> 启用</label><p v-if="error" class="notice notice-danger">{{ error }}</p><div class="form-actions"><button type="button" class="btn" :disabled="busy" @click="editing = false">取消</button><button class="btn btn-primary" :disabled="busy">保存</button></div></form></BaseModal>
  <StepUpDialog v-if="verify" title="访问数据源前验证" @verified="verified" @close="verify = false; retry = undefined" />
</template>
<style scoped>
.datasource-workspace { margin-top: 20px; }
.sql-input { width: 100%; resize: vertical; }
.database-cell { display: block; max-width: 30rem; max-height: 12rem; overflow: auto; white-space: pre-wrap; overflow-wrap: anywhere; }
</style>
