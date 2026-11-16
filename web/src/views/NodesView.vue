<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import {
  AlertTriangle,
  ChevronRight,
  Loader2,
  Network,
  Pencil,
  Plus,
  RefreshCw,
  Search,
  Trash2,
} from 'lucide-vue-next'
import PageHeader from '@/components/ui/PageHeader.vue'
import StatusBadge from '@/components/ui/StatusBadge.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import { nodesApi } from '@/api/endpoints'
import { errorMessage } from '@/api/client'
import { confirmAction } from '@/composables/useConfirm'
import { useToast } from '@/composables/useToast'
import type { Node } from '@/api/types'
import { capabilityItems } from '@/utils/node'
import { timeAgo } from '@/utils/format'

const toast = useToast()
const nodes = ref<Node[]>([])
const loading = ref(true)
const error = ref('')
const query = ref('')
const deletingId = ref('')

const filtered = computed(() => {
  const q = query.value.trim().toLowerCase()
  if (!q) return nodes.value
  return nodes.value.filter((node) =>
    [node.name, node.remark, node.tags, node.addr, node.protocol, node.domain, node.forwardPorts]
      .filter(Boolean)
      .some((value) => value.toLowerCase().includes(q)),
  )
})

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    const response = await nodesApi.list()
    nodes.value = response.items
  } catch (err) {
    error.value = errorMessage(err)
  } finally {
    loading.value = false
  }
}

async function remove(node: Node): Promise<void> {
  const ok = await confirmAction({
    title: '删除节点',
    message: `确认删除节点「${node.name}」吗？`,
    detail: '删除后，引用该节点的转发配置将无法继续下发。该操作不可撤销。',
    confirmText: '删除节点',
    danger: true,
  })
  if (!ok) return
  deletingId.value = node.id
  try {
    await nodesApi.remove(node.id)
    toast.success('节点已删除')
    await load()
  } catch (err) {
    toast.error('删除节点失败', errorMessage(err))
  } finally {
    deletingId.value = ''
  }
}

onMounted(load)
</script>

<template>
  <PageHeader
    eyebrow="NODES / RELAYS"
    title="节点"
  >
    <template #actions>
      <button type="button" class="btn" :disabled="loading" @click="load">
        <Loader2 v-if="loading" :size="16" class="spin" aria-hidden="true" />
        <RefreshCw v-else :size="16" aria-hidden="true" />
        刷新
      </button>
      <RouterLink to="/nodes/new" class="btn btn-primary">
        <Plus :size="16" aria-hidden="true" />
        新建节点
      </RouterLink>
    </template>
  </PageHeader>

  <section class="panel">
    <div class="filter-bar">
      <div class="search-box">
        <Search :size="16" aria-hidden="true" />
        <input v-model="query" type="search" placeholder="搜索名称、地址、端口段…" aria-label="搜索节点" />
      </div>
      <span class="pill">共 {{ nodes.length }} 个节点 · 筛选后 {{ filtered.length }} 个</span>
    </div>

    <div v-if="error" class="notice notice-danger" role="alert" style="margin: 14px">
      <AlertTriangle :size="17" aria-hidden="true" />
      <div class="grow"><strong>读取节点失败。</strong>{{ error }}</div>
      <button type="button" class="btn btn-sm" @click="load">重试</button>
    </div>

    <div v-if="loading && nodes.length === 0" class="panel-body" aria-hidden="true">
      <div v-for="i in 5" :key="i" class="skeleton skeleton-line" style="height: 38px; margin-bottom: 10px"></div>
    </div>

    <div v-else-if="nodes.length === 0" class="panel-body">
      <EmptyState
        :icon="Network"
        title="还没有节点"
        description="先登记公网中继节点，转发条目才有可用的入口能力。"
      >
        <template #actions>
          <RouterLink to="/nodes/new" class="btn btn-primary">
            <Plus :size="16" aria-hidden="true" />
            新建节点
          </RouterLink>
        </template>
      </EmptyState>
    </div>

    <div v-else-if="filtered.length === 0" class="panel-body">
      <EmptyState :icon="Search" title="没有匹配的节点" description="换一个关键词，或清空筛选条件。">
        <template #actions>
          <button type="button" class="btn" @click="query = ''">清空筛选</button>
        </template>
      </EmptyState>
    </div>

    <div v-else class="table-wrap mobile-cards">
      <table class="data-table">
        <thead>
          <tr>
            <th scope="col">节点</th>
            <th scope="col">地址 / 协议</th>
            <th scope="col">能力</th>
            <th scope="col">端口段</th>
            <th scope="col">状态</th>
            <th scope="col">最近在线</th>
            <th scope="col">操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="node in filtered" :key="node.id">
            <td class="cell-no-label">
              <div class="row-main">
                <span class="row-avatar"><Network :size="16" aria-hidden="true" /></span>
                <div class="grow">
                  <RouterLink :to="`/nodes/${node.id}`" class="cell-primary">{{ node.name }}</RouterLink>
                  <div class="cell-sub mono">{{ node.id }}</div>
                  <div v-if="node.remark" class="cell-sub">{{ node.remark }}</div>
                </div>
              </div>
            </td>
            <td data-label="地址 / 协议">
              <div class="mono">{{ node.addr || '—' }}</div>
              <div class="cell-sub">{{ node.protocol?.toUpperCase() || '—' }}<span v-if="node.httpPort"> · HTTP {{ node.httpPort }}</span></div>
            </td>
            <td data-label="能力">
              <div class="cap-badges">
                <span
                  v-for="cap in capabilityItems(node)"
                  :key="cap.label"
                  class="cap-badge"
                  :class="{ 'is-on': cap.on }"
                >
                  {{ cap.label }}
                </span>
              </div>
            </td>
            <td data-label="端口段">
              <span class="mono" :title="node.forwardPorts || '未限制'">{{ node.forwardPorts || '未限制' }}</span>
            </td>
            <td data-label="状态"><StatusBadge :status="node.status" /></td>
            <td data-label="最近在线"><span :title="node.lastSeenAt || '从未连接'">{{ timeAgo(node.lastSeenAt) }}</span></td>
            <td data-label="操作" class="cell-actions">
              <div class="action-row">
                <RouterLink :to="`/nodes/${node.id}`" class="btn btn-sm">
                  详情<ChevronRight :size="14" aria-hidden="true" />
                </RouterLink>
                <RouterLink :to="`/nodes/${node.id}/edit`" class="btn btn-sm">
                  <Pencil :size="14" aria-hidden="true" />编辑
                </RouterLink>
                <button
                  type="button"
                  class="btn btn-sm btn-danger"
                  :disabled="deletingId === node.id"
                  @click="remove(node)"
                >
                  <Loader2 v-if="deletingId === node.id" :size="14" class="spin" aria-hidden="true" />
                  <Trash2 v-else :size="14" aria-hidden="true" />
                  删除
                </button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
</template>
