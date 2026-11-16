<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import {
  AlertTriangle,
  ArrowLeftRight,
  ChevronRight,
  Loader2,
  Pencil,
  Play,
  Plus,
  RefreshCw,
  Search,
  Trash2,
} from 'lucide-vue-next'
import PageHeader from '@/components/ui/PageHeader.vue'
import StatusBadge from '@/components/ui/StatusBadge.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import { devicesApi, forwardsApi, nodesApi } from '@/api/endpoints'
import { errorMessage } from '@/api/client'
import { confirmAction } from '@/composables/useConfirm'
import { useToast } from '@/composables/useToast'
import type { Device, Forward, Node } from '@/api/types'
import { formatBytes, splitTags } from '@/utils/format'
import { forwardKindOptions, kindLabel } from '@/utils/node'

const toast = useToast()

const forwards = ref<Forward[]>([])
const devices = ref<Device[]>([])
const nodes = ref<Node[]>([])
const loading = ref(true)
const error = ref('')
const query = ref('')
const kind = ref('')
const deviceFilter = ref('')
const nodeFilter = ref('')
const applyingId = ref('')
const deletingId = ref('')

const filtered = computed(() => {
  const q = query.value.trim().toLowerCase()
  if (!q) return forwards.value
  return forwards.value.filter((item) =>
    [
      item.name,
      item.kind,
      item.domain,
      item.targetIp,
      item.clientId,
      item.tags,
      item.state,
      deviceName(item.deviceId),
      nodeName(item.nodeId),
    ]
      .filter(Boolean)
      .some((value) => value.toLowerCase().includes(q)),
  )
})

function deviceName(id: string): string {
  if (!id) return ''
  return devices.value.find((device) => device.id === id)?.name || id
}

function nodeName(id: string): string {
  if (!id) return ''
  return nodes.value.find((node) => node.id === id)?.name || id
}

function entryLabel(item: Forward): string {
  if (item.kind === 'host') return item.domain || '—'
  if (item.remotePort) return `:${item.remotePort}`
  if (item.domain) return item.domain
  return '—'
}

function targetLabel(item: Forward): string {
  if (item.targetIp && item.targetPort) return `${item.targetIp}:${item.targetPort}`
  if (item.targetIp) return item.targetIp
  if (item.domain) return item.domain
  return '—'
}

async function loadChoices(): Promise<void> {
  try {
    const [deviceResponse, nodeResponse] = await Promise.all([devicesApi.list(), nodesApi.list()])
    devices.value = deviceResponse.items
    nodes.value = nodeResponse.items
  } catch {
    /* filters simply stay empty; the forwards list still works */
  }
}

async function loadForwards(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    const response = await forwardsApi.list({
      kind: kind.value || undefined,
      device: deviceFilter.value || undefined,
      node: nodeFilter.value || undefined,
    })
    forwards.value = response.items
  } catch (err) {
    error.value = errorMessage(err)
  } finally {
    loading.value = false
  }
}

function setKind(value: string): void {
  if (kind.value === value) return
  kind.value = value
  void loadForwards()
}

async function apply(item: Forward): Promise<void> {
  applyingId.value = item.id
  try {
    const result = await forwardsApi.apply(item.id)
    if (result.lastError) {
      toast.warning('已推送，等待连接', result.lastError)
    } else {
      toast.success('配置已推送')
    }
    await loadForwards()
  } catch (err) {
    toast.error('下发失败', errorMessage(err))
  } finally {
    applyingId.value = ''
  }
}

async function remove(item: Forward): Promise<void> {
  const ok = await confirmAction({
    title: '删除转发',
    message: `确认删除转发「${item.name}」吗？`,
    detail: '该操作不可撤销。删除后会同时关闭节点入口并移除 Agent 隧道配置。',
    confirmText: '删除转发',
    danger: true,
  })
  if (!ok) return
  deletingId.value = item.id
  try {
    await forwardsApi.remove(item.id)
    toast.success('转发已删除')
    await loadForwards()
  } catch (err) {
    toast.error('删除转发失败', errorMessage(err))
  } finally {
    deletingId.value = ''
  }
}

onMounted(() => {
  void loadChoices()
  void loadForwards()
})
</script>

