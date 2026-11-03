<script setup>
import { computed } from 'vue'
import { s, api, run, label, when, enc, syncTasks, changed, openChat, focusTask } from './store'
import Evidence from './Evidence.vue'
import { ListChecks, RefreshCw } from 'lucide-vue-next'
const props = defineProps({ id: String })
const t = computed(() => s.tasks.find((x) => x.id === props.id))
const locked = computed(
  () =>
    !t.value ||
    (t.value.chat_id && t.value.chat_id !== s.chat) ||
    t.value.host_id !== s.host ||
    (t.value.service_id && t.value.service_id !== s.service),
)
async function resume() {
  const task = await api('/tasks/' + enc(props.id))
  const chats = await api('/chats')
  const c = chats.find((x) => x.id === task.chat_id)
  if (!c || c.host_id !== task.host_id || (c.service_id || '') !== (task.service_id || '')) {
    await syncTasks()
    throw Error('原会话不存在或目标已变化，请重新预览计划。')
  }
  s.chats = chats
  await openChat(c)
  focusTask(task)
}
async function confirm() {
  if (locked.value || s.mode !== 'change' || t.value?.status !== 'awaiting_confirmation') return
  await api('/tasks/' + enc(props.id) + '/confirm', 'POST', {})
  await syncTasks()
  changed('已提交确认，可在任务中查看进度。')
}
</script>
<template>
  <div class="section-title">
    <ListChecks />
    <div>
      <small>TASK RECORD</small>
      <h2>操作任务</h2>
    </div>
  </div>
  <template v-if="t"
    ><div class="row split">
      <code>{{ t.id }}</code
      ><span class="pill">{{ label(t.status) }}</span>
    </div>
    <p>
      {{ t.kind }} / {{ t.action }} · {{ when(t.updated_at || t.created_at) }}
    </p>
    <h3>计划与影响</h3>
    <Evidence :value="t.plan" /><template v-if="t.steps?.length"
      ><h3>执行步骤</h3>
      <Evidence :value="t.steps" /></template
    ><template v-if="t.result"
      ><h3>执行与验证结果</h3>
      <Evidence :value="t.result"
    /></template>
    <div class="actions">
      <button @click="run('tasks', syncTasks)"><RefreshCw />刷新状态</button
      ><button
        v-if="t.status === 'awaiting_confirmation'"
        class="primary"
        :disabled="locked || s.mode === 'inspect' || s.busy.confirm"
        @click="run('confirm', confirm)"
      >
        确认执行此计划
      </button>
    </div>
    <p
      v-if="t.status === 'awaiting_confirmation' && s.mode === 'inspect'"
      class="hint"
    >
      请先将当前会话切换为允许确认变更。
    </p>
    <p v-if="locked" class="hint">请先核对任务所属会话和目标。</p>
    <button v-if="t.chat_id && (locked || s.mode === 'inspect') && t.status === 'awaiting_confirmation'" :disabled="s.busy.resume" @click="run('resume', resume)">打开任务所属会话</button></template
  >
  <p v-else>该任务不在当前目标下，请重新选择。</p>
</template>
