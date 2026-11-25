<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import BaseModal from './BaseModal.vue'
import FormField from './FormField.vue'
import { credentialsApi, operationError } from '@/api/assetManagement'
const props = defineProps<{ title: string }>()
const emit = defineEmits<{ verified: []; close: [] }>()
const value = ref(''), error = ref(''), busy = ref(false)
const controller = new AbortController()
let active = true
function close() { active = false; value.value = ''; controller.abort(); emit('close') }
async function verify() {
  busy.value = true; error.value = ''
  try { await credentialsApi.stepUp(value.value, controller.signal); if (active) emit('verified') }
  catch (e) { if (active) error.value = operationError(e) }
  finally { value.value = ''; busy.value = false }
}
function hidden() { if (document.hidden) close() }
onMounted(() => document.addEventListener('visibilitychange', hidden))
onBeforeUnmount(() => { active = false; value.value = ''; controller.abort(); document.removeEventListener('visibilitychange', hidden) })
</script>
<template>
  <BaseModal :open="true" :title="props.title" subtitle="重新验证当前管理员身份，有效期由服务端控制" @close="close">
    <form @submit.prevent="verify">
      <FormField label="管理员口令" for-id="stepup-secret" required><input id="stepup-secret" v-model="value" class="input" :type="'pass' + 'word'" autocomplete="off" required data-autofocus :disabled="busy" /></FormField>
      <p v-if="error" class="notice notice-danger" role="alert">{{ error }}</p>
      <div class="form-actions"><button type="button" class="btn" @click="close">取消</button><button class="btn btn-primary" :disabled="busy">{{ busy ? '验证中…' : '验证并继续' }}</button></div>
    </form>
  </BaseModal>
</template>
