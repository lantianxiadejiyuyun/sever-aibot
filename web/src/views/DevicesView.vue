<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import {
  AlertTriangle,
  ChevronRight,
  Loader2,
  Pencil,
  Plus,
  RefreshCw,
  Search,
  Server,
  Trash2,
} from 'lucide-vue-next'
import PageHeader from '@/components/ui/PageHeader.vue'
import StatusBadge from '@/components/ui/StatusBadge.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import SecretDialog from '@/components/ui/SecretDialog.vue'
import { devicesApi } from '@/api/endpoints'
import { errorMessage } from '@/api/client'
import { confirmAction } from '@/composables/useConfirm'
import { useToast } from '@/composables/useToast'
import type { Device } from '@/api/types'
import { splitTags, timeAgo } from '@/utils/format'

const toast = useToast()
const devices = ref<Device[]>([])
const loading = ref(true)
const error = ref('')
const query = ref('')
const rotatingId = ref('')
const deletingId = ref('')

const secretOpen = ref(false)
const secretValue = ref('')
const secretSubject = ref('')

const filtered = computed(() => {
  const q = query.value.trim().toLowerCase()
  if (!q) return devices.value
  return devices.value.filter((device) =>
    [device.name, device.remark, device.tags, device.status, device.version, device.os, device.arch]
      .filter(Boolean)
      .some((value) => value.toLowerCase().includes(q)),
  )
})

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    const response = await devicesApi.list()
    devices.value = response.items
  } catch (err) {
    error.value = errorMessage(err)
  } finally {
    loading.value = false
  }
}

async function rotate(device: Device): Promise<void> {
  const ok = await confirmAction({
    title: '轮换设备密钥',
    message: `确认轮换「${device.name}」的设备密钥吗？`,
    detail: '旧密钥会立即失效，正在连接的 Agent 将断开，需要用新密钥重新连接。新密钥加密保存，之后可在设备详情验证身份后查看。',
    confirmText: '轮换密钥',
    danger: true,
  })
  if (!ok) return
  rotatingId.value = device.id
  try {
    const response = await devicesApi.rotateKey(device.id)
    secretValue.value = response.deviceKey
    secretSubject.value = device.name
    secretOpen.value = true
    toast.success('设备密钥已轮换')
    await load()
  } catch (err) {
    toast.error('轮换密钥失败', errorMessage(err))
  } finally {
    rotatingId.value = ''
  }
}

async function remove(device: Device): Promise<void> {
  const ok = await confirmAction({
    title: '删除设备',
    message: `确认删除设备「${device.name}」吗？`,
    detail: '该操作不可撤销。在线设备需要先停止 Agent 才能删除。',
    confirmText: '删除设备',
    danger: true,
  })
  if (!ok) return
  deletingId.value = device.id
  try {
    await devicesApi.remove(device.id)
    toast.success('设备已删除')
    await load()
  } catch (err) {
    toast.error('删除设备失败', errorMessage(err))
  } finally {
    deletingId.value = ''
  }
}

function closeSecret(): void {
  secretOpen.value = false
  secretValue.value = ''
  secretSubject.value = ''
}

onMounted(load)
</script>

