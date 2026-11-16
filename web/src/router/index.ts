import { createRouter, createWebHistory } from 'vue-router'
import type { RouteRecordRaw } from 'vue-router'
import { bootstrapSession, session } from '@/stores/session'
import { safeRedirect } from './redirect'

declare module 'vue-router' {
  interface RouteMeta {
    title?: string
    requiresAuth?: boolean
    parent?: string
  }
}

const routes: RouteRecordRaw[] = [
  {
    path: '/login',
    name: 'login',
    component: () => import('@/views/LoginView.vue'),
    meta: { title: '登录', requiresAuth: false },
  },
  {
    path: '/',
    component: () => import('@/components/layout/AppShell.vue'),
    meta: { requiresAuth: true },
    children: [
      {
        path: '',
        name: 'overview',
        component: () => import('@/views/OverviewView.vue'),
        meta: { title: '概览', requiresAuth: true },
      },
      {
        path: 'inspections',
        name: 'inspections',
        component: () => import('@/views/InspectionsView.vue'),
        meta: { title: '巡检', requiresAuth: true },
      },
      {
        path: 'metrics',
        name: 'metrics',
        component: () => import('@/views/MetricsView.vue'),
        meta: { title: '性能', requiresAuth: true },
      },
      {
        path: 'assets',
        name: 'assets',
        component: () => import('@/views/AssetsView.vue'),
        meta: { title: '资产', requiresAuth: true },
      },
      {
        path: 'devices',
        name: 'devices',
        component: () => import('@/views/DevicesView.vue'),
        meta: { title: '设备', requiresAuth: true },
      },
      {
        path: 'devices/new',
        name: 'device-new',
        component: () => import('@/views/DeviceCreateView.vue'),
        meta: { title: '新建设备', requiresAuth: true, parent: '/devices' },
      },
      {
        path: 'devices/:id',
        name: 'device-detail',
        component: () => import('@/views/DeviceDetailView.vue'),
        meta: { title: '设备详情', requiresAuth: true, parent: '/devices' },
      },
      {
        path: 'devices/:id/edit',
        name: 'device-edit',
        component: () => import('@/views/DeviceEditView.vue'),
        meta: { title: '编辑设备', requiresAuth: true, parent: '/devices' },
      },
      {
        path: 'nodes',
        name: 'nodes',
        component: () => import('@/views/NodesView.vue'),
        meta: { title: '节点', requiresAuth: true },
      },
      {
        path: 'nodes/new',
        name: 'node-new',
        component: () => import('@/views/NodeCreateView.vue'),
        meta: { title: '新建节点', requiresAuth: true, parent: '/nodes' },
      },
      {
        path: 'nodes/:id',
        name: 'node-detail',
        component: () => import('@/views/NodeDetailView.vue'),
        meta: { title: '节点详情', requiresAuth: true, parent: '/nodes' },
      },
      {
        path: 'nodes/:id/edit',
        name: 'node-edit',
        component: () => import('@/views/NodeEditView.vue'),
        meta: { title: '编辑节点', requiresAuth: true, parent: '/nodes' },
      },
      {
        path: 'forwards',
        name: 'forwards',
        component: () => import('@/views/ForwardsView.vue'),
        meta: { title: '转发', requiresAuth: true },
      },
      {
        path: 'forwards/new',
        name: 'forward-new',
        component: () => import('@/views/ForwardCreateView.vue'),
        meta: { title: '新建转发', requiresAuth: true, parent: '/forwards' },
      },
      {
        path: 'forwards/:id',
        name: 'forward-detail',
        component: () => import('@/views/ForwardDetailView.vue'),
        meta: { title: '转发详情', requiresAuth: true, parent: '/forwards' },
      },
      {
        path: 'forwards/:id/edit',
        name: 'forward-edit',
        component: () => import('@/views/ForwardEditView.vue'),
        meta: { title: '编辑转发', requiresAuth: true, parent: '/forwards' },
      },
      {
        path: 'audit',
        name: 'audit',
        component: () => import('@/views/AuditView.vue'),
        meta: { title: '审计', requiresAuth: true },
      },
      {
        path: 'settings',
        name: 'settings',
        component: () => import('@/views/SettingsView.vue'),
        meta: { title: '设置', requiresAuth: true },
      },
      {
        path: ':pathMatch(.*)*',
        name: 'not-found',
        component: () => import('@/views/NotFoundView.vue'),
        meta: { title: '页面不存在', requiresAuth: true },
      },
    ],
  },
]

const router = createRouter({
  history: createWebHistory(import.meta.env.BASE_URL),
  routes,
  scrollBehavior(to, _from, savedPosition) {
    if (savedPosition) return savedPosition
    if (to.hash) {
      const reduced =
        typeof window !== 'undefined' &&
        window.matchMedia?.('(prefers-reduced-motion: reduce)').matches
      return { el: to.hash, behavior: reduced ? 'auto' : 'smooth' }
    }
    return { top: 0 }
  },
})

router.beforeEach(async (to) => {
  await bootstrapSession()

  const requiresAuth = to.matched.some((record) => record.meta.requiresAuth)
  if (requiresAuth && !session.user) {
    return { path: '/login', query: { redirect: to.fullPath } }
  }

  if (to.path === '/login' && session.user) {
    const target = safeRedirect(to.query.redirect)
    return target || { path: '/' }
  }

  return true
})

router.afterEach((to) => {
  const title = to.meta.title ? `${to.meta.title} · AIBOT 运维控制台` : 'AIBOT 运维控制台'
  document.title = title
})

export default router
