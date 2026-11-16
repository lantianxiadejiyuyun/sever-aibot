<script setup lang="ts">
import { RouterView } from 'vue-router'
import { Loader2 } from 'lucide-vue-next'
import { session } from '@/stores/session'
import { useTheme } from '@/composables/useTheme'
import ToastHost from '@/components/ui/ToastHost.vue'
import ConfirmHost from '@/components/ui/ConfirmHost.vue'

useTheme()
</script>

<template>
  <div v-if="!session.ready" class="boot-screen" role="status" aria-live="polite">
    <div class="brand-mark" aria-hidden="true">
      <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round">
        <path d="M5 19 12 5l7 14" />
        <path d="M8.5 14h7" />
      </svg>
    </div>
    <span class="flex muted" style="font-size: 12.5px">
      <Loader2 :size="15" class="spin" aria-hidden="true" />
      正在验证会话…
    </span>
  </div>
  <RouterView v-else />
  <ToastHost />
  <ConfirmHost />
</template>
