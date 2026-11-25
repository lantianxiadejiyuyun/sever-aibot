import { createApp } from 'vue'
import App from './App.vue'
import router from './router'
import { clearSession } from './stores/session'
import { setUnauthorizedHandler } from './api/client'
import './styles/base.css'
import './styles/console-shell.css'

setUnauthorizedHandler(() => {
  clearSession()
  const current = router.currentRoute.value
  if (current.path === '/login') return
  void router.replace({ path: '/login', query: { redirect: current.fullPath } })
})

createApp(App).use(router).mount('#app')

if (import.meta.env.PROD && 'serviceWorker' in navigator) {
  window.addEventListener('load', () => {
    void navigator.serviceWorker.register('/sw.js', { scope: '/', updateViaCache: 'none' }).catch(() => {
      console.warn('离线静态页面不可用，控制台仍可联网使用。')
    })
  })
}
