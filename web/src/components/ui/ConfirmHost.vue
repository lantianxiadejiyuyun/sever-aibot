<script setup lang="ts">
import { AlertTriangle } from 'lucide-vue-next'
import { useConfirm } from '@/composables/useConfirm'
import BaseModal from './BaseModal.vue'

const { state, resolve } = useConfirm()
</script>

<template>
  <BaseModal
    :open="state.open"
    :title="state.title"
    :close-on-backdrop="true"
    @close="resolve(false)"
  >
    <div class="flex" style="align-items: flex-start; gap: 12px">
      <span
        class="notice-icon"
        :style="{
          display: 'grid',
          placeItems: 'center',
          width: '34px',
          height: '34px',
          flex: 'none',
          borderRadius: '9px',
          background: state.danger ? 'var(--danger-weak)' : 'var(--primary-weak)',
          color: state.danger ? 'var(--danger)' : 'var(--primary)',
        }"
        aria-hidden="true"
      >
        <AlertTriangle :size="18" />
      </span>
      <div class="stack-sm" style="gap: 5px">
        <p style="font-size: 13px; line-height: 1.65">{{ state.message }}</p>
        <p v-if="state.detail" class="muted" style="font-size: 12px; line-height: 1.6">
          {{ state.detail }}
        </p>
      </div>
    </div>
    <template #footer>
      <button type="button" class="btn" @click="resolve(false)">{{ state.cancelText }}</button>
      <button
        type="button"
        class="btn"
        :class="state.danger ? 'btn-danger' : 'btn-primary'"
        data-autofocus
        @click="resolve(true)"
      >
        {{ state.confirmText }}
      </button>
    </template>
  </BaseModal>
</template>
