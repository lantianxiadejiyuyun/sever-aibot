<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import { ChevronLeft, KeyRound, Loader2, Network, Pencil, Trash2 } from 'lucide-vue-next'
import PageHeader from '@/components/ui/PageHeader.vue'
import StatusBadge from '@/components/ui/StatusBadge.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import SecretDialog from '@/components/ui/SecretDialog.vue'
import { nodesApi } from '@/api/endpoints'
import { errorMessage } from '@/api/client'
import { confirmAction } from '@/composables/useConfirm'
import { useToast } from '@/composables/useToast'
import type { Node } from '@/api/types'
import { formatTime, splitTags, timeAgo } from '@/utils/format'
import { capabilityItems } from '@/utils/node'

const route = useRoute()
const router = useRouter()
const toast = useToast()

const id = computed(() => String(route.params.id))
const node = ref<Node | null>(null)
const loading = ref(true)
const error = ref('')
const deleting = ref(false)
const rotating = ref(false)
const secretOpen = ref(false)
const nodeKey = ref('')

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    node.value = await nodesApi.get(id.value)
  } catch (err) {
    error.value = errorMessage(err)
  } finally {
    loading.value = false
  }
}

async function remove(): Promise<void> {
  if (!node.value) return
  const name = node.value.name
  const ok = await confirmAction({
    title: '删除节点',
    message: `确认删除节点「${name}」吗？`,
    detail: '引用该节点的转发将无法继续下发。该操作不可撤销。',
    confirmText: '删除节点',
    danger: true,
  })
  if (!ok) return
  deleting.value = true
  try {
    await nodesApi.remove(node.value.id)
    toast.success('节点已删除')
    await router.replace('/nodes')
  } catch (err) {
    toast.error('删除节点失败', errorMessage(err))
  } finally {
    deleting.value = false
  }
}

async function rotate(): Promise<void> {
  if (!node.value) return
  const ok = await confirmAction({
    title: '轮换节点密钥',
    message: `确认轮换节点「${node.value.name}」的密钥吗？`,
    detail: '旧密钥立即失效，节点会断开；需要用新密钥重启节点程序。',
    confirmText: '轮换密钥',
    danger: true,
  })
  if (!ok) return
  rotating.value = true
  try {
    const response = await nodesApi.rotateKey(node.value.id)
    nodeKey.value = response.nodeKey
    secretOpen.value = true
    toast.success('节点密钥已轮换')
    await load()
  } catch (err) {
    toast.error('轮换密钥失败', errorMessage(err))
  } finally {
    rotating.value = false
  }
}

onMounted(load)
</script>