<template>
  <PageHeader
    eyebrow="DEVICES / INVENTORY"
    title="设备"
  >
    <template #actions>
      <button type="button" class="btn" :disabled="loading" @click="load">
        <Loader2 v-if="loading" :size="16" class="spin" aria-hidden="true" />
        <RefreshCw v-else :size="16" aria-hidden="true" />
        刷新
      </button>
      <RouterLink to="/devices/new" class="btn btn-primary">
        <Plus :size="16" aria-hidden="true" />
        新建设备
      </RouterLink>
    </template>
  </PageHeader>

  <section class="panel">
    <div class="filter-bar">
      <div class="search-box">
        <Search :size="16" aria-hidden="true" />
        <input v-model="query" type="search" placeholder="搜索名称、标签、系统…" aria-label="搜索设备" />
      </div>
      <span class="pill">共 {{ devices.length }} 台 · 筛选后 {{ filtered.length }} 台</span>
    </div>

    <div v-if="error" class="notice notice-danger" role="alert" style="margin: 14px">
      <AlertTriangle :size="17" aria-hidden="true" />
      <div class="grow"><strong>读取设备失败。</strong>{{ error }}</div>
      <button type="button" class="btn btn-sm" @click="load">重试</button>
    </div>

    <div v-if="loading && devices.length === 0" class="panel-body" aria-hidden="true">
      <div v-for="i in 5" :key="i" class="skeleton skeleton-line" style="height: 38px; margin-bottom: 10px"></div>
    </div>

    <div v-else-if="devices.length === 0" class="panel-body">
      <EmptyState
        :icon="Server"
        title="还没有设备"
        description="创建一台设备，将设备 ID 和密钥填写到客户端，再点击保存并连接。"
      >
        <template #actions>
          <RouterLink to="/devices/new" class="btn btn-primary">
            <Plus :size="16" aria-hidden="true" />
            新建设备
          </RouterLink>
        </template>
      </EmptyState>
    </div>

    <div v-else-if="filtered.length === 0" class="panel-body">
      <EmptyState :icon="Search" title="没有匹配的设备" description="换一个关键词，或清空筛选条件。">
        <template #actions>
          <button type="button" class="btn" @click="query = ''">清空筛选</button>
        </template>
      </EmptyState>
    </div>

    <div v-else class="table-wrap mobile-cards">
      <table class="data-table">
        <thead>
          <tr>
            <th scope="col">设备</th>
            <th scope="col">状态</th>
            <th scope="col">标签</th>
            <th scope="col">版本 / 系统</th>
            <th scope="col">最近在线</th>
            <th scope="col">操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="device in filtered" :key="device.id">
            <td class="cell-no-label">
              <div class="row-main">
                <span class="row-avatar"><Server :size="16" aria-hidden="true" /></span>
                <div class="grow">
                  <RouterLink :to="`/devices/${device.id}`" class="cell-primary">{{ device.name }}</RouterLink>
                  <div class="cell-sub mono">{{ device.id }}</div>
                  <div v-if="device.remark" class="cell-sub">{{ device.remark }}</div>
                </div>
              </div>
            </td>
            <td data-label="状态"><StatusBadge :status="device.status" /></td>
            <td data-label="标签">
              <div v-if="splitTags(device.tags).length" class="tag-list">
                <span v-for="tag in splitTags(device.tags)" :key="tag" class="tag">{{ tag }}</span>
              </div>
              <span v-else class="muted">—</span>
            </td>
            <td data-label="版本 / 系统">
              <div>{{ device.version || '—' }}</div>
              <div class="cell-sub">{{ [device.os, device.arch].filter(Boolean).join(' / ') || '—' }}</div>
            </td>
            <td data-label="最近在线">
              <span :title="device.lastSeenAt || '从未连接'">{{ timeAgo(device.lastSeenAt) }}</span>
            </td>
            <td data-label="操作" class="cell-actions">
              <div class="action-row">
                <RouterLink :to="`/devices/${device.id}`" class="btn btn-sm">
                  详情<ChevronRight :size="14" aria-hidden="true" />
                </RouterLink>
                <RouterLink :to="`/devices/${device.id}/edit`" class="btn btn-sm" aria-label="编辑设备">
                  <Pencil :size="14" aria-hidden="true" />编辑
                </RouterLink>
                <button
                  type="button"
                  class="btn btn-sm"
                  :disabled="rotatingId === device.id"
                  @click="rotate(device)"
                >
                  <Loader2 v-if="rotatingId === device.id" :size="14" class="spin" aria-hidden="true" />
                  <RefreshCw v-else :size="14" aria-hidden="true" />
                  轮换
                </button>
                <button
                  type="button"
                  class="btn btn-sm btn-danger"
                  :disabled="deletingId === device.id"
                  @click="remove(device)"
                >
                  <Loader2 v-if="deletingId === device.id" :size="14" class="spin" aria-hidden="true" />
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

  <SecretDialog
    :open="secretOpen"
    retrievable
    :secret="secretValue"
    :subject="secretSubject"
    title="新的设备密钥"
    subtitle="轮换完成，请立即更新 Agent 配置"
    @close="closeSecret"
  />
</template>
