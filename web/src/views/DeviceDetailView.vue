<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import {
  AlertTriangle,
  ChevronLeft,
  KeyRound,
  Loader2,
  Pencil,
  RefreshCw,
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
import { formatTime, splitTags, timeAgo } from '@/utils/format'

const route = useRoute()
const router = useRouter()
const toast = useToast()

const id = computed(() => String(route.params.id))
const editing = ref(false)
const deleting = ref(false)
const device = ref<Device | null>(null)
const loading = ref(true)
const error = ref('')
const secretOpen = ref(false)
const secretValue = ref('')

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    device.value = await devicesApi.get(id.value)
  } catch (err) {
    error.value = errorMessage(err)
  } finally {
    loading.value = false
  }
}

async function rotate(): Promise<void> {
  if (!device.value) return
  const ok = await confirmAction({
    title: '轮换设备密钥',
    message: `确认轮换「${device.value.name}」的设备密钥吗？`,
    detail: '旧密钥会立即失效，在线 Agent 将断开。新密钥只显示一次。',
    confirmText: '轮换密钥',
    danger: true,
  })
  if (!ok) return
  editing.value = true
  try {
    const response = await devicesApi.rotateKey(device.value.id)
    secretValue.value = response.deviceKey
    secretOpen.value = true
    toast.success('设备密钥已轮换')
    await load()
  } catch (err) {
    toast.error('轮换密钥失败', errorMessage(err))
  } finally {
    editing.value = false
  }
}

async function remove(): Promise<void> {
  if (!device.value) return
  const name = device.value.name
  const ok = await confirmAction({
    title: '删除设备',
    message: `确认删除设备「${name}」吗？`,
    detail: '该操作不可撤销。在线设备需要先停止 Agent 才能删除。',
    confirmText: '删除设备',
    danger: true,
  })
  if (!ok) return
  deleting.value = true
  try {
    await devicesApi.remove(device.value.id)
    toast.success('设备已删除')
    await router.replace('/devices')
  } catch (err) {
    toast.error('删除设备失败', errorMessage(err))
  } finally {
    deleting.value = false
  }
}

onMounted(load)
</script>

<template>
  <PageHeader
    eyebrow="DEVICES / DETAIL"
    :title="device?.name || '设备详情'"
    :description="device ? `设备 ID：${device.id}` : '设备接入信息与密钥状态。'"
  >
    <template #actions>
      <RouterLink to="/devices" class="btn">
        <ChevronLeft :size="16" aria-hidden="true" />
        返回列表
      </RouterLink>
      <RouterLink v-if="device" :to="`/devices/${device.id}/edit`" class="btn">
        <Pencil :size="16" aria-hidden="true" />
        编辑
      </RouterLink>
      <button v-if="device" type="button" class="btn" :disabled="editing" @click="rotate">
        <Loader2 v-if="editing" :size="16" class="spin" aria-hidden="true" />
        <RefreshCw v-else :size="16" aria-hidden="true" />
        轮换密钥
      </button>
      <button v-if="device" type="button" class="btn btn-danger" :disabled="deleting" @click="remove">
        <Loader2 v-if="deleting" :size="16" class="spin" aria-hidden="true" />
        <Trash2 v-else :size="16" aria-hidden="true" />
        删除
      </button>
    </template>
  </PageHeader>

  <section v-if="loading" class="panel" aria-hidden="true">
    <div class="panel-body">
      <div v-for="i in 6" :key="i" class="skeleton skeleton-line" style="height: 34px; margin-bottom: 12px"></div>
    </div>
  </section>

  <section v-else-if="error" class="panel">
    <EmptyState :icon="Server" title="无法读取设备" :description="error">
      <template #actions>
        <button type="button" class="btn" @click="load">重试</button>
        <RouterLink to="/devices" class="btn btn-primary">返回设备列表</RouterLink>
      </template>
    </EmptyState>
  </section>

  <div v-else-if="device" class="detail-grid">
    <section class="panel">
      <div class="panel-head">
        <div>
          <h2 class="panel-title">基本信息</h2>
          <p class="panel-sub">Agent 上报与控制面登记的信息</p>
        </div>
        <div class="panel-actions">
          <StatusBadge :status="device.status" />
        </div>
      </div>
      <div class="panel-body">
        <div class="kv-grid">
          <div class="kv">
            <div class="kv-label">设备 ID</div>
            <div class="kv-value mono">{{ device.id }}</div>
          </div>
          <div class="kv">
            <div class="kv-label">密钥存储</div>
            <div class="kv-value">服务端仅保存密钥哈希</div>
          </div>
          <div class="kv">
            <div class="kv-label">版本</div>
            <div class="kv-value">{{ device.version || '—' }}</div>
          </div>
          <div class="kv">
            <div class="kv-label">操作系统 / 架构</div>
            <div class="kv-value">{{ [device.os, device.arch].filter(Boolean).join(' / ') || '—' }}</div>
          </div>
          <div class="kv">
            <div class="kv-label">标签</div>
            <div v-if="splitTags(device.tags).length" class="tag-list" style="margin-top: 5px">
              <span v-for="tag in splitTags(device.tags)" :key="tag" class="tag">{{ tag }}</span>
            </div>
            <div v-else class="kv-value muted">—</div>
          </div>
          <div class="kv">
            <div class="kv-label">最近在线</div>
            <div class="kv-value">{{ timeAgo(device.lastSeenAt) }}</div>
          </div>
          <div class="kv">
            <div class="kv-label">创建时间</div>
            <div class="kv-value">{{ formatTime(device.createdAt) }}</div>
          </div>
          <div class="kv">
            <div class="kv-label">更新时间</div>
            <div class="kv-value">{{ formatTime(device.updatedAt) }}</div>
          </div>
          <div class="kv" style="grid-column: 1 / -1">
            <div class="kv-label">备注</div>
            <div class="kv-value" style="white-space: pre-wrap">{{ device.remark || '—' }}</div>
          </div>
        </div>
      </div>
    </section>

    <div class="stack">
      <section class="panel">
        <div class="panel-head">
          <div>
            <h2 class="panel-title">接入状态</h2>
            <p class="panel-sub">会话与密钥状态</p>
          </div>
        </div>
        <div class="panel-body">
          <div class="meta-list">
            <div class="meta-row">
              <span class="meta-label">当前状态</span>
              <span class="meta-value"><StatusBadge :status="device.status" /></span>
            </div>
            <div class="meta-row">
              <span class="meta-label">最近心跳</span>
              <span class="meta-value">{{ timeAgo(device.lastSeenAt) }}</span>
            </div>
            <div class="meta-row">
              <span class="meta-label">最近心跳时间</span>
              <span class="meta-value mono">{{ formatTime(device.lastSeenAt) }}</span>
            </div>
          </div>
          <div class="notice notice-warning" style="margin-top: 14px">
            <AlertTriangle :size="16" aria-hidden="true" />
            <div>
              <strong>密钥不可再次查看。</strong>控制面只保存哈希，丢失或泄露时请直接轮换；旧密钥会立即失效。
            </div>
          </div>
          <div class="notice notice-info" style="margin-top: 10px">
            <KeyRound :size="16" aria-hidden="true" />
            <div>Agent 通过 <span class="mono">/agent/ws</span> 使用设备密钥认证，Web 会话与设备密钥相互隔离。</div>
          </div>
        </div>
      </section>
    </div>
  </div>

  <SecretDialog
    :open="secretOpen"
    :secret="secretValue"
    :subject="device?.name || ''"
    title="新的设备密钥"
    subtitle="轮换完成，请立即更新 Agent 配置"
    @close="secretOpen = false"
  />
</template>