<template>
  <PageHeader
    eyebrow="NODES / DETAIL"
    :title="node?.name || '节点详情'"
    :description="node ? `节点 ID：${node.id}` : '公网中继节点配置与能力。'"
  >
    <template #actions>
      <RouterLink to="/nodes" class="btn">
        <ChevronLeft :size="16" aria-hidden="true" />
        返回列表
      </RouterLink>
      <RouterLink v-if="node" :to="`/nodes/${node.id}/edit`" class="btn">
        <Pencil :size="16" aria-hidden="true" />
        编辑
      </RouterLink>
      <button v-if="node" type="button" class="btn" :disabled="rotating" @click="rotate">
        <Loader2 v-if="rotating" :size="16" class="spin" aria-hidden="true" />
        <KeyRound v-else :size="16" aria-hidden="true" />
        轮换密钥
      </button>
      <button v-if="node" type="button" class="btn btn-danger" :disabled="deleting" @click="remove">
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
    <EmptyState :icon="Network" title="无法读取节点" :description="error">
      <template #actions>
        <button type="button" class="btn" @click="load">重试</button>
        <RouterLink to="/nodes" class="btn btn-primary">返回节点列表</RouterLink>
      </template>
    </EmptyState>
  </section>

  <div v-else-if="node" class="stack">
    <div class="detail-grid">
      <section class="panel">
        <div class="panel-head">
          <div>
            <h2 class="panel-title">基本配置</h2>
            <p class="panel-sub">地址、协议与域名能力</p>
          </div>
          <div class="panel-actions"><StatusBadge :status="node.status" /></div>
        </div>
        <div class="panel-body">
          <div class="kv-grid">
            <div class="kv">
              <div class="kv-label">节点 ID</div>
              <div class="kv-value mono">{{ node.id }}</div>
            </div>
            <div class="kv">
              <div class="kv-label">接入地址</div>
              <div class="kv-value mono">{{ node.addr || '—' }}</div>
            </div>
            <div class="kv">
              <div class="kv-label">连接协议</div>
              <div class="kv-value">{{ node.protocol?.toUpperCase() || '—' }}</div>
            </div>
            <div class="kv">
              <div class="kv-label">HTTP 端口</div>
              <div class="kv-value mono">{{ node.httpPort || '未设置' }}</div>
            </div>
            <div class="kv">
              <div class="kv-label">替换地址</div>
              <div class="kv-value mono">{{ node.replaceAddress || '—' }}</div>
            </div>
            <div class="kv">
              <div class="kv-label">节点域名</div>
              <div class="kv-value mono">{{ node.domain || '—' }}</div>
            </div>
            <div class="kv">
              <div class="kv-label">URL 模板</div>
              <div class="kv-value mono">{{ node.urlTemplate || '—' }}</div>
            </div>
            <div class="kv">
              <div class="kv-label">禁止域名前缀</div>
              <div class="kv-value mono">{{ node.denyDomainPrefix || '—' }}</div>
            </div>
            <div class="kv">
              <div class="kv-label">允许通配符</div>
              <div class="kv-value">{{ node.allowWildcard ? '是' : '否' }}</div>
            </div>
            <div class="kv">
              <div class="kv-label">端口段</div>
              <div class="kv-value mono">{{ node.forwardPorts || '未限制' }}</div>
            </div>
            <div class="kv">
              <div class="kv-label">最大连接池</div>
              <div class="kv-value mono">{{ node.maxPoolCount || '默认' }}</div>
            </div>
            <div class="kv">
              <div class="kv-label">P2P 关闭转发</div>
              <div class="kv-value">{{ node.p2pDisableForward ? '是' : '否' }}</div>
            </div>
            <div class="kv">
              <div class="kv-label">标签</div>
              <div v-if="splitTags(node.tags).length" class="tag-list" style="margin-top: 5px">
                <span v-for="tag in splitTags(node.tags)" :key="tag" class="tag">{{ tag }}</span>
              </div>
              <div v-else class="kv-value muted">—</div>
            </div>
            <div class="kv">
              <div class="kv-label">创建 / 更新时间</div>
              <div class="kv-value">
                {{ formatTime(node.createdAt) }}<br />
                <span class="muted">{{ formatTime(node.updatedAt) }}</span>
              </div>
            </div>
            <div v-if="node.remark" class="kv" style="grid-column: 1 / -1">
              <div class="kv-label">备注</div>
              <div class="kv-value" style="white-space: pre-wrap">{{ node.remark }}</div>
            </div>
          </div>
        </div>
      </section>

      <div class="stack">
        <section class="panel">
          <div class="panel-head">
            <div>
              <h2 class="panel-title">能力开关</h2>
              <p class="panel-sub">转发类型必须与节点能力匹配</p>
            </div>
          </div>
          <div class="panel-body">
            <div class="cap-badges">
              <span
                v-for="cap in capabilityItems(node)"
                :key="cap.label"
                class="cap-badge"
                :class="{ 'is-on': cap.on }"
              >
                {{ cap.label }} · {{ cap.on ? '启用' : '关闭' }}
              </span>
            </div>
            <div class="meta-list" style="margin-top: 16px">
              <div class="meta-row">
                <span class="meta-label">当前状态</span>
                <span class="meta-value"><StatusBadge :status="node.status" /></span>
              </div>
              <div class="meta-row">
                <span class="meta-label">最近心跳</span>
                <span class="meta-value">{{ timeAgo(node.lastSeenAt) }}</span>
              </div>
              <div class="meta-row">
                <span class="meta-label">最近心跳时间</span>
                <span class="meta-value mono">{{ formatTime(node.lastSeenAt) }}</span>
              </div>
            </div>
          </div>
        </section>

        <section class="panel">
          <div class="panel-head">
            <div>
              <h2 class="panel-title">配额与限速</h2>
              <p class="panel-sub">节点侧执行的上限参数</p>
            </div>
          </div>
          <div class="panel-body">
            <div class="meta-list">
              <div class="meta-row">
                <span class="meta-label">流量上限</span>
                <span class="meta-value">{{ node.limitTotalGb || '不限制' }}<span v-if="node.limitTotalGb"> GB</span></span>
              </div>
              <div class="meta-row">
                <span class="meta-label">计量方式</span>
                <span class="meta-value mono">{{ node.limitKind || '默认' }}</span>
              </div>
              <div class="meta-row">
                <span class="meta-label">重置日</span>
                <span class="meta-value">{{ node.limitResetDay || '不重置' }}</span>
              </div>
              <div class="meta-row">
                <span class="meta-label">限速</span>
                <span class="meta-value">{{ node.limiterMbps || '不限速' }}<span v-if="node.limiterMbps"> Mbps</span></span>
              </div>
              <div class="meta-row">
                <span class="meta-label">并发上限</span>
                <span class="meta-value">{{ node.concurrency || '默认' }}</span>
              </div>
              <div class="meta-row">
                <span class="meta-label">连接数上限</span>
                <span class="meta-value">{{ node.connections || '默认' }}</span>
              </div>
            </div>
          </div>
        </section>
      </div>
    </div>

    <section class="panel">
      <div class="panel-head">
        <div>
          <h2 class="panel-title">节点规则</h2>
          <p class="panel-sub">原样保存的自定义规则文本</p>
        </div>
      </div>
      <div class="panel-body">
        <pre v-if="node.rules" class="code-block">{{ node.rules }}</pre>
        <p v-else class="muted" style="font-size: 12.5px">未配置额外规则。</p>
      </div>
    </section>

    <SecretDialog
      :open="secretOpen"
      :secret="nodeKey"
      :subject="node?.name || ''"
      title="节点密钥已轮换"
      subtitle="请立即更新节点程序的 AIBOT_NODE_KEY 并重启"
      @close="secretOpen = false"
    />
  </div>
</template>
