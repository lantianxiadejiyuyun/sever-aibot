<script setup lang="ts">
import { RouterLink } from 'vue-router'
import { Compass } from 'lucide-vue-next'
import PageHeader from '@/components/ui/PageHeader.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
</script>

<template>
  <PageHeader eyebrow="SYSTEM / 404" title="页面不存在" />
  <section class="panel">
    <EmptyState
      :icon="Compass"
      title="找不到这个页面"
      description="链接可能已失效，或功能路径发生了调整。返回概览后可以继续操作。"
    >
      <template #actions>
        <RouterLink to="/" class="btn btn-primary">返回概览</RouterLink>
      </template>
    </EmptyState>
  </section>
</template>
