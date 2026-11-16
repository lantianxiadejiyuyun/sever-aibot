<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { X } from 'lucide-vue-next'
import SidebarContent from './SidebarContent.vue'
import AppTopbar from './AppTopbar.vue'
import BottomNav from './BottomNav.vue'

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

onBeforeUnmount(() => {
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
        <RouterView />
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
