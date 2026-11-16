<script setup lang="ts">
import { AlertTriangle, CheckCircle2, Info, X, XCircle } from 'lucide-vue-next'
import { useToast } from '@/composables/useToast'
import type { ToastKind } from '@/composables/useToast'

const { toasts, dismiss } = useToast()

const icons = {
  success: CheckCircle2,
  error: XCircle,
  warning: AlertTriangle,
  info: Info,
} as const

function iconFor(kind: ToastKind) {
  return icons[kind]
}
</script>

<template>
  <div class="toast-host" aria-live="polite" aria-atomic="false">
    <div
      v-for="toast in toasts"
      :key="toast.id"
      class="toast"
      :class="`toast-${toast.kind}`"
      :role="toast.kind === 'error' ? 'alert' : 'status'"
    >
      <component :is="iconFor(toast.kind)" :size="18" class="toast-icon" aria-hidden="true" />
      <div class="toast-main">
        <div class="toast-message">{{ toast.message }}</div>
        <div v-if="toast.detail" class="toast-detail">{{ toast.detail }}</div>
      </div>
      <button type="button" class="toast-close" aria-label="关闭通知" @click="dismiss(toast.id)">
        <X :size="15" aria-hidden="true" />
      </button>
    </div>
  </div>
</template>
