<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import { AlertTriangle, ChevronLeft, Loader2, Network } from 'lucide-vue-next'
import PageHeader from '@/components/ui/PageHeader.vue'
import ForwardForm from '@/components/forms/ForwardForm.vue'
import { devicesApi, forwardsApi, nodesApi } from '@/api/endpoints'
import type { ForwardInput } from '@/api/endpoints'
import { errorMessage } from '@/api/client'
import { useToast } from '@/composables/useToast'
import type { Device, Node } from '@/api/types'

const route = useRoute()
const router = useRouter()
const toast = useToast()

const devices = ref<Device[]>([])
const nodes = ref<Node[]>([])
const choicesLoading = ref(true)
const choicesError = ref('')
const submitting = ref(false)
const serverError = ref('')

const validKinds = ['port', 'host', 'tunnel', 'p2p', 'proxy']
const requestedKind = typeof route.query.kind === 'string' && validKinds.includes(route.query.kind)
  ? route.query.kind
  : 'port'
const defaultKind = ref(requestedKind)

async function loadChoices(): Promise<void> {
  choicesLoading.value = true
  choicesError.value = ''
  try {
    const [deviceResponse, nodeResponse] = await Promise.all([devicesApi.list(), nodesApi.list()])
    devices.value = deviceResponse.items
    nodes.value = nodeResponse.items
  } catch (err) {
    choicesError.value = errorMessage(err)
  } finally {
    choicesLoading.value = false
  }
}

async function create(payload: ForwardInput): Promise<void> {
  submitting.value = true
  serverError.value = ''
  try {
    const forward = await forwardsApi.create(payload)
    toast.success('转发已保存')
    await router.replace(`/forwards/${forward.id}`)
  } catch (err) {
    serverError.value = errorMessage(err)
  } finally {
    submitting.value = false
  }
}

onMounted(loadChoices)
</script>

<template>
  <PageHeader
    eyebrow="FORWARDS / CREATE"
    title="新建转发"
  >
    <template #actions>
      <RouterLink to="/forwards" class="btn">
        <ChevronLeft :size="16" aria-hidden="true" />
        返回列表
      </RouterLink>
    </template>
  </PageHeader>

  <div v-if="choicesError" class="notice notice-danger" role="alert" style="margin-bottom: 14px">
    <AlertTriangle :size="17" aria-hidden="true" />
    <div class="grow"><strong>读取节点/设备失败。</strong>{{ choicesError }}</div>
    <button type="button" class="btn btn-sm" @click="loadChoices">重试</button>
  </div>

  <div v-if="!choicesLoading && nodes.length === 0" class="notice notice-warning" style="margin-bottom: 14px">
    <Network :size="17" aria-hidden="true" />
    <div class="grow">
      <strong>还没有可用节点。</strong>转发必须绑定节点，请先创建节点并启用对应能力。
    </div>
    <RouterLink to="/nodes/new" class="btn btn-sm">新建节点</RouterLink>
  </div>

  <section class="panel form-panel">
    <div class="panel-head">
      <div>
        <h2 class="panel-title">转发配置</h2>
        <p class="panel-sub">类型、节点与目标</p>
      </div>
      <Loader2 v-if="choicesLoading" :size="16" class="spin muted" aria-hidden="true" />
    </div>
    <div class="panel-body">
      <ForwardForm
        :devices="devices"
        :nodes="nodes"
        :default-kind="defaultKind"
        :submitting="submitting"
        :server-error="serverError"
        submit-label="保存转发"
        @submit="create"
        @cancel="router.push('/forwards')"
      />
    </div>
  </section>
</template>
