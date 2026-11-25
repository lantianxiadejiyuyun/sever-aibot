<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import {
  AlertTriangle,
  ArrowLeftRight,
  ChevronLeft,
  Loader2,
  Pencil,
  Play,
 Square,
  Trash2,
} from 'lucide-vue-next'
import PageHeader from '@/components/ui/PageHeader.vue'
import StatusBadge from '@/components/ui/StatusBadge.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import { devicesApi, forwardsApi, nodesApi } from '@/api/endpoints'
import { errorMessage } from '@/api/client'
import { forwardRuntimeApi, type TrafficDay, type ForwardRuntime } from '@/api/forwardRuntime'
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
const stopping = ref(false)
const traffic = ref<TrafficDay[]>([])
const runtime = ref<ForwardRuntime | null>(null)
const runtimeError = ref('')
let disposed = false
onUnmounted(() => { disposed = true })
async function refreshRuntime(): Promise<void> {
 try { const [state, days] = await Promise.all([forwardRuntimeApi.runtime(id.value), forwardRuntimeApi.traffic(id.value)]); runtime.value = state; traffic.value = days.items; runtimeError.value = '' } catch (err) { runtimeError.value = errorMessage(err) }
}

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
 await refreshRuntime()
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
    const result = await forwardRuntimeApi.start(forward.value.id)
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
  for (let i = 0; i < 30 && !disposed; i++) {
    await new Promise((resolve) => setTimeout(resolve, 1000))
    try {
      const latest = await forwardsApi.get(id.value)
      forward.value = latest
      await refreshRuntime()
 if (!['applying', 'stopping'].includes(latest.state)) {
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

async function stop(): Promise<void> {
 if (!forward.value) return
 stopping.value = true
 try { forward.value = await forwardRuntimeApi.stop(forward.value.id); toast.success('已请求停止，等待节点确认撤销'); void pollSettled() } catch (err) { toast.error('停止失败', errorMessage(err)) } finally { stopping.value = false }
}
async function remove(): Promise<void> {
  if (!forward.value) return
  const name = forward.value.name
  const ok = await confirmAction({
    title: '删除转发',
    message: `确认删除转发「${name}」吗？`,
    detail: '仅已停止并确认撤销的转发可以删除。历史日流量继续保留。',
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
        :disabled="applying || stopping"
        @click="apply"
      >
        <Loader2 v-if="applying" :size="16" class="spin" aria-hidden="true" />
        <Play v-else :size="16" aria-hidden="true" />
        下发配置
      </button>
      <button v-if="forward" type="button" class="btn" :disabled="stopping || applying || ['stopping', 'stopped'].includes(forward.state)" @click="stop"><Square :size="16" aria-hidden="true" />{{ forward.state === 'stopping' ? '等待撤销确认' : '停止' }}</button>
      <button v-if="forward" type="button" class="btn btn-danger" :disabled="deleting || !['saved', 'stopped'].includes(forward.state)" title="停止并确认撤销后可删除" @click="remove">
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
            <p class="panel-sub">配置版本与真实业务流量</p>
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
              <p class="panel-sub">启动 → 监听确认 → 业务流量观测；停止 → 撤销确认</p>
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
                <template v-if="forward.kind === 'p2p'">尝试认证 TCP 直连；配置 UDP 会合地址后尝试 NAT 打洞与 QUIC 直连，失败时按策略回退中继。开启节点额度、限速或连接限制时使用中继。</template>
                <template v-if="forward.kind === 'tunnel' || forward.kind === 'proxy'">访问设备仅开放 127.0.0.1 上的私有入口。</template>
                <template v-if="forward.kind === 'proxy'">使用 HTTP CONNECT 或 SOCKS5 CONNECT；仅允许此规则指定的目标地址和端口。</template>
                <template v-if="forward.kind === 'host'">HTTP 域名路由使用节点 HTTP 入口端口；HTTPS 需前置代理终止 TLS。</template>
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
              :disabled="applying || stopping"
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
      <div class="panel-head"><div><h2 class="panel-title">每日业务流量 · UTC</h2><p class="panel-sub">上行：入口到目标；下行：目标到入口。统计业务字节，未包含隧道开销。</p></div><button class="btn" @click="refreshRuntime">刷新观测</button></div>
      <div class="panel-body">
        <p v-if="runtime">节点{{ runtime.nodeOnline ? '在线' : '离线' }} · 服务设备{{ runtime.agentOnline ? '在线' : '离线' }} · revision {{ runtime.revision }}</p>
        <p v-if="runtime && forward.kind === 'p2p'">最近连接路径：{{ ({ direct: '认证直连（TCP / QUIC）', relay: '中继回退', unavailable: '连接不可用', not_observed: '尚未建立业务连接' } as Record<string, string>)[runtime.p2pStatus] || runtime.p2pStatus }}。实际结果取决于网络 NAT 和防火墙策略。</p>
        <p v-if="runtimeError" role="alert">{{ runtimeError }}</p>
        <p v-else-if="!traffic.length" class="muted">尚未收到真实业务流量样本。</p>
        <table v-else class="table"><thead><tr><th>日期</th><th>上行</th><th>下行</th></tr></thead><tbody><tr v-for="day in traffic" :key="day.day"><td>{{ day.day }}</td><td>{{ formatBytes(day.uploadBytes) }}</td><td>{{ formatBytes(day.downloadBytes) }}</td></tr></tbody></table>
        <p class="muted">节点重连会重发未确认的报告；节点进程重启前尚未确认的内存样本可能丢失。</p>
      </div>
    </section>
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
