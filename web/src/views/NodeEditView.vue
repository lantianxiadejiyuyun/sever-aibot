<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import { ChevronLeft, Loader2, Network } from 'lucide-vue-next'
import PageHeader from '@/components/ui/PageHeader.vue'
import NodeForm from '@/components/forms/NodeForm.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import { nodesApi } from '@/api/endpoints'
import type { NodeInput } from '@/api/endpoints'
import { errorMessage } from '@/api/client'
import { useToast } from '@/composables/useToast'
import type { Node } from '@/api/types'

const route = useRoute()
const router = useRouter()
const toast = useToast()

const id = computed(() => String(route.params.id))
const detailPath = computed(() => `/nodes/${id.value}`)

const node = ref<Node | null>(null)
const loading = ref(true)
const loadError = ref('')
const submitting = ref(false)
const serverError = ref('')

async function load(): Promise<void> {
  loading.value = true
  loadError.value = ''
  try {
    node.value = await nodesApi.get(id.value)
  } catch (err) {
    loadError.value = errorMessage(err)
  } finally {
    loading.value = false
  }
}

async function save(payload: NodeInput): Promise<void> {
  submitting.value = true
  serverError.value = ''
  try {
    await nodesApi.update(id.value, payload)
    toast.success('节点已更新')
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
    eyebrow="NODES / EDIT"
    title="编辑节点"
    :description="node ? `正在编辑「${node.name}」` : '调整节点地址、端口池与能力开关。'"
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
      <div v-for="i in 6" :key="i" class="skeleton skeleton-line" style="height: 40px; margin-bottom: 12px"></div>
    </div>
  </section>

  <section v-else-if="loadError" class="panel form-panel">
    <EmptyState :icon="Network" title="无法读取节点" :description="loadError">
      <template #actions>
        <button type="button" class="btn" @click="load"><Loader2 :size="15" aria-hidden="true" />重试</button>
        <RouterLink to="/nodes" class="btn btn-primary">返回节点列表</RouterLink>
      </template>
    </EmptyState>
  </section>

  <section v-else class="panel form-panel">
    <div class="panel-head">
      <div>
        <h2 class="panel-title">节点配置</h2>
        <p class="panel-sub mono">{{ node?.id }}</p>
      </div>
    </div>
    <div class="panel-body">
      <NodeForm
        :initial="node"
        :submitting="submitting"
        :server-error="serverError"
        submit-label="保存修改"
        @submit="save"
        @cancel="router.push(detailPath)"
      />
    </div>
  </section>
</template>
