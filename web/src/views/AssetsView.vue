<script setup lang="ts">
import { Boxes } from 'lucide-vue-next'
import PageHeader from '@/components/ui/PageHeader.vue'
import ComingSoon from '@/components/ui/ComingSoon.vue'
</script>

<template>
  <PageHeader eyebrow="MANAGE / ASSETS" title="资产" />
  <ComingSoon
    :icon="Boxes"
    title="资产管理界面即将上线"
    description="后端接口已就绪，界面尚未接入。"
    :planned="[
      '资产列表、详情与编辑界面',
      '凭据中心界面（复用 step-up 验证）',
      '访问路径与 SSH 配置展示',
      '资产连通性检查',
    ]"
  />
</template>
