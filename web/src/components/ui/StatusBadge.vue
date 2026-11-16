<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{
  status?: string | null
  label?: string
}>()

const normalized = computed(() => (props.status || '').toLowerCase())

const tone = computed(() => {
  switch (normalized.value) {
    case 'online':
    case 'observed':
    case 'ok':
    case 'success':
    case 'active':
      return 'badge-success'
    case 'applied':
    case 'info':
      return 'badge-info'
    case 'saved':
    case 'pending':
      return 'badge-muted'
    case 'warning':
    case 'degraded':
    case 'applying':
      return 'badge-warning'
    case 'error':
    case 'denied':
    case 'failed':
    case 'offline':
      return normalized.value === 'offline' ? 'badge-muted' : 'badge-danger'
    default:
      return 'badge-muted'
  }
})

const dotTone = computed(() => {
  switch (normalized.value) {
    case 'online':
    case 'observed':
    case 'ok':
    case 'success':
    case 'active':
      return 'dot-success'
    case 'applied':
    case 'info':
      return 'dot-info'
    case 'warning':
    case 'degraded':
    case 'applying':
      return 'dot-warning'
    case 'error':
    case 'denied':
    case 'failed':
      return 'dot-danger'
    default:
      return ''
  }
})

const text = computed(() => {
  if (props.label) return props.label
  switch (normalized.value) {
    case 'online':
      return '在线'
    case 'offline':
      return '离线'
    case 'saved':
      return '已保存'
    case 'applying':
      return '下发中'
    case 'applied':
      return '已下发'
    case 'observed':
      return '已观测'
    case 'error':
      return '异常'
    case 'ok':
      return '成功'
    case 'denied':
      return '拒绝'
    default:
      return props.status || '未知'
  }
})
</script>

<template>
  <span class="badge" :class="tone">
    <span class="dot" :class="dotTone" aria-hidden="true"></span>
    {{ text }}
  </span>
</template>
