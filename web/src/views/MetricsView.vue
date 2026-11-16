<script setup lang="ts">
import { Gauge } from 'lucide-vue-next'
import PageHeader from '@/components/ui/PageHeader.vue'
import ComingSoon from '@/components/ui/ComingSoon.vue'
</script>

<template>
  <PageHeader eyebrow="MONITOR / METRICS" title="性能" />
  <ComingSoon
    :icon="Gauge"
    title="性能监控即将上线"
    description="该功能尚未进入当前里程碑。"
    :planned="[
      'CPU、内存、磁盘与网络时间序列',
      '设备与节点维度筛选',
      '流量、连接数与带宽上限对照',
      '阈值告警与采集保留策略',
    ]"
  />
</template>
