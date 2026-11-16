<script setup lang="ts">
import { RouterLink, useRoute } from 'vue-router'
import { bottomNavItems, isNavActive } from '@/navigation'

const route = useRoute()
</script>

<template>
  <nav class="bottom-nav" aria-label="移动端主导航">
    <RouterLink
      v-for="item in bottomNavItems"
      :key="item.to"
      :to="item.to"
      class="bottom-nav-link"
      :class="{ 'is-active': isNavActive(route.path, item.to) }"
      :aria-current="isNavActive(route.path, item.to) ? 'page' : undefined"
    >
      <component :is="item.icon" :size="20" aria-hidden="true" />
      <span>{{ item.label }}</span>
    </RouterLink>
  </nav>
</template>