<template>
  <PageHeader
    eyebrow="FORWARDS / ENTRIES"
    title="转发"
  >
    <template #actions>
      <button type="button" class="btn" :disabled="loading" @click="loadForwards">
        <Loader2 v-if="loading" :size="16" class="spin" aria-hidden="true" />
        <RefreshCw v-else :size="16" aria-hidden="true" />
        刷新
      </button>
      <RouterLink to="/forwards/new" class="btn btn-primary">
        <Plus :size="16" aria-hidden="true" />
        新建转发
      </RouterLink>
    </template>
  </PageHeader>

  <section class="panel">
    <div class="filter-bar">
      <div class="search-box">
        <Search :size="16" aria-hidden="true" />
        <input v-model="query" type="search" placeholder="搜索名称、域名、目标、标签…" aria-label="搜索转发" />
      </div>
      <select v-model="nodeFilter" class="select" aria-label="按节点筛选" @change="loadForwards">
        <option value="">全部节点</option>
        <option v-for="node in nodes" :key="node.id" :value="node.id">{{ node.name }}</option>
      </select>
      <select v-model="deviceFilter" class="select" aria-label="按设备筛选" @change="loadForwards">
        <option value="">全部设备</option>
        <option v-for="device in devices" :key="device.id" :value="device.id">{{ device.name }}</option>
      </select>
      <span class="pill">共 {{ forwards.length }} 条 · 筛选后 {{ filtered.length }} 条</span>
    </div>

    <div class="filter-bar" style="padding-top: 0">
      <div class="chip-row" role="group" aria-label="按转发类型筛选">
        <button type="button" class="chip" :class="{ 'is-active': kind === '' }" @click="setKind('')">全部</button>
        <button
          v-for="option in forwardKindOptions"
          :key="option.value"
          type="button"
          class="chip"
          :class="{ 'is-active': kind === option.value }"
          @click="setKind(option.value)"
        >
          {{ option.label }}
        </button>
      </div>
    </div>

    <div v-if="error" class="notice notice-danger" role="alert" style="margin: 14px">
      <AlertTriangle :size="17" aria-hidden="true" />
      <div class="grow"><strong>读取转发失败。</strong>{{ error }}</div>
      <button type="button" class="btn btn-sm" @click="loadForwards">重试</button>
    </div>

    <div v-if="loading && forwards.length === 0" class="panel-body" aria-hidden="true">
      <div v-for="i in 5" :key="i" class="skeleton skeleton-line" style="height: 38px; margin-bottom: 10px"></div>
    </div>

    <div v-else-if="forwards.length === 0" class="panel-body">
      <EmptyState
        :icon="ArrowLeftRight"
        title="还没有转发条目"
        description="先确认节点已启用对应能力，再创建端口、域名或隧道转发。"
      >
        <template #actions>
          <RouterLink to="/forwards/new" class="btn btn-primary">
            <Plus :size="16" aria-hidden="true" />
            新建转发
          </RouterLink>
        </template>
      </EmptyState>
    </div>

    <div v-else-if="filtered.length === 0" class="panel-body">
      <EmptyState :icon="Search" title="没有匹配的转发" description="换一个关键词，或清空类型/节点/设备筛选。" />
    </div>

    <div v-else class="table-wrap mobile-cards">
      <table class="data-table">
        <thead>
          <tr>
            <th scope="col">转发</th>
            <th scope="col">节点</th>
            <th scope="col">设备</th>
            <th scope="col">公开入口</th>
            <th scope="col">目标</th>
            <th scope="col">状态</th>
            <th scope="col">流量</th>
            <th scope="col">操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="item in filtered" :key="item.id">
            <td class="cell-no-label">
              <div class="row-main">
                <span class="row-avatar"><ArrowLeftRight :size="16" aria-hidden="true" /></span>
                <div class="grow">
                  <RouterLink :to="`/forwards/${item.id}`" class="cell-primary">{{ item.name }}</RouterLink>
                  <div class="cell-sub">
                    <span class="badge badge-secondary">{{ kindLabel(item.kind) }}</span>
                    <span v-if="splitTags(item.tags).length" style="margin-left: 6px">{{ splitTags(item.tags).join(' · ') }}</span>
                  </div>
                </div>
              </div>
            </td>
            <td data-label="节点"><span :title="item.nodeId">{{ nodeName(item.nodeId) || '—' }}</span></td>
            <td data-label="设备"><span :title="item.deviceId">{{ deviceName(item.deviceId) || '—' }}</span></td>
            <td data-label="公开入口"><span class="mono">{{ entryLabel(item) }}</span></td>
            <td data-label="目标"><span class="mono">{{ targetLabel(item) }}</span></td>
            <td data-label="状态">
              <StatusBadge :status="item.state" />
              <div v-if="item.revision" class="cell-sub mono">rev {{ item.revision }}</div>
            </td>
            <td data-label="流量"><span class="mono">{{ formatBytes(item.trafficBytes) }}</span></td>
            <td data-label="操作" class="cell-actions">
              <div class="action-row">
                <RouterLink :to="`/forwards/${item.id}`" class="btn btn-sm">
                  详情<ChevronRight :size="14" aria-hidden="true" />
                </RouterLink>
                <RouterLink :to="`/forwards/${item.id}/edit`" class="btn btn-sm">
                  <Pencil :size="14" aria-hidden="true" />编辑
                </RouterLink>
                <button
                  type="button"
                  class="btn btn-sm"
                  :disabled="applyingId === item.id || item.kind !== 'port'"
                  :title="item.kind !== 'port' ? '当前仅支持 port 类型下发' : ''"
                  @click="apply(item)"
                >
                  <Loader2 v-if="applyingId === item.id" :size="14" class="spin" aria-hidden="true" />
                  <Play v-else :size="14" aria-hidden="true" />
                  下发
                </button>
                <button
                  type="button"
                  class="btn btn-sm btn-danger"
                  :disabled="deletingId === item.id"
                  @click="remove(item)"
                >
                  <Loader2 v-if="deletingId === item.id" :size="14" class="spin" aria-hidden="true" />
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

  <div class="notice notice-info" style="margin-top: 14px">
    <AlertTriangle :size="16" aria-hidden="true" />
    <div>
      保存只会写入控制面。点击下发后配置会推送到节点与 Agent，状态由「下发中」变为「已下发」或「异常」。
    </div>
  </div>
</template>
