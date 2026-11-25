<script setup lang="ts">
import { onMounted, ref } from 'vue'
import PageHeader from '@/components/ui/PageHeader.vue'
import BaseModal from '@/components/ui/BaseModal.vue'
import { request, errorMessage } from '@/api/client'
import { confirmAction } from '@/composables/useConfirm'
interface Channel { id: string; name: string; kind: string; enabled: boolean }
interface Notification { id: string; message: string; read: boolean; createdAt: number }
interface Delivery { id: string; channelId: string; state: string; attempts: number; nextAttempt: number; lastError: string; createdAt: number }
interface Log { id: number; outboxId: string; attempt: number; state: string; message: string; createdAt: number }
const channels = ref<Channel[]>([]), notifications = ref<Notification[]>([]), deliveries = ref<Delivery[]>([]), logs = ref<Log[]>([])
const loading = ref(false), busy = ref(false), error = ref(''), formError = ref(''), opened = ref(false)
const form = ref({ id: '', name: '', kind: 'webhook', enabled: false, webhookUrl: '' })
function date(t: number) { return t ? new Date(t * 1000).toLocaleString() : '—' }
function state(s: string) { return ({ pending: '等待重试 / 发送', sending: '发送中', sent: '已发送', failed: '失败' } as Record<string, string>)[s] || s }
async function load() {
  loading.value = true; error.value = ''
  const results = await Promise.allSettled([request<{items: Channel[]}>('/notify/channels'), request<{items: Notification[]}>('/notifications'), request<{items: Delivery[]}>('/notify/outbox'), request<{items: Log[]}>('/notify/logs')])
  const [c, n, d, l] = results
  if (c.status === 'fulfilled') channels.value = c.value.items
  if (n.status === 'fulfilled') notifications.value = n.value.items
  if (d.status === 'fulfilled') deliveries.value = d.value.items
  if (l.status === 'fulfilled') logs.value = l.value.items
  results.forEach((v, i) => { if (v.status === 'rejected') error.value += `${['频道', '站内通知', '发送队列', '发送日志'][i]}：${errorMessage(v.reason)}；` })
  loading.value = false
}
function edit(c?: Channel) { form.value = c ? { ...c, webhookUrl: '' } : { id: '', name: '', kind: 'webhook', enabled: false, webhookUrl: '' }; formError.value = ''; opened.value = true }
async function save() {
  busy.value = true; formError.value = ''
  try { await request(form.value.id ? `/notify/channels/${form.value.id}` : '/notify/channels', { method: form.value.id ? 'PUT' : 'POST', body: form.value }); form.value.webhookUrl = ''; opened.value = false; await load() }
  catch (e) { formError.value = errorMessage(e) } finally { busy.value = false }
}
async function action(path: string, method: 'POST' | 'DELETE' = 'POST') {
  busy.value = true; error.value = ''
  try { await request(path, { method }); await load() } catch (e) { error.value = errorMessage(e) } finally { busy.value = false }
}
async function remove(c: Channel) { if (await confirmAction({ title: '删除通知频道', message: `删除「${c.name}」？`, detail: '历史日志将保留，未发送的通知将无法投递到该频道。', danger: true, confirmText: '删除' })) await action(`/notify/channels/${c.id}`, 'DELETE') }
function close() { if (!busy.value) { opened.value = false; form.value.webhookUrl = '' } }
onMounted(load)
</script>
<template>
  <PageHeader eyebrow="SYSTEM / NOTIFICATIONS" title="通知"><template #actions><button class="btn" :disabled="busy || loading" @click="load">刷新</button><button class="btn btn-primary" :disabled="busy || loading" @click="edit()">新建频道</button></template></PageHeader>
  <p v-if="error" class="notice notice-danger" role="alert">{{ error }}</p><p v-if="loading" role="status">正在加载通知…</p>
  <section class="panel"><div class="panel-header"><h2>站内通知</h2></div><div class="table-wrap"><table class="data-table"><thead><tr><th>时间</th><th>消息</th><th>状态</th></tr></thead><tbody><tr v-for="n in notifications" :key="n.id"><td>{{ date(n.createdAt) }}</td><td>{{ n.message }}</td><td><span v-if="n.read">已读</span><button v-else class="btn btn-sm" :disabled="busy" @click="action(`/notifications/${n.id}/read`)">标记已读</button></td></tr></tbody></table></div><p v-if="!loading && !notifications.length" class="panel-body">暂无通知。检查状态变化时会自动生成通知。</p></section>
  <section class="panel"><div class="panel-header"><h2>通知频道</h2></div><p class="panel-body">支持飞书群机器人和通用 Webhook。启用后接收检查状态变化；保存配置不发送测试消息。回调必须使用公网 HTTPS 443 地址。</p><div class="table-wrap"><table class="data-table"><thead><tr><th>名称</th><th>类型</th><th>状态</th><th>操作</th></tr></thead><tbody><tr v-for="c in channels" :key="c.id"><td>{{ c.name }}</td><td>{{ c.kind === 'feishu' ? '飞书群机器人' : '通用 Webhook' }}</td><td>{{ c.enabled ? '启用' : '暂停' }}</td><td><div class="action-row"><button class="btn btn-sm" :disabled="busy" @click="edit(c)">编辑</button><button class="btn btn-sm btn-danger" :disabled="busy" @click="remove(c)">删除</button></div></td></tr></tbody></table></div><p v-if="!loading && !channels.length" class="panel-body">暂无外部通知频道，站内通知仍正常生成。</p></section>
  <section class="panel"><div class="panel-header"><h2>发送队列</h2></div><p class="panel-body">失败时指数退避，最多尝试 5 次。最终失败后可手动重试；远端应按事件 ID 去重，网络中断后可能重复投递。显示最近 200 条。</p><div class="table-wrap"><table class="data-table"><thead><tr><th>创建时间</th><th>频道</th><th>状态 / 次数</th><th>下次尝试 / 错误</th><th>操作</th></tr></thead><tbody><tr v-for="d in deliveries" :key="d.id"><td>{{ date(d.createdAt) }}</td><td>{{ channels.find(c => c.id === d.channelId)?.name || '频道已删除' }}</td><td>{{ state(d.state) }} · {{ d.attempts }}</td><td>{{ date(d.nextAttempt) }}<div class="cell-sub">{{ d.lastError }}</div></td><td><button v-if="d.state === 'failed'" class="btn btn-sm" :disabled="busy" @click="action(`/notify/outbox/${encodeURIComponent(d.id)}/retry`)">重新入队</button></td></tr></tbody></table></div><p v-if="!loading && !deliveries.length" class="panel-body">暂无待投递或历史消息。</p></section>
  <section class="panel"><div class="panel-header"><h2>发送日志</h2></div><div class="table-wrap"><table class="data-table"><thead><tr><th>时间</th><th>投递记录</th><th>尝试</th><th>结果</th></tr></thead><tbody><tr v-for="l in logs" :key="l.id"><td>{{ date(l.createdAt) }}</td><td class="delivery-id">{{ l.outboxId }}</td><td>{{ l.attempt }}</td><td>{{ state(l.state) }}<div class="cell-sub">{{ l.message || '投递成功' }}</div></td></tr></tbody></table></div><p v-if="!loading && !logs.length" class="panel-body">暂无发送日志。</p></section>
  <BaseModal :open="opened" :title="form.id ? '编辑频道' : '新建频道'" @close="close"><form class="stack" @submit.prevent="save"><p v-if="formError" class="notice notice-danger" role="alert">{{ formError }}</p><label>名称<input v-model="form.name" class="input" maxlength="120" required /></label><label>类型<select v-model="form.kind" class="select"><option value="webhook">通用 Webhook</option><option value="feishu">飞书群机器人</option></select></label><label>Webhook 地址<input v-model="form.webhookUrl" class="input" type="password" autocomplete="new-password" :required="!form.id" :placeholder="form.id ? '留空保留现有地址' : 'https://…'" /></label><p class="form-hint">完整地址加密保存，列表不返回地址或机器人令牌。编辑时留空保留原值。</p><label><input v-model="form.enabled" type="checkbox" /> 启用频道并接收后续通知</label><div class="action-row"><button type="submit" class="btn btn-primary" :disabled="busy">{{ busy ? '保存中…' : '保存' }}</button><button type="button" class="btn" :disabled="busy" @click="close">取消</button></div></form></BaseModal>
</template>
<style scoped>.panel { margin-bottom: 20px; }.stack { display: grid; gap: 14px; }.stack label { display: grid; gap: 6px; }.stack label:has(input[type=checkbox]) { display: block; }.delivery-id { overflow-wrap: anywhere; max-width: 300px; }</style>
