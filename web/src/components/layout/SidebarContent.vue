<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import { LogOut } from 'lucide-vue-next'
import BrandMark from './BrandMark.vue'
import { isNavActive, navGroups } from '@/navigation'
import { session } from '@/stores/session'
import { useLogout } from '@/composables/useLogout'

const emit = defineEmits<{ navigate: [] }>()
const route = useRoute()
const { signOut, pending } = useLogout()

const username = computed(() => session.user?.username || '管理员')
const initial = computed(() => username.value.slice(0, 1).toUpperCase())
</script>

<template>
  <BrandMark />
  <nav class="nav-groups" aria-label="主导航">
    <div v-for="group in navGroups" :key="group.label">
      <div class="nav-group-label">{{ group.label }}</div>
      <RouterLink
        v-for="item in group.items"
        :key="item.to"
        :to="item.to"
        class="nav-item"
        :class="{ 'is-active': isNavActive(route.path, item.to) }"
        @click="emit('navigate')"
      >
        <component :is="item.icon" :size="18" class="nav-icon" aria-hidden="true" />
        <span>{{ item.label }}</span>
      </RouterLink>
    </div>
  </nav>
  <div class="sidebar-footer">
    <div class="user-card">
      <span class="avatar" aria-hidden="true">{{ initial }}</span>
      <RouterLink to="/settings" class="user-card-main" @click="emit('navigate')">
        <strong>{{ username }}</strong>
        <small>超级管理员</small>
      </RouterLink>
      <button
        type="button"
        class="btn btn-ghost btn-icon"
        aria-label="退出登录"
        title="退出登录"
        :disabled="pending"
        @click="signOut"
      >
        <LogOut :size="17" aria-hidden="true" />
      </button>
    </div>
  </div>
</template>
