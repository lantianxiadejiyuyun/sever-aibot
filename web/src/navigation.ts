import type { Component } from 'vue'
import {
  ArrowLeftRight,
  Boxes,
  KeyRound,
  Bell,
  Bot,
  Database,
  GitPullRequest,
  ClipboardCheck,
  Gauge,
  LayoutDashboard,
  Network,
  ScrollText,
  Server,
  Settings,
} from 'lucide-vue-next'

export interface NavItem {
  label: string
  to: string
  icon: Component
}

export interface NavGroup {
  label: string
  items: NavItem[]
}

export const navGroups: NavGroup[] = [
  {
    label: '监控',
    items: [
      { label: '概览', to: '/', icon: LayoutDashboard },
      { label: '巡检', to: '/inspections', icon: ClipboardCheck },
      { label: 'AI 助手', to: '/ai', icon: Bot },
      { label: '性能', to: '/metrics', icon: Gauge },
    ],
  },
  {
    label: '管理',
    items: [
      { label: '设备', to: '/devices', icon: Server },
      { label: '节点', to: '/nodes', icon: Network },
      { label: '转发', to: '/forwards', icon: ArrowLeftRight },
      { label: '资产', to: '/assets', icon: Boxes },
      { label: '凭据', to: '/credentials', icon: KeyRound },
      { label: '数据源', to: '/datasources', icon: Database },
      { label: '变更', to: '/changes', icon: GitPullRequest },
    ],
  },
  {
    label: '系统',
    items: [
      { label: '通知', to: '/notifications', icon: Bell },
      { label: '审计', to: '/audit', icon: ScrollText },
      { label: '设置', to: '/settings', icon: Settings },
    ],
  },
]

export const bottomNavItems: NavItem[] = [
  { label: '概览', to: '/', icon: LayoutDashboard },
  { label: '设备', to: '/devices', icon: Server },
  { label: '节点', to: '/nodes', icon: Network },
  { label: '转发', to: '/forwards', icon: ArrowLeftRight },
  { label: '设置', to: '/settings', icon: Settings },
]

export function isNavActive(path: string, to: string): boolean {
  if (to === '/') return path === '/'
  return path === to || path.startsWith(`${to}/`)
}

export function navGroupForPath(path: string): string {
  for (const group of navGroups) {
    for (const item of group.items) {
      if (isNavActive(path, item.to)) return group.label
    }
  }
  return '控制台'
}
