<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { X } from 'lucide-vue-next'
import SidebarContent from './SidebarContent.vue'
import AppTopbar from './AppTopbar.vue'
import BottomNav from './BottomNav.vue'
import { siteName, announcement, siteError, online, loadSiteSettings } from '@/composables/useConsoleShell'

const route = useRoute()
const drawerOpen = ref(false)

function openDrawer(): void {
  drawerOpen.value = true
  document.body.style.overflow = 'hidden'
  window.addEventListener('keydown', onKeydown)
}

function closeDrawer(): void {
  drawerOpen.value = false
  document.body.style.overflow = ''
  window.removeEventListener('keydown', onKeydown)
}

function onKeydown(event: KeyboardEvent): void {
  if (event.key === 'Escape') closeDrawer()
}

watch(
  () => route.fullPath,
  () => {
    if (drawerOpen.value) closeDrawer()
  },
)

function connectivityChanged() {
  online.value = navigator.onLine
  if (online.value) void loadSiteSettings()
}
function refreshSettings() { if (online.value) void loadSiteSettings() }
watch(() => route.path, (_path, previous) => { if (previous === '/settings') refreshSettings() })
onMounted(() => {
  connectivityChanged()
  window.addEventListener('online', connectivityChanged)
  window.addEventListener('offline', connectivityChanged)
  window.addEventListener('focus', refreshSettings)
})
onBeforeUnmount(() => {
  window.removeEventListener('online', connectivityChanged)
  window.removeEventListener('offline', connectivityChanged)
  window.removeEventListener('focus', refreshSettings)
  if (drawerOpen.value) closeDrawer()
  window.removeEventListener('keydown', onKeydown)
})
</script>

<template>
  <div class="app-shell">
    <aside class="app-sidebar">
      <SidebarContent />
    </aside>

    <div class="app-main">
      <AppTopbar @menu="openDrawer" />
      <main class="page-container">
        <p class="site-heading">{{ siteName }}</p>
        <p v-if="announcement" class="notice notice-info site-announcement" role="status">{{ announcement }}</p>
        <p v-if="siteError" class="notice notice-warning" role="alert">站点设置加载失败：{{ siteError }} <button class="btn btn-sm" :disabled="!online" @click="refreshSettings">重试</button></p>
        <div v-if="!online" class="notice notice-warning" role="alert">当前离线，资产、凭据和终端操作已暂停。连接网络后将重新加载页面数据；离线不提供缓存的业务数据。</div>
        <RouterView v-else />
      </main>
    </div>

    <BottomNav />
  </div>

  <Teleport to="body">
    <div v-if="drawerOpen" class="drawer-backdrop" @click.self="closeDrawer">
      <div class="drawer" role="dialog" aria-modal="true" aria-label="导航菜单">
        <button
          type="button"
          class="icon-btn"
          style="align-self: flex-end; margin-bottom: 6px"
          aria-label="关闭导航菜单"
          @click="closeDrawer"
        >
          <X :size="18" aria-hidden="true" />
        </button>
        <SidebarContent @navigate="closeDrawer" />
      </div>
    </div>
  </Teleport>
</template>
