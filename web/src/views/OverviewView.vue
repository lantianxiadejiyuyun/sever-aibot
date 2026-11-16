<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import {
  AlertTriangle,
  ArrowLeftRight,
  ArrowUpRight,
  Loader2,
  Network,
  RefreshCw,
  ScrollText,
  Server,
} from 'lucide-vue-next'
import PageHeader from '@/components/ui/PageHeader.vue'
import SampleTrafficChart from '@/components/ui/SampleTrafficChart.vue'
import StatusBadge from '@/components/ui/StatusBadge.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import { overviewApi } from '@/api/endpoints'
import { errorMessage } from '@/api/client'
import type { OverviewResponse } from '@/api/types'
import { actionLabel, timeAgo } from '@/utils/format'

const overview = ref<OverviewResponse | null>(null)
const loading = ref(true)
const error = ref('')

const counts = computed(() => overview.value?.counts)
const recentAudit = computed(() => overview.value?.recentAudit ?? [])
const offlineDevices = computed(() =>
  counts.value ? Math.max(0, counts.value.devices - counts.value.devicesOnline) : 0,
)
const inactiveForwards = computed(() =>
  counts.value ? Math.max(0, counts.value.forwards - counts.value.forwardsActive) : 0,
)
const anomalies = computed(() => offlineDevices.value + inactiveForwards.value)

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    overview.value = await overviewApi.get()
  } catch (err) {
    error.value = errorMessage(err)
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <PageHeader
    eyebrow="OVERVIEW / LIVE"
    title="概览"
  >
    <template #actions>
      <button type="button" class="btn" :disabled="loading" @click="load">
        <Loader2 v-if="loading" :size="16" class="spin" aria-hidden="true" />
        <RefreshCw v-else :size="16" aria-hidden="true" />
        刷新
      </button>
      <RouterLink to="/audit" class="btn btn-primary">
        <ScrollText :size="16" aria-hidden="true" />
        查看审计
      </RouterLink>
    </template>
  </PageHeader>

  <div v-if="error" class="notice notice-danger" role="alert" style="margin-bottom: 16px">
    <AlertTriangle :size="17" aria-hidden="true" />
    <div class="grow">
      <strong>读取概览失败。</strong>{{ error }}
    </div>
    <button type="button" class="btn btn-sm" @click="load">重试</button>
  </div>

  <div v-if="loading && !overview" class="kpi-grid" aria-hidden="true">
    <div v-for="i in 4" :key="i" class="panel stat-card">
      <div class="skeleton skeleton-line" style="width: 38%"></div>
      <div class="skeleton" style="height: 30px; width: 55%; margin-top: 14px"></div>
      <div class="skeleton skeleton-line" style="width: 65%; margin-top: 12px"></div>
    </div>
  </div>

  <div v-else-if="counts" class="kpi-grid">
    <section class="panel stat-card">
      <div class="stat-head">
        <span class="stat-icon"><Server :size="16" aria-hidden="true" /></span>
        在线设备
      </div>
      <div class="stat-value">
        {{ counts.devicesOnline }}<small>/ {{ counts.devices }}</small>
      </div>
      <div class="stat-foot">
        <span>已注册设备总数</span>
        <StatusBadge :status="offlineDevices === 0 ? 'online' : 'warning'" :label="offlineDevices === 0 ? '全部在线' : `${offlineDevices} 台离线`" />
      </div>
    </section>

    <section class="panel stat-card">
      <div class="stat-head">
        <span class="stat-icon"><Network :size="16" aria-hidden="true" /></span>
        节点
      </div>
      <div class="stat-value">
        {{ counts.nodesOnline }}<small>/ {{ counts.nodes }}</small>
      </div>
      <div class="stat-foot">
        <span>公网中继节点</span>
        <StatusBadge :status="counts.nodesOnline === counts.nodes && counts.nodes > 0 ? 'online' : 'warning'" :label="counts.nodes === 0 ? '尚未配置' : `${counts.nodesOnline} 在线`" />
      </div>
    </section>

    <section class="panel stat-card">
      <div class="stat-head">
        <span class="stat-icon"><ArrowLeftRight :size="16" aria-hidden="true" /></span>
        转发
      </div>
      <div class="stat-value">
        {{ counts.forwards }}<small>条</small>
      </div>
      <div class="stat-foot">
        <span>已生效 {{ counts.forwardsActive }} 条</span>
        <StatusBadge :status="inactiveForwards === 0 ? 'online' : 'saved'" :label="inactiveForwards === 0 ? '全部生效' : `${inactiveForwards} 条未生效`" />
      </div>
    </section>

    <section class="panel stat-card">
      <div class="stat-head">
        <span class="stat-icon" style="background: var(--warning-weak); color: var(--warning)">
          <AlertTriangle :size="16" aria-hidden="true" />
        </span>
        异常
      </div>
      <div class="stat-value">{{ anomalies }}<small>项</small></div>
      <div class="stat-foot">
        <span>离线设备 {{ offlineDevices }} · 未生效转发 {{ inactiveForwards }}</span>
        <StatusBadge :status="anomalies === 0 ? 'online' : 'error'" :label="anomalies === 0 ? '正常' : '需关注'" />
      </div>
    </section>
  </div>

  <div class="overview-grid">
    <div class="stack">
      <section class="panel">
        <div class="panel-head">
          <div>
            <h2 class="panel-title">近 7 天流量</h2>
            <p class="panel-sub">按天聚合的入口流量趋势</p>
          </div>
          <div class="panel-actions">
            <span class="sample-badge">示例数据</span>
            <span class="chart-legend"><span class="legend-dot"></span>流量</span>
          </div>
        </div>
        <div class="panel-body">
          <SampleTrafficChart />
          <div class="notice notice-info" style="margin-top: 14px">
            <AlertTriangle :size="16" aria-hidden="true" />
            <div>
              <strong>暂无采集。</strong>流量与性能采集将在 Agent / 指标里程碑提供，此图仅用于展示图表布局，不代表真实流量。
            </div>
          </div>
        </div>
      </section>

      <section class="panel">
        <div class="panel-head">
          <div>
            <h2 class="panel-title">最近操作</h2>
            <p class="panel-sub">来自控制面审计日志</p>
          </div>
          <div class="panel-actions">
            <RouterLink to="/audit" class="btn btn-sm">
              全部审计
              <ArrowUpRight :size="15" aria-hidden="true" />
            </RouterLink>
          </div>
        </div>
        <div class="panel-body">
          <div v-if="recentAudit.length" class="activity-list">
            <div v-for="entry in recentAudit" :key="entry.id" class="activity-item">
              <span class="activity-icon"><ScrollText :size="15" aria-hidden="true" /></span>
              <div class="activity-main">
                <div class="activity-title">
                  {{ actionLabel(entry.action) }}
                  <span class="muted">·</span>
                  {{ entry.actor }}
                </div>
                <div class="activity-meta">
                  {{ timeAgo(entry.createdAt) }}
                  <span v-if="entry.target"> · {{ entry.target }}</span>
                  <span v-if="entry.ip"> · {{ entry.ip }}</span>
                </div>
              </div>
              <StatusBadge :status="entry.result" />
            </div>
          </div>
          <EmptyState
            v-else
            :icon="ScrollText"
            title="暂无审计记录"
            description="登录、设备、节点与转发的每次变更都会记录在这里。"
          />
        </div>
      </section>
    </div>

    <section class="panel">
      <div class="panel-head">
        <div>
          <h2 class="panel-title">运行摘要</h2>
          <p class="panel-sub">只展示控制面已上报的真实计数</p>
        </div>
      </div>
      <div class="panel-body">
        <div class="meta-list">
          <div class="meta-row">
            <span class="meta-label">设备在线率</span>
            <span class="meta-value">
              <strong>{{ counts && counts.devices ? Math.round((counts.devicesOnline / counts.devices) * 100) : 0 }}%</strong>
            </span>
          </div>
          <div class="meta-row">
            <span class="meta-label">节点在线率</span>
            <span class="meta-value">
              <strong>{{ counts && counts.nodes ? Math.round((counts.nodesOnline / counts.nodes) * 100) : 0 }}%</strong>
            </span>
          </div>
          <div class="meta-row">
            <span class="meta-label">转发生效率</span>
            <span class="meta-value">
              <strong>{{ counts && counts.forwards ? Math.round((counts.forwardsActive / counts.forwards) * 100) : 0 }}%</strong>
            </span>
          </div>
          <div class="meta-row">
            <span class="meta-label">需要关注</span>
            <span class="meta-value">{{ anomalies }} 项</span>
          </div>
        </div>

        <div class="form-section" style="margin-top: 18px">
          <div class="form-section-title">快捷入口</div>
          <div class="stack-sm">
            <RouterLink to="/devices" class="btn" style="justify-content: space-between">
              <span class="flex"><Server :size="16" aria-hidden="true" />设备与密钥</span>
              <ArrowUpRight :size="15" aria-hidden="true" />
            </RouterLink>
            <RouterLink to="/nodes" class="btn" style="justify-content: space-between">
              <span class="flex"><Network :size="16" aria-hidden="true" />节点能力</span>
              <ArrowUpRight :size="15" aria-hidden="true" />
            </RouterLink>
            <RouterLink to="/forwards" class="btn" style="justify-content: space-between">
              <span class="flex"><ArrowLeftRight :size="16" aria-hidden="true" />转发条目</span>
              <ArrowUpRight :size="15" aria-hidden="true" />
            </RouterLink>
          </div>
        </div>
      </div>
    </section>
  </div>
</template>
