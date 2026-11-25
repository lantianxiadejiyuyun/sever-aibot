<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { request, errorMessage, ApiError } from '@/api/client'
import StepUpDialog from '@/components/ui/StepUpDialog.vue'
import { formatTime } from '@/utils/format'
interface Settings { siteName: string; announcement: string; auditRetentionDays: number; recordingRetentionDays: number }
interface Session { id: string; ip: string; userAgent: string; createdAt: string; lastSeenAt: string; expiresAt: string; current: boolean }
const settings = ref<Settings>({ siteName: '', announcement: '', auditRetentionDays: 180, recordingRetentionDays: 30 })
const sessions = ref<Session[]>([]), error = ref(''), notice = ref(''), busy = ref(false), stepUp = ref(false)
let retry: (() => Promise<void>) | null = null
async function load() { try { const [a,b] = await Promise.all([request<Settings>('/settings'), request<{items: Session[]}>('/auth/sessions')]); settings.value=a; sessions.value=b.items } catch(e) { error.value=errorMessage(e) } }
async function execute(action: () => Promise<void>) { busy.value=true; error.value=''; notice.value=''; try { await action() } catch(e) { if(e instanceof ApiError && e.code==='step_up_required'){ retry=action; stepUp.value=true } else error.value=errorMessage(e) } finally { busy.value=false } }
async function save() { await execute(async()=>{ settings.value=await request<Settings>('/settings',{method:'PUT',body:settings.value}); notice.value='设置已保存' }) }
async function revoke(s: Session) { if(!window.confirm(`结束${s.current?'当前':'此'}登录会话？`)) return; await execute(async()=>{ await request(`/auth/sessions/${encodeURIComponent(s.id)}`,{method:'DELETE'}); if(s.current) window.location.assign('/login'); else await load() }) }
async function verified() { stepUp.value=false; const fn=retry; retry=null; if(fn) await execute(fn) }
onMounted(load)
</script>
<template>
<section class="panel"><div class="panel-head"><h2 class="panel-title">站点设置</h2></div><form class="panel-body stack" @submit.prevent="save">
<label>站点名称<input v-model="settings.siteName" class="input" required maxlength="100" /></label>
<label>公告<textarea v-model="settings.announcement" class="input" maxlength="4000" rows="3"></textarea></label>
<label>审计保留天数<input v-model.number="settings.auditRetentionDays" type="number" min="7" max="3650" class="input" required /></label>
<label>终端录制保留天数<input v-model.number="settings.recordingRetentionDays" type="number" min="1" max="3650" class="input" required /></label>
<p class="form-hint">启动时及每天执行保留策略。缩短保留期后，超期审计及已结束会话的录制将被清理，无法恢复；访问历史仍保留，进行中的会话不受影响。</p>
<button class="btn btn-primary" :disabled="busy">保存设置</button>
</form></section>
<section class="panel"><div class="panel-head"><h2 class="panel-title">登录会话</h2><a class="btn btn-sm" href="/api/v1/audit/export" download>导出最近 500 条审计</a></div><div class="panel-body stack">
<div v-for="s in sessions" :key="s.id" class="notice"><strong>{{ s.current ? '当前会话' : '其他会话' }} · {{ s.ip }}</strong><p class="muted" style="overflow-wrap:anywhere">{{ s.userAgent }}</p><p>最后活动 {{ formatTime(s.lastSeenAt) }} · 到期 {{ formatTime(s.expiresAt) }}</p><button class="btn btn-danger btn-sm" :disabled="busy" @click="revoke(s)">结束会话</button></div>
</div></section>
<p v-if="error" class="notice notice-danger" role="alert">{{ error }}</p><p v-if="notice" class="notice" role="status">{{ notice }}</p>
<StepUpDialog v-if="stepUp" title="确认系统管理操作" @verified="verified" @close="stepUp=false; retry=null" />
</template>
