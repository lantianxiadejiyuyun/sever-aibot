import { createApp } from 'vue'
import App from './App.vue'
import router from './router'
import { clearSession } from './stores/session'
import { setUnauthorizedHandler } from './api/client'
import './styles/base.css'

setUnauthorizedHandler(() => {
  clearSession()
  const current = router.currentRoute.value
  if (current.path === '/login') return
  void router.replace({ path: '/login', query: { redirect: current.fullPath } })
})

createApp(App).use(router).mount('#app')
