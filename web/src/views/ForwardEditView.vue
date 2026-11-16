<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import { ChevronLeft, Loader2, Network } from 'lucide-vue-next'
import PageHeader from '@/components/ui/PageHeader.vue'
import ForwardForm from '@/components/forms/ForwardForm.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import { devicesApi, forwardsApi, nodesApi } from '@/api/endpoints'
import type { ForwardInput } from '@/api/endpoints'
import { errorMessage } from '@/api/client'
import { useToast } from '@/composables/useToast'
import type { Device, Forward, Node } from '@/api/types'

const route = useRoute()
const router = useRouter()
const toast = useToast()

const id = computed(() => String(route.params.id))
const detailPath = computed(() => `/forwards/${id.value}`)

const forward = ref<Forward | null>(null)
const devices = ref<Device[]>([])
const nodes = ref<Node[]>([])
const loading = ref(true)
const loadError = ref('')
const submitting = ref(false)
const serverError = ref('')

async function load(): Promise<void> {
  loading.value = true
  loadError.value = ''
  try {
    const [forwardResponse, deviceResponse, nodeResponse] = await Promise.all([
      forwardsApi.get(id.value),
      devicesApi.list().catch(() => ({ items: [], total: 0 })),
      nodesApi.list().catch(() => ({ items: [], total: 0 })),
    ])
    forward.value = forwardResponse
    devices.value = deviceResponse.items
    nodes.value = nodeResponse.items
  } catch (err) {
    loadError.value = errorMessage(err)
  } finally {
    loading.value = false
  }
}

async function save(payload: ForwardInput): Promise<void> {
  submitting.value = true
  serverError.value = ''
  try {
    await forwardsApi.update(id.value, payload)
    toast.success('转发已更新')
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
    eyebrow="FORWARDS / EDIT"
    title="编辑转发"
    :description="forward ? `正在编辑「${forward.name}」` : '调整类型、节点与目标配置。'"
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
      <div v-for="i in 5" :key="i" class="skeleton skeleton-line" style="height: 40px; margin-bottom: 12px"></div>
    </div>
  </section>

  <section v-else-if="loadError" class="panel form-panel">
    <EmptyState :icon="Network" title="无法读取转发" :description="loadError">
      <template #actions>
        <button type="button" class="btn" @click="load"><Loader2 :size="15" aria-hidden="true" />重试</button>
        <RouterLink to="/forwards" class="btn btn-primary">返回转发列表</RouterLink>
      </template>
    </EmptyState>
  </section>

  <section v-else class="panel form-panel">
    <div class="panel-head">
      <div>
        <h2 class="panel-title">转发配置</h2>
        <p class="panel-sub mono">{{ forward?.id }}</p>
      </div>
      <span class="badge badge-muted">保存后会重置为 saved</span>
    </div>
    <div class="panel-body">
      <ForwardForm
        :initial="forward"
        :devices="devices"
        :nodes="nodes"
        :submitting="submitting"
        :server-error="serverError"
        submit-label="保存修改"
        @submit="save"
        @cancel="router.push(detailPath)"
      />
    </div>
  </section>
</template>
