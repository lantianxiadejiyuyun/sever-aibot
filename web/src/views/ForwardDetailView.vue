<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import {
  AlertTriangle,
  ArrowLeftRight,
  ChevronLeft,
  Loader2,
  Pencil,
  Play,
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
import { formatBytes, formatTime, splitTags } from '@/utils/format'
import { kindLabel } from '@/utils/node'

const route = useRoute()
const router = useRouter()
const toast = useToast()

const id = computed(() => String(route.params.id))
const forward = ref<Forward | null>(null)
const nodes = ref<Node[]>([])
const devices = ref<Device[]>([])
const loading = ref(true)
const error = ref('')
const applying = ref(false)
const deleting = ref(false)
const applyNotice = ref('')

const nodeName = computed(() => {
  if (!forward.value?.nodeId) return '—'
  return nodes.value.find((node) => node.id === forward.value?.nodeId)?.name || forward.value.nodeId
})
const deviceName = computed(() => {
  if (!forward.value?.deviceId) return '—'
  return devices.value.find((device) => device.id === forward.value?.deviceId)?.name || forward.value.deviceId
})
const prettyOptions = computed(() => {
  const raw = forward.value?.options?.trim()
  if (!raw) return '{}'
  try {
    return JSON.stringify(JSON.parse(raw) as unknown, null, 2)
  } catch {
    return raw
  }
})

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    const [forwardResponse, nodeResponse, deviceResponse] = await Promise.all([
      forwardsApi.get(id.value),
      nodesApi.list().catch(() => ({ items: [], total: 0 })),
      devicesApi.list().catch(() => ({ items: [], total: 0 })),
    ])
    forward.value = forwardResponse
    nodes.value = nodeResponse.items
    devices.value = deviceResponse.items
  } catch (err) {
    error.value = errorMessage(err)
  } finally {
    loading.value = false
  }
}

async function apply(): Promise<void> {
  if (!forward.value) return
  applying.value = true
  applyNotice.value = ''
  try {
    const result = await forwardsApi.apply(forward.value.id)
    forward.value = result
    applyNotice.value = result.lastError
    toast.success('配置已推送')
    void pollSettled()
  } catch (err) {
    toast.error('下发失败', errorMessage(err))
  } finally {
    applying.value = false
  }
}

// The node reports the bind result asynchronously. Poll briefly so the badge
// moves from 下发中 to 已下发/异常 without a manual refresh.
async function pollSettled(): Promise<void> {
  for (let i = 0; i < 8; i++) {
    await new Promise((resolve) => setTimeout(resolve, 1000))
    try {
      const latest = await forwardsApi.get(id.value)
      forward.value = latest
      if (latest.state !== 'applying') {
        applyNotice.value = latest.lastError
        if (latest.state === 'error') {
          toast.error('节点下发失败', latest.lastError)
        }
        return
      }
    } catch {
      return
    }
  }
}

async function remove(): Promise<void> {
  if (!forward.value) return
  const name = forward.value.name
  const ok = await confirmAction({
    title: '删除转发',
    message: `确认删除转发「${name}」吗？`,
    detail: '该操作不可撤销。删除后会同时关闭节点入口并移除 Agent 隧道配置。',
    confirmText: '删除转发',
    danger: true,
  })
  if (!ok) return
  deleting.value = true
  try {
    await forwardsApi.remove(forward.value.id)
    toast.success('转发已删除')
    await router.replace('/forwards')
  } catch (err) {
    toast.error('删除转发失败', errorMessage(err))
  } finally {
    deleting.value = false
  }
}

onMounted(load)
</script>

