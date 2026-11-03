<script setup>
import { computed } from 'vue'
import { s, api, run, label, when, enc, syncTasks, changed } from './store'
import Evidence from './Evidence.vue'
import { ListChecks, RefreshCw } from 'lucide-vue-next'
const props = defineProps({ id: String })
const t = computed(() => s.tasks.find((x) => x.id === props.id))
const locked = computed(
  () =>
    !t.value ||
    t.value.host_id !== s.host ||
    (t.value.service_id && t.value.service_id !== s.service),
)
async function confirm() {
  if (locked.value) return
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
    <p v-if="locked" class="hint">当前目标与任务不一致。</p></template
  >
  <p v-else>该任务不在当前目标下，请重新选择。</p>
</template>
