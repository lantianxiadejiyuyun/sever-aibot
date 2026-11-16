<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import { ChevronLeft, Loader2, Server } from 'lucide-vue-next'
import PageHeader from '@/components/ui/PageHeader.vue'
import DeviceForm from '@/components/forms/DeviceForm.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import { devicesApi } from '@/api/endpoints'
import { errorMessage } from '@/api/client'
import { useToast } from '@/composables/useToast'
import type { Device } from '@/api/types'

const route = useRoute()
const router = useRouter()
const toast = useToast()

const id = computed(() => String(route.params.id))
const detailPath = computed(() => `/devices/${id.value}`)

const device = ref<Device | null>(null)
const loading = ref(true)
const loadError = ref('')
const submitting = ref(false)
const serverError = ref('')

async function load(): Promise<void> {
  loading.value = true
  loadError.value = ''
  try {
    device.value = await devicesApi.get(id.value)
  } catch (err) {
    loadError.value = errorMessage(err)
  } finally {
    loading.value = false
  }
}

async function save(payload: { name: string; remark: string; tags: string }): Promise<void> {
  submitting.value = true
  serverError.value = ''
  try {
    await devicesApi.update(id.value, payload)
    toast.success('设备已更新')
    await router.replace(detailPath.value)
  } catch (err) {
    serverError.value = errorMessage(err)
  } finally {
    submitting.value = false
  }
}

onMounted(load)
</script>

<template>
  <PageHeader
    eyebrow="DEVICES / EDIT"
    title="编辑设备"
    :description="device ? `正在编辑「${device.name}」` : '修改设备名称、标签与备注。'"
  >
    <template #actions>
      <RouterLink :to="detailPath" class="btn">
        <ChevronLeft :size="16" aria-hidden="true" />
        返回详情
      </RouterLink>
    </template>
  </PageHeader>

  <section v-if="loading" class="panel form-panel">
    <div class="panel-body" aria-hidden="true">
      <div v-for="i in 4" :key="i" class="skeleton skeleton-line" style="height: 40px; margin-bottom: 12px"></div>
    </div>
  </section>

  <section v-else-if="loadError" class="panel form-panel">
    <EmptyState
      :icon="Server"
      title="无法读取设备"
      :description="loadError"
    >
      <template #actions>
        <button type="button" class="btn" @click="load"><Loader2 :size="15" aria-hidden="true" />重试</button>
        <RouterLink to="/devices" class="btn btn-primary">返回设备列表</RouterLink>
      </template>
    </EmptyState>
  </section>

  <section v-else class="panel form-panel">
    <div class="panel-head">
      <div>
        <h2 class="panel-title">设备信息</h2>
        <p class="panel-sub mono">{{ device?.id }}</p>
      </div>
    </div>
    <div class="panel-body">
      <DeviceForm
        :initial="device"
        :submitting="submitting"
        :server-error="serverError"
        submit-label="保存修改"
        @submit="save"
        @cancel="router.push(detailPath)"
      />
    </div>
  </section>
</template>
