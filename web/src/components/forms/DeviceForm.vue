<script setup lang="ts">
import { reactive, ref, watch } from 'vue'
import { Loader2, Save } from 'lucide-vue-next'
import FormField from '@/components/ui/FormField.vue'
import type { Device } from '@/api/types'

const props = withDefaults(
  defineProps<{
    initial?: Device | null
    submitting?: boolean
    serverError?: string
    submitLabel?: string
  }>(),
  { initial: null, submitting: false, serverError: '', submitLabel: '保存' },
)

const emit = defineEmits<{
  submit: [payload: { name: string; remark: string; tags: string }]
  cancel: []
}>()

const form = reactive({ name: '', remark: '', tags: '' })
const localError = ref('')

watch(
  () => props.initial,
  (device) => {
    form.name = device?.name ?? ''
    form.remark = device?.remark ?? ''
    form.tags = device?.tags ?? ''
  },
  { immediate: true },
)

function submit(): void {
  localError.value = ''
  if (!form.name.trim()) {
    localError.value = '设备名称不能为空。'
    return
  }
  emit('submit', {
    name: form.name.trim(),
    remark: form.remark.trim(),
    tags: form.tags.trim(),
  })
}
</script>

<template>
  <form novalidate @submit.prevent="submit">
    <div class="form-grid">
      <FormField
        label="设备名称"
        for-id="device-name"
        required
        :error="localError"
        hint="用于在设备列表和转发条目中识别这台机器。"
      >
        <input
          id="device-name"
          v-model="form.name"
          class="input"
          type="text"
          maxlength="80"
          autocomplete="off"
          placeholder="例如：生产 Web 服务器"
        />
      </FormField>

      <FormField label="标签" for-id="device-tags" hint="逗号分隔，例如 env:prod, region:hk">
        <input
          id="device-tags"
          v-model="form.tags"
          class="input"
          type="text"
          maxlength="200"
          autocomplete="off"
          placeholder="env:prod, region:hk"
        />
      </FormField>

      <FormField label="备注" for-id="device-remark" span2 hint="可选，记录用途、负责人或接入说明。">
        <textarea
          id="device-remark"
          v-model="form.remark"
          class="textarea plain"
          rows="3"
          maxlength="500"
          placeholder="可选备注"
        ></textarea>
      </FormField>
    </div>

    <div v-if="serverError" class="notice notice-danger" role="alert" style="margin-top: 16px">
      {{ serverError }}
    </div>

    <div class="form-actions">
      <button type="button" class="btn" @click="emit('cancel')">取消</button>
      <button type="submit" class="btn btn-primary" :disabled="submitting">
        <Loader2 v-if="submitting" :size="16" class="spin" aria-hidden="true" />
        <Save v-else :size="16" aria-hidden="true" />
        {{ submitting ? '正在保存…' : submitLabel }}
      </button>
    </div>
  </form>
</template>
