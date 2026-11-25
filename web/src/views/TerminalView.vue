<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, RouterLink } from 'vue-router'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import PageHeader from '@/components/ui/PageHeader.vue'
import StepUpDialog from '@/components/ui/StepUpDialog.vue'
import TerminalRecording from '@/components/TerminalRecording.vue'
import { request, getCsrfToken, errorMessage } from '@/api/client'

interface AccessSession { id: string; assetId: string; actor: string; startedAt: string; endedAt: string; recordingPurgedAt: string; result: string; bytesIn: number; bytesOut: number }
const route = useRoute()
const host = ref<HTMLElement | null>(null), error = ref(''), status = ref('未连接'), busy = ref(false), verify = ref(false)
const sessions = ref<AccessSession[]>([]), current = ref(''), name = ref('SSH 终端'), replay = ref('')
let term: Terminal | undefined, fit: FitAddon | undefined, ws: WebSocket | undefined, observer: ResizeObserver | undefined
let disposed = false
const recordingAction = ref<{ id: string; download: boolean } | null>(null)
function verifyRecording(id: string, download: boolean) { recordingAction.value = { id, download }; verify.value = true }
function cancelVerification() { verify.value = false; recordingAction.value = null }
async function verifiedAction() {
  const action = recordingAction.value
  cancelVerification()
  if (!action) { await connect(); return }
  if (action.download) await recording(action.id)
  else replay.value = action.id
}
const encoder = new TextEncoder()
async function history() { try { sessions.value = (await request<{ items: AccessSession[] }>('/access/sessions')).items.filter(s => s.assetId === String(route.params.id)) } catch (e) { error.value = errorMessage(e) } }
async function connect() {
  verify.value = false; busy.value = true; error.value = ''
  try {
    const assetId = String(route.params.id)
    const row = await request<AccessSession>('/access/sessions', { method: 'POST', body: { assetId } })
    if (disposed || String(route.params.id) !== assetId) { await request(`/access/sessions/${row.id}/terminate`, { method: 'POST', body: {} }); return }
    current.value = row.id
    const url = new URL(`/api/v1/access/sessions/${row.id}/ws`, location.origin); url.protocol = location.protocol === 'https:' ? 'wss:' : 'ws:'
    const socket = new WebSocket(url); ws = socket; socket.binaryType = 'arraybuffer'; status.value = '正在连接'; term?.clear()
    socket.onopen = () => { if (ws !== socket || disposed) return; socket.send(JSON.stringify({ csrf: getCsrfToken() })); status.value = '已连接'; fit?.fit(); resize(); term?.focus() }
    socket.onmessage = e => { if (ws !== socket || disposed) return; if (e.data instanceof ArrayBuffer) term?.write(new Uint8Array(e.data)); else { error.value = String(e.data); term?.writeln(String(e.data)) } }
    socket.onerror = () => { if (ws === socket && !disposed) error.value = '终端连接失败，请检查身份验证、主机指纹和访问路径。' }
    socket.onclose = () => { if (ws !== socket || disposed) return; status.value = '已断开'; current.value = ''; void history() }
  } catch (e) { error.value = errorMessage(e) } finally { busy.value = false }
}
function resize() { if (ws?.readyState === WebSocket.OPEN && term) ws.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows })) }
async function stop(id = current.value) { if (!id) return; try { await request(`/access/sessions/${id}/terminate`, { method: 'POST', body: {} }); if (id === current.value) ws?.close(); await history() } catch (e) { error.value = errorMessage(e) } }
async function recording(id: string) {
  try {
    const response = await fetch(`/api/v1/access/sessions/${id}/recording`, { credentials: 'same-origin' })
    if (!response.ok) { const body = await response.json(); throw new Error(body.error?.message || '录制下载失败') }
    const url = URL.createObjectURL(await response.blob()), link = document.createElement('a'); link.href = url; link.download = `${id}.cast`; link.click(); setTimeout(() => URL.revokeObjectURL(url), 1000)
  } catch (e) { error.value = errorMessage(e) }
}
onMounted(async () => {
  await nextTick(); if (!host.value) return
  term = new Terminal({ cols: 120, rows: 30, cursorBlink: true, fontSize: 14, fontFamily: 'Consolas, monospace', scrollback: 3000, convertEol: false })
  fit = new FitAddon(); term.loadAddon(fit); term.open(host.value); fit.fit()
  term.onData(data => { if (ws?.readyState === WebSocket.OPEN) ws.send(encoder.encode(data)) })
  observer = new ResizeObserver(() => { fit?.fit(); resize() }); observer.observe(host.value)
  try { name.value = (await request<{ name: string }>(`/assets/${encodeURIComponent(String(route.params.id))}`)).name } catch (e) { error.value = errorMessage(e) }
  await history()
})
watch(() => route.params.id, async id => {
  replay.value = ''; cancelVerification()
  const previous = ws; ws = undefined; previous?.close(); current.value = ''; status.value = '未连接'; error.value = ''; term?.clear()
  try { const asset = await request<{ name: string }>(`/assets/${encodeURIComponent(String(id))}`); if (String(route.params.id) === String(id)) name.value = asset.name } catch (e) { error.value = errorMessage(e) }
  await history()
})
onBeforeUnmount(() => { disposed = true; ws?.close(); observer?.disconnect(); term?.dispose() })
</script>
<template>
  <PageHeader eyebrow="ACCESS / TERMINAL" :title="name"><template #actions><RouterLink class="btn" to="/assets">返回资产</RouterLink><button class="btn btn-primary" :disabled="busy || !!current" @click="verify = true">验证身份并连接</button><button class="btn btn-danger" :disabled="!current" @click="stop()">终止会话</button></template></PageHeader>
  <p v-if="error" class="notice notice-danger" role="alert">{{ error }}</p>
  <section class="panel"><div class="panel-body"><p role="status">{{ status }} · 最长 1 小时，15 分钟无输入自动断开。终端输出将录制，单次上限 16 MiB。</p><p class="form-hint">请先在资产详情核对并固定 SSH 主机指纹。此终端仅执行你交互输入的操作；退出页面即断开。</p></div><div ref="host" class="terminal-host" aria-label="交互式 SSH 终端"></div></section>
  <section class="panel terminal-history"><div class="panel-body"><h3>访问历史</h3><button class="btn btn-sm" @click="history">刷新</button></div><div class="table-wrap"><table class="data-table"><thead><tr><th>开始时间</th><th>操作者</th><th>结果</th><th>输出</th><th>操作</th></tr></thead><tbody><tr v-for="item in sessions" :key="item.id"><td>{{ new Date(item.startedAt).toLocaleString() }}</td><td>{{ item.actor }}</td><td>{{ item.result || '进行中' }}</td><td>{{ item.bytesOut }} B</td><td><span v-if="item.recordingPurgedAt" class="muted">录制已过期清理</span><template v-else><button class="btn btn-sm" @click="verifyRecording(item.id, true)">下载录制</button><button class="btn btn-sm" @click="verifyRecording(item.id, false)">回放</button></template><button v-if="!item.endedAt" class="btn btn-sm btn-danger" @click="stop(item.id)">终止</button></td></tr><tr v-if="!sessions.length"><td colspan="5">暂无会话记录</td></tr></tbody></table></div></section>
  <TerminalRecording v-if="replay" :key="replay" :session-id="replay" @close="replay = ''" />
  <StepUpDialog v-if="verify" :title="recordingAction ? '查看录制前验证' : '连接终端前验证'" @verified="verifiedAction" @close="cancelVerification" />
</template>
<style scoped>
.terminal-host { height: min(65vh, 650px); min-height: 300px; padding: 12px; background: #000; overflow: hidden; }
.terminal-history { margin-top: 20px; }
</style>