<template>
  <PageHeader
    eyebrow="FORWARDS / DETAIL"
    :title="forward?.name || '转发详情'"
    :description="forward ? `${kindLabel(forward.kind)} · ${forward.id}` : '转发配置、状态与下发说明。'"
  >
    <template #actions>
      <RouterLink to="/forwards" class="btn">
        <ChevronLeft :size="16" aria-hidden="true" />
        返回列表
      </RouterLink>
      <RouterLink v-if="forward" :to="`/forwards/${forward.id}/edit`" class="btn">
        <Pencil :size="16" aria-hidden="true" />
        编辑
      </RouterLink>
      <button
        v-if="forward"
        type="button"
        class="btn"
        :disabled="applying || forward.kind !== 'port'"
        :title="forward.kind !== 'port' ? '当前仅支持 port 类型下发' : ''"
        @click="apply"
      >
        <Loader2 v-if="applying" :size="16" class="spin" aria-hidden="true" />
        <Play v-else :size="16" aria-hidden="true" />
        下发配置
      </button>
      <button v-if="forward" type="button" class="btn btn-danger" :disabled="deleting" @click="remove">
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
    <EmptyState :icon="ArrowLeftRight" title="无法读取转发" :description="error">
      <template #actions>
        <button type="button" class="btn" @click="load">重试</button>
        <RouterLink to="/forwards" class="btn btn-primary">返回转发列表</RouterLink>
      </template>
    </EmptyState>
  </section>

  <div v-else-if="forward" class="stack">
    <div class="detail-grid">
      <section class="panel">
        <div class="panel-head">
          <div>
            <h2 class="panel-title">转发配置</h2>
            <p class="panel-sub">控制面保存的配置，未包含 Agent 实际观测状态</p>
          </div>
          <div class="panel-actions">
            <span class="badge badge-secondary">{{ kindLabel(forward.kind) }}</span>
            <StatusBadge :status="forward.state" />
          </div>
        </div>
        <div class="panel-body">
          <div class="kv-grid">
            <div class="kv">
              <div class="kv-label">转发 ID</div>
              <div class="kv-value mono">{{ forward.id }}</div>
            </div>
            <div class="kv">
              <div class="kv-label">节点</div>
              <div class="kv-value">{{ nodeName }}<div class="cell-sub mono">{{ forward.nodeId }}</div></div>
            </div>
            <div class="kv">
              <div class="kv-label">设备</div>
              <div class="kv-value">{{ deviceName }}<div v-if="forward.deviceId" class="cell-sub mono">{{ forward.deviceId }}</div></div>
            </div>
            <div class="kv">
              <div class="kv-label">客户端 ID</div>
              <div class="kv-value mono">{{ forward.clientId || '—' }}</div>
            </div>
            <div class="kv">
              <div class="kv-label">目标地址</div>
              <div class="kv-value mono">{{ forward.targetIp || '—' }}</div>
            </div>
            <div class="kv">
              <div class="kv-label">目标端口</div>
              <div class="kv-value mono">{{ forward.targetPort || '—' }}</div>
            </div>
            <div class="kv">
              <div class="kv-label">公开入口端口</div>
              <div class="kv-value mono">{{ forward.remotePort || '—' }}</div>
            </div>
            <div class="kv">
              <div class="kv-label">域名</div>
              <div class="kv-value mono">{{ forward.domain || '—' }}</div>
            </div>
            <div class="kv">
              <div class="kv-label">标签</div>
              <div v-if="splitTags(forward.tags).length" class="tag-list" style="margin-top: 5px">
                <span v-for="tag in splitTags(forward.tags)" :key="tag" class="tag">{{ tag }}</span>
              </div>
              <div v-else class="kv-value muted">—</div>
            </div>
            <div class="kv">
              <div class="kv-label">流量</div>
              <div class="kv-value mono">{{ formatBytes(forward.trafficBytes) }}</div>
            </div>
            <div class="kv">
              <div class="kv-label">创建时间</div>
              <div class="kv-value">{{ formatTime(forward.createdAt) }}</div>
            </div>
            <div class="kv">
              <div class="kv-label">更新时间</div>
              <div class="kv-value">{{ formatTime(forward.updatedAt) }}</div>
            </div>
          </div>
        </div>
      </section>

      <div class="stack">
        <section class="panel">
          <div class="panel-head">
            <div>
              <h2 class="panel-title">状态与下发</h2>
              <p class="panel-sub">saved → applied → observed</p>
            </div>
          </div>
          <div class="panel-body">
            <div class="meta-list">
              <div class="meta-row">
                <span class="meta-label">当前状态</span>
                <span class="meta-value"><StatusBadge :status="forward.state" /></span>
              </div>
              <div class="meta-row">
                <span class="meta-label">配置版本</span>
                <span class="meta-value mono">{{ forward.revision || 0 }}</span>
              </div>
              <div class="meta-row">
                <span class="meta-label">最近错误</span>
                <span class="meta-value">{{ forward.lastError || '无' }}</span>
              </div>
            </div>

            <div class="notice notice-info" style="margin-top: 14px">
              <AlertTriangle :size="16" aria-hidden="true" />
              <div>
                <strong>下发会把配置推送到节点和 Agent。</strong>
                节点绑定入口端口后状态变为「已下发」，失败时会显示具体错误。
                <template v-if="forward.kind !== 'port'">当前仅支持 port 类型下发。</template>
              </div>
            </div>

            <div v-if="applyNotice" class="notice notice-warning" role="status" style="margin-top: 10px">
              {{ applyNotice }}
            </div>

            <div v-if="forward.lastError" class="notice notice-danger" role="alert" style="margin-top: 10px">
              <AlertTriangle :size="16" aria-hidden="true" />
              <div><strong>Agent 回报错误。</strong>{{ forward.lastError }}</div>
            </div>

            <button
              type="button"
              class="btn btn-primary"
              style="width: 100%; margin-top: 14px"
              :disabled="applying || forward.kind !== 'port'"
              @click="apply"
            >
              <Loader2 v-if="applying" :size="16" class="spin" aria-hidden="true" />
              <Play v-else :size="16" aria-hidden="true" />
              {{ applying ? '正在下发…' : '下发配置' }}
            </button>
          </div>
        </section>
      </div>
    </div>

    <section class="panel">
      <div class="panel-head">
        <div>
          <h2 class="panel-title">高级选项</h2>
          <p class="panel-sub">原样保存的 JSON 选项</p>
        </div>
      </div>
      <div class="panel-body">
        <pre class="code-block">{{ prettyOptions }}</pre>
      </div>
    </section>
  </div>
</template>
