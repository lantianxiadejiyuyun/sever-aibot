<script setup>
import { ref, onMounted } from 'vue'
import { s, chat, api, run, enc, patchChat, changed, syncTasks } from './store'
import Evidence from './Evidence.vue'
import { Layers3 } from 'lucide-vue-next'
const ctx = ref(null),
  constraints = ref(chat.value?.constraints || ''),
  exclude = ref([...(chat.value?.exclude_sources || [])])
async function load() {
  ctx.value = await api('/chats/' + enc(s.chat) + '/context')
  constraints.value = ctx.value.constraints || ''
}
async function save() {
  await patchChat({ exclude_sources: exclude.value, constraints: constraints.value.trim() })
  await Promise.all([load(), syncTasks()])
  changed('上下文设置已保存。')
}
onMounted(() => s.chat && run('context', load))
</script>
<template>
  <div class="section-title">
    <Layers3 />
    <div>
      <small>CONTEXT</small>
      <h2>会话上下文</h2>
    </div>
  </div>
  <p v-if="!s.chat" class="hint">新建会话后可以查看实际使用的资料。</p>
  <template v-else
    ><div v-if="ctx" class="metrics">
      <div>
        <small>模型容量</small><b>{{ ctx.capacity }}</b>
      </div>
      <div>
        <small>预留回复</small><b>{{ ctx.reserved }}</b>
      </div>
    </div>
    <label>会话固定约束<input v-model="constraints" :disabled="s.busy.context" maxlength="512" placeholder="例如：仅检查订单服务的公网入口" /></label>
    <p class="hint">修改约束会使旧的待确认计划失效；操作权限仍由会话模式决定。</p>
    <h3>排除可选资料</h3>
    <div class="row">
      <label class="check"
        ><input
          type="checkbox"
          value="history"
          v-model="exclude"
        />历史对话</label
      ><label class="check"
        ><input
          type="checkbox"
          value="report"
          v-model="exclude"
        />最近巡检</label
      >
    </div>
    <button @click="run('context', save)" :disabled="s.busy.context">
      保存上下文设置
    </button>
    <h3>本轮资料</h3>
    <details v-for="(a, i) in ctx?.sources" :key="i">
      <summary>
        {{
          {
            host: '服务器',
            service: '服务档案',
            report: '巡检报告',
            rule: '网络规则',
            history: '近期对话',
            history_summary: '已摘要的历史对话',
            task: '未完成任务',
          }[a.name] || a.name
        }}
        · {{ a.time || '无采集时间' }}
      </summary>
      <Evidence :value="a.data" />
    </details>
    <p v-if="ctx?.summary" class="hint">{{ ctx.summary }}</p></template
  >
</template>
