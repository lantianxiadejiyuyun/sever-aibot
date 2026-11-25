import { ref } from 'vue'
import { request, errorMessage } from '@/api/client'

export const siteName = ref('AIBOT 运维控制台')
export const announcement = ref('')
export const siteError = ref('')
export const online = ref(typeof navigator === 'undefined' || navigator.onLine)
export const layoutMode = ref<'auto' | 'mobile' | 'desktop'>('auto')
let currentTitle = ''
export function setPageTitle(title = '') {
  currentTitle = title
  document.title = title ? `${title} · ${siteName.value}` : siteName.value
}
export async function loadSiteSettings() {
  try {
    const settings = await request<{ siteName: string; announcement: string }>('/settings')
    siteName.value = settings.siteName || 'AIBOT 运维控制台'
    announcement.value = settings.announcement || ''
    siteError.value = ''
    setPageTitle(currentTitle)
  } catch (error) { siteError.value = errorMessage(error) }
}
export function setLayoutMode(mode: 'auto' | 'mobile' | 'desktop') {
  layoutMode.value = mode
  document.documentElement.dataset.layout = mode
  document.querySelector('meta[name="viewport"]')?.setAttribute('content', mode === 'desktop'
    ? 'width=1280, viewport-fit=cover'
    : 'width=device-width, initial-scale=1, viewport-fit=cover')
}
