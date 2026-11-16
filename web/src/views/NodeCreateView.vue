<script setup lang="ts">
import { ref } from 'vue'
import { RouterLink, useRouter } from 'vue-router'
import { ChevronLeft, KeyRound } from 'lucide-vue-next'
import PageHeader from '@/components/ui/PageHeader.vue'
import NodeForm from '@/components/forms/NodeForm.vue'
import SecretDialog from '@/components/ui/SecretDialog.vue'
import { nodesApi } from '@/api/endpoints'
import type { NodeInput } from '@/api/endpoints'
import { errorMessage } from '@/api/client'
import { useToast } from '@/composables/useToast'
import type { Node } from '@/api/types'

const router = useRouter()
const toast = useToast()

const submitting = ref(false)
const serverError = ref('')
const secretOpen = ref(false)
const nodeKey = ref('')
const createdNode = ref<Node | null>(null)

async function create(payload: NodeInput): Promise<void> {
  submitting.value = true
  serverError.value = ''
  try {
    const response = await nodesApi.create(payload)
    createdNode.value = response.node
    nodeKey.value = response.nodeKey
    secretOpen.value = true
    toast.success('节点已创建，请保存一次性密钥')
  } catch (err) {
    serverError.value = errorMessage(err)
  } finally {
    submitting.value = false
  }
}

function closeSecret(): void {
  secretOpen.value = false
  const id = createdNode.value?.id
  if (id) {
    void router.replace(`/nodes/${id}`)
  } else {
    void router.replace('/nodes')
  }
}
</script>

<template>
  <PageHeader eyebrow="NODES / CREATE" title="新建节点">
    <template #actions>
      <RouterLink to="/nodes" class="btn">
        <ChevronLeft :size="16" aria-hidden="true" />
        返回列表
      </RouterLink>
    </template>
  </PageHeader>

  <section class="panel form-panel">
    <div class="panel-head">
      <div>
        <h2 class="panel-title">节点配置</h2>
        <p class="panel-sub">保存后生成一次性节点接入密钥</p>
      </div>
      <span class="badge badge-info"><KeyRound :size="13" aria-hidden="true" />密钥仅显示一次</span>
    </div>
    <div class="panel-body">
      <NodeForm
        :submitting="submitting"
        :server-error="serverError"
        submit-label="创建节点"
        @submit="create"
        @cancel="router.push('/nodes')"
      />
    </div>
  </section>

  <SecretDialog
    :open="secretOpen"
    :secret="nodeKey"
    :subject="createdNode?.name || ''"
    title="节点创建成功"
    subtitle="请立即把密钥写入节点程序配置"
    @close="closeSecret"
  />
</template>
