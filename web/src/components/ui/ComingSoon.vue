<script setup lang="ts">
import type { Component } from 'vue'
import { Sparkles } from 'lucide-vue-next'

withDefaults(
  defineProps<{
    title: string
    description: string
    icon?: Component
    planned?: string[]
  }>(),
  { icon: () => Sparkles, planned: () => [] },
)
</script>

<template>
  <section class="panel">
    <div class="empty-state">
      <div class="empty-icon"><component :is="icon" :size="26" aria-hidden="true" /></div>
      <div class="empty-title">{{ title }}</div>
      <p class="empty-desc">{{ description }}</p>
      <div v-if="planned.length" class="stack-sm" style="margin-top: 10px; text-align: left; width: min(460px, 100%)">
        <div v-for="item in planned" :key="item" class="flex" style="gap: 8px">
          <span class="dot dot-info" aria-hidden="true"></span>
          <span class="text-2" style="font-size: 12.5px">{{ item }}</span>
        </div>
      </div>
    </div>
  </section>
</template>
