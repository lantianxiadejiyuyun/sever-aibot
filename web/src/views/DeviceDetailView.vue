<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
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
import StepUpDialog from '@/components/ui/StepUpDialog.vue'
import BaseModal from '@/components/ui/BaseModal.vue'
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
const stepUpOpen = ref(false)
const retainOpen = ref(false)
const retainedKey = ref('')
const keyBusy = ref(false)
const keyAction = ref<'reveal' | 'retain'>('reveal')
let secretTimer: ReturnType<typeof setTimeout> | undefined
let generation = 0
function clearSecret(): void {
  secretOpen.value = false
  secretValue.value = ''
  if (secretTimer) clearTimeout(secretTimer)
}
function showSecret(value: string): void {
  clearSecret()
  secretValue.value = value
  secretOpen.value = true
  secretTimer = setTimeout(clearSecret, 60000)
}
function startReveal(): void { keyAction.value = 'reveal'; stepUpOpen.value = true }
function startRetain(): void {
  if (!retainedKey.value.trim()) return
  keyAction.value = 'retain'; retainOpen.value = false; stepUpOpen.value = true
}
async function verifiedKey(): Promise<void> {
  stepUpOpen.value = false
  keyBusy.value = true
  const target = id.value, current = generation
  try {
    if (keyAction.value === 'retain') {
      await devicesApi.retainKey(target, retainedKey.value.trim())
      retainedKey.value = ''
      if (current !== generation) return
      toast.success('当前密钥已验证并加密保存，原有连接不受影响')
      await load()
    } else {
      const response = await devicesApi.revealKey(target)
      if (current === generation) showSecret(response.deviceKey)
    }
  } catch (err) {
    if (current === generation) toast.error('密钥操作失败', errorMessage(err))
  } finally {
    retainedKey.value = ''
    if (current === generation) keyBusy.value = false
  }
}
function clearSensitive(): void {
  generation++
  clearSecret()
  retainedKey.value = ''
  retainOpen.value = false
  stepUpOpen.value = false
  keyBusy.value = false
}
function visibilityChanged(): void { if (document.hidden) clearSensitive() }

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
    detail: '旧密钥会立即失效，在线 Agent 将断开。新密钥会加密保存，之后可验证身份再次查看。',
    confirmText: '轮换密钥',
    danger: true,
  })
  if (!ok) return
  editing.value = true
  const current = generation
  try {
    const response = await devicesApi.rotateKey(device.value.id)
    if (current !== generation) return
    showSecret(response.deviceKey)
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

watch(id, () => { clearSensitive(); void load() }, { immediate: true })
document.addEventListener('visibilitychange', visibilityChanged)
onBeforeUnmount(() => { clearSensitive(); document.removeEventListener('visibilitychange', visibilityChanged) })
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
      <button v-if="device?.keyRetrievable" type="button" class="btn btn-primary" :disabled="keyBusy" @click="startReveal">
        <KeyRound :size="16" aria-hidden="true" />查看密钥
      </button>
      <button v-else-if="device" type="button" class="btn" :disabled="keyBusy" @click="retainOpen = true">
        <KeyRound :size="16" aria-hidden="true" />保存现有密钥
      </button>
      <button v-if="device" type="button" class="btn" :disabled="editing || keyBusy" @click="rotate">
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
            <div class="kv-value">{{ device.keyRetrievable ? '密钥已加密保存，可验证身份后查看' : '旧版仅保存哈希，尚未保留可查看的密钥' }}</div>
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
              <template v-if="device.keyRetrievable"><strong>可再次查看。</strong>点击「查看密钥」并完成二次验证；显示 60 秒或切换离开页面后自动隐藏。</template>
              <template v-else><strong>旧版密钥尚未保留。</strong>已知当前密钥时可点击「保存现有密钥」，验证匹配后加密保存，不会断开 Agent。若已丢失则需要轮换，旧密钥将失效。</template>
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
    title="设备密钥"
    subtitle="请勿泄露；60 秒后自动隐藏"
    confirm-text="关闭并隐藏"
    retrievable
    @close="clearSecret"
  />
  <BaseModal :open="retainOpen" title="保存现有设备密钥" subtitle="验证当前密钥后加密保留，不轮换、不影响连接" @close="retainOpen = false; retainedKey = ''">
    <label class="form-field">当前设备密钥
      <input v-model="retainedKey" class="input" type="password" autocomplete="off" placeholder="填写当前 Agent 使用的设备密钥" />
    </label>
    <template #footer><button class="btn btn-primary" :disabled="!retainedKey.trim() || keyBusy" @click="startRetain">验证身份并保存</button></template>
  </BaseModal>
  <StepUpDialog v-if="stepUpOpen" title="验证身份以访问设备密钥" @verified="verifiedKey" @close="stepUpOpen = false; retainedKey = ''" />
</template>
