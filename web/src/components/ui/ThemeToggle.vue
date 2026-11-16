<script setup lang="ts">
import { computed } from 'vue'
import { Monitor, Moon, Sun } from 'lucide-vue-next'
import { useTheme } from '@/composables/useTheme'
import type { ThemeMode } from '@/composables/useTheme'

withDefaults(defineProps<{ compact?: boolean }>(), { compact: true })

const { mode, resolved, setMode } = useTheme()

const label = computed(() => {
  if (mode.value === 'auto') return '跟随系统'
  return mode.value === 'dark' ? '深色' : '浅色'
})

const icon = computed(() => {
  if (mode.value === 'auto') return Monitor
  return resolved.value === 'dark' ? Moon : Sun
})

function cycle(): void {
  const order: ThemeMode[] = ['auto', 'light', 'dark']
  const index = order.indexOf(mode.value)
  setMode(order[(index + 1) % order.length])
}
</script>

<template>
  <button
    type="button"
    class="icon-btn"
    :aria-label="`主题：${label}，点击切换`"
    :title="`主题：${label}`"
    @click="cycle"
  >
    <component :is="icon" :size="18" aria-hidden="true" />
  </button>
</template>
