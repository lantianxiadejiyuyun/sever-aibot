<script setup>
import { computed } from 'vue'
const props = defineProps({ value: null })
const names = {
  host_id: '服务器',
  service_id: '服务',
  source: '来源',
  time: '采集时间',
  before: '变更前',
  after: '变更后',
  data: '配置',
  steps: '执行步骤',
  rollback: '回退',
  verification: '验证',
  impact: '影响',
  protocol: '协议',
  image: '镜像',
  action: '动作',
  version: '版本',
  status: '状态',
  name: '名称',
  target: '目标',
  message: '说明',
  error: '错误',
  result: '结果',
  evidence: '证据',
  directory: '目录',
  fingerprint: '指纹',
  created_at: '创建时间',
  updated_at: '更新时间',
  expected: '预期配置',
  verified: '已验证状态',
  state: '现场状态',
}
function format(v, n = 0) {
  if (v === null || v === undefined) return '未记录'
  if (Array.isArray(v))
    return v.length ? v.map((x) => format(x, n + 1)).join('\n') : '无'
  if (typeof v === 'object')
    return Object.entries(v)
      .filter(
        ([k]) =>
          !/(secret|token|private.?key|api.?key|pass(word|phrase)|access.?key|auth.?key)/i.test(
            k,
          ),
      )
      .map(
        ([k, x]) =>
          (n ? '  '.repeat(Math.min(n, 4)) : '') +
          (names[k] || k) +
          '：' +
          (x && typeof x === 'object' ? '\n' : '') +
          format(x, n + 1),
      )
      .join('\n')
  return String(v)
}
const display = computed(() => format(props.value))
</script>
<template>
  <pre class="evidence">{{ display }}</pre>
</template>
