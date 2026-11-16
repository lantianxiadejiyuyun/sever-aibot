<script setup lang="ts">
import { ref } from 'vue'
import { RouterLink, useRouter } from 'vue-router'
import { ChevronLeft, KeyRound } from 'lucide-vue-next'
import PageHeader from '@/components/ui/PageHeader.vue'
import DeviceForm from '@/components/forms/DeviceForm.vue'
import SecretDialog from '@/components/ui/SecretDialog.vue'
import { devicesApi } from '@/api/endpoints'
import { errorMessage } from '@/api/client'
import { useToast } from '@/composables/useToast'
import type { Device } from '@/api/types'

const router = useRouter()
const toast = useToast()

const submitting = ref(false)
const serverError = ref('')
const secretOpen = ref(false)
const deviceKey = ref('')
const createdDevice = ref<Device | null>(null)

async function create(payload: { name: string; remark: string; tags: string }): Promise<void> {
  submitting.value = true
  serverError.value = ''
  try {
    const response = await devicesApi.create(payload)
    createdDevice.value = response.device
    deviceKey.value = response.deviceKey
    secretOpen.value = true
    toast.success('设备已创建，请保存一次性密钥')
  } catch (err) {
    serverError.value = errorMessage(err)
  } finally {
    submitting.value = false
  }
}

function closeSecret(): void {
  secretOpen.value = false
  const id = createdDevice.value?.id
  if (id) {
    void router.replace(`/devices/${id}`)
  } else {
    void router.replace('/devices')
  }
}
</script>

<template>
  <PageHeader
    eyebrow="DEVICES / CREATE"
    title="新建设备"
  >
    <template #actions>
      <RouterLink to="/devices" class="btn">
        <ChevronLeft :size="16" aria-hidden="true" />
        返回列表
      </RouterLink>
    </template>
  </PageHeader>

  <section class="panel form-panel">
    <div class="panel-head">
      <div>
        <h2 class="panel-title">设备信息</h2>
        <p class="panel-sub">保存后生成一次性 Agent 接入密钥</p>
      </div>
      <span class="badge badge-info"><KeyRound :size="13" aria-hidden="true" />密钥仅显示一次</span>
    </div>
    <div class="panel-body">
      <DeviceForm
        :submitting="submitting"
        :server-error="serverError"
        submit-label="创建设备"
        @submit="create"
        @cancel="router.push('/devices')"
      />
    </div>
  </section>

  <SecretDialog
    :open="secretOpen"
    :secret="deviceKey"
    :subject="createdDevice?.name || ''"
    title="设备创建成功"
    subtitle="请立即把密钥写入 Agent 配置"
    @close="closeSecret"
  />
</template>
