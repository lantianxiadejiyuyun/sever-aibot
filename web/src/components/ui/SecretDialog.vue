<script setup lang="ts">
import { computed, ref } from 'vue'
import { AlertTriangle, Check, Copy, KeyRound } from 'lucide-vue-next'
import BaseModal from './BaseModal.vue'
import { copyText } from '@/utils/clipboard'
import { useToast } from '@/composables/useToast'

const props = withDefaults(
  defineProps<{
    open: boolean
    title?: string
    subtitle?: string
    secret: string
    subject?: string
    confirmText?: string
  }>(),
  {
    title: '一次性密钥',
    subtitle: '',
    subject: '',
    confirmText: '我已保存，继续',
  },
)

const emit = defineEmits<{ close: [] }>()
const toast = useToast()
const copied = ref(false)
const copyError = ref('')

const warning = computed(() => {
  const target = props.subject ? `设备「${props.subject}」的` : ''
  return `${target}密钥只显示这一次，关闭后无法再次查看。请立即保存到安全的位置。`
})

async function copy(): Promise<void> {
  copyError.value = ''
  const ok = await copyText(props.secret)
  if (ok) {
    copied.value = true
    toast.success('密钥已复制')
    window.setTimeout(() => {
      copied.value = false
    }, 2200)
  } else {
    copyError.value = '浏览器不允许写入剪贴板，请手动选择密钥后复制。'
  }
}

function close(): void {
  copied.value = false
  copyError.value = ''
  emit('close')
}
</script>

<template>
  <BaseModal :open="open" :title="title" :subtitle="subtitle" :close-on-backdrop="false" @close="close">
    <div class="stack">
      <div class="notice notice-warning" role="alert">
        <AlertTriangle :size="17" aria-hidden="true" />
        <div><strong>仅显示一次。</strong>{{ warning }}</div>
      </div>

      <div class="secret-key">
        <KeyRound :size="17" class="muted" aria-hidden="true" />
        <code>{{ secret }}</code>
        <button type="button" class="btn btn-sm copy-btn" data-autofocus @click="copy">
          <Check v-if="copied" :size="15" aria-hidden="true" />
          <Copy v-else :size="15" aria-hidden="true" />
          {{ copied ? '已复制' : '复制' }}
        </button>
      </div>
      <p v-if="copyError" class="form-error" role="alert">{{ copyError }}</p>
      <p class="form-hint">密钥同时用于 Agent 首次连接认证；轮换旧密钥会立即失效。</p>
    </div>
    <template #footer>
      <button type="button" class="btn btn-primary" @click="close">{{ confirmText }}</button>
    </template>
  </BaseModal>
</template>
