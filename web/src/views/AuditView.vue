<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { AlertTriangle, Loader2, RefreshCw, ScrollText, Search } from 'lucide-vue-next'
import PageHeader from '@/components/ui/PageHeader.vue'
import StatusBadge from '@/components/ui/StatusBadge.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import { auditApi } from '@/api/endpoints'
import { errorMessage } from '@/api/client'
import type { AuditLog } from '@/api/types'
import { actionLabel, formatTime } from '@/utils/format'

const items = ref<AuditLog[]>([])
const loading = ref(true)
const error = ref('')
const query = ref('')
const resultFilter = ref('')

const filtered = computed(() => {
  const q = query.value.trim().toLowerCase()
  return items.value.filter((entry) => {
    if (resultFilter.value && entry.result !== resultFilter.value) return false
    if (!q) return true
    return [entry.actor, entry.action, entry.target, entry.detail, entry.ip, entry.userAgent, entry.result]
      .filter(Boolean)
      .some((value) => value.toLowerCase().includes(q))
  })
})

function detailText(detail: string): string {
  if (!detail) return ''
  return detail.length > 140 ? `${detail.slice(0, 140)}…` : detail
}

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    const response = await auditApi.list(200)
    items.value = response.items
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
    eyebrow="SYSTEM / AUDIT"
    title="审计"
  >
    <template #actions>
      <button type="button" class="btn" :disabled="loading" @click="load">
        <Loader2 v-if="loading" :size="16" class="spin" aria-hidden="true" />
        <RefreshCw v-else :size="16" aria-hidden="true" />
        刷新
      </button>
    </template>
  </PageHeader>

  <section class="panel">
    <div class="filter-bar">
      <div class="search-box">
        <Search :size="16" aria-hidden="true" />
        <input v-model="query" type="search" placeholder="搜索操作者、动作、对象、IP…" aria-label="搜索审计日志" />
      </div>
      <div class="chip-row" role="group" aria-label="按结果筛选">
        <button type="button" class="chip" :class="{ 'is-active': resultFilter === '' }" @click="resultFilter = ''">全部</button>
        <button type="button" class="chip" :class="{ 'is-active': resultFilter === 'ok' }" @click="resultFilter = 'ok'">成功</button>
        <button type="button" class="chip" :class="{ 'is-active': resultFilter === 'denied' }" @click="resultFilter = 'denied'">拒绝</button>
      </div>
      <span class="pill">共 {{ items.length }} 条 · 筛选后 {{ filtered.length }} 条</span>
    </div>

    <div v-if="error" class="notice notice-danger" role="alert" style="margin: 14px">
      <AlertTriangle :size="17" aria-hidden="true" />
      <div class="grow"><strong>读取审计日志失败。</strong>{{ error }}</div>
      <button type="button" class="btn btn-sm" @click="load">重试</button>
    </div>

    <div v-if="loading && items.length === 0" class="panel-body" aria-hidden="true">
      <div v-for="i in 6" :key="i" class="skeleton skeleton-line" style="height: 36px; margin-bottom: 10px"></div>
    </div>

    <div v-else-if="items.length === 0" class="panel-body">
      <EmptyState
        :icon="ScrollText"
        title="暂无审计记录"
        description="登录、设备、节点与转发的所有变更都会写入这里。"
      />
    </div>

    <div v-else-if="filtered.length === 0" class="panel-body">
      <EmptyState :icon="Search" title="没有匹配的记录" description="调整关键词或结果筛选后再试。">
        <template #actions>
          <button type="button" class="btn" @click="query = ''; resultFilter = ''">清空筛选</button>
        </template>
      </EmptyState>
    </div>

    <div v-else class="table-wrap mobile-cards">
      <table class="data-table">
        <thead>
          <tr>
            <th scope="col">时间</th>
            <th scope="col">操作者</th>
            <th scope="col">动作</th>
            <th scope="col">对象</th>
            <th scope="col">结果</th>
            <th scope="col">来源 IP</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="entry in filtered" :key="entry.id">
            <td data-label="时间"><span class="mono">{{ formatTime(entry.createdAt) }}</span></td>
            <td data-label="操作者"><span class="cell-primary">{{ entry.actor || 'system' }}</span></td>
            <td data-label="动作">
              <div>{{ actionLabel(entry.action) }}</div>
              <div class="cell-sub mono">{{ entry.action }}</div>
              <div v-if="entry.detail" class="cell-sub" :title="entry.detail">{{ detailText(entry.detail) }}</div>
            </td>
            <td data-label="对象">
              <span class="mono">{{ entry.target || '—' }}</span>
            </td>
            <td data-label="结果"><StatusBadge :status="entry.result" /></td>
            <td data-label="来源 IP">
              <div class="mono">{{ entry.ip || '—' }}</div>
              <div v-if="entry.userAgent" class="cell-sub" :title="entry.userAgent">{{ entry.userAgent }}</div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
</template>
