<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import { ArrowLeft, Menu, Settings } from 'lucide-vue-next'
import { navGroupForPath } from '@/navigation'
import { session } from '@/stores/session'
import ThemeToggle from '@/components/ui/ThemeToggle.vue'

const emit = defineEmits<{ menu: [] }>()
const route = useRoute()
const router = useRouter()

const groupLabel = computed(() => navGroupForPath(route.path))
const title = computed(() => route.meta.title || '控制台')
const showBack = computed(() => Boolean(route.meta.parent) && route.path !== '/')
const fallbackParent = computed(() => (route.meta.parent as string) || '/')
const username = computed(() => session.user?.username || '管理员')
const initial = computed(() => username.value.slice(0, 1).toUpperCase())

function goBack(): void {
  const state = window.history.state as { back?: string | null } | null
  if (state && state.back) {
    router.back()
  } else {
    void router.push(fallbackParent.value)
  }
}
</script>

<template>
  <header class="topbar">
    <div class="topbar-left">
      <button
        type="button"
        class="icon-btn mobile-menu-btn"
        aria-label="打开导航菜单"
        @click="emit('menu')"
      >
        <Menu :size="19" aria-hidden="true" />
      </button>
      <button
        v-if="showBack"
        type="button"
        class="icon-btn mobile-menu-btn"
        aria-label="返回上一页"
        @click="goBack"
      >
        <ArrowLeft :size="19" aria-hidden="true" />
      </button>
      <div class="crumb">
        <span>{{ groupLabel }}</span>
        <span class="crumb-sep" aria-hidden="true">/</span>
        <strong>{{ title }}</strong>
      </div>
    </div>
    <div class="topbar-right">
      <span class="health-badge">
        <span class="dot dot-success" aria-hidden="true"></span>
        控制面在线
      </span>
      <ThemeToggle />
      <RouterLink to="/settings" class="avatar" :aria-label="`当前用户 ${username}，打开设置`" :title="username">
        {{ initial }}
      </RouterLink>
    </div>
  </header>
</template>
