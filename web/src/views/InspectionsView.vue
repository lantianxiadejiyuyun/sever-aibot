<script setup lang="ts">
import { ClipboardCheck } from 'lucide-vue-next'
import PageHeader from '@/components/ui/PageHeader.vue'
import ComingSoon from '@/components/ui/ComingSoon.vue'
</script>

<template>
  <PageHeader eyebrow="MONITOR / INSPECTIONS" title="巡检" />
  <ComingSoon
    :icon="ClipboardCheck"
    title="巡检功能即将上线"
    description="该功能尚未进入当前里程碑。"
    :planned="[
      '巡检任务排期与手动触发',
      '磁盘、内存、负载、服务与证书检查项',
      '巡检评分、发现项与历史对比',
      '可导出报告与通知渠道联动',
    ]"
  />
</template>
