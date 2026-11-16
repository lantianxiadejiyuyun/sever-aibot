import { computed, readonly, ref } from 'vue'

export type ThemeMode = 'auto' | 'light' | 'dark'
export type ResolvedTheme = 'light' | 'dark'

const STORAGE_KEY = 'aibot-theme-mode'
const LIGHT_COLOR = '#f4f7fb'
const DARK_COLOR = '#0f141a'

function readStoredMode(): ThemeMode {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (raw === 'light' || raw === 'dark' || raw === 'auto') return raw
  } catch {
    /* ignore storage failures */
  }
  return 'auto'
}

const mode = ref<ThemeMode>(readStoredMode())
const systemDark = ref(false)
const initialized = ref(false)

const query = typeof window !== 'undefined' && window.matchMedia
  ? window.matchMedia('(prefers-color-scheme: dark)')
  : null

systemDark.value = query ? query.matches : false

export const resolvedTheme = computed<ResolvedTheme>(() => {
  if (mode.value === 'auto') return systemDark.value ? 'dark' : 'light'
  return mode.value
})

function updateThemeColor(theme: ResolvedTheme): void {
  const themeMeta = document.getElementById('theme-color')
  if (themeMeta) themeMeta.setAttribute('content', theme === 'dark' ? DARK_COLOR : LIGHT_COLOR)
  const statusMeta = document.getElementById('apple-status')
  if (statusMeta) statusMeta.setAttribute('content', theme === 'dark' ? 'black-translucent' : 'default')
}

function apply(): void {
  if (typeof document === 'undefined') return
  const theme = resolvedTheme.value
  const root = document.documentElement
  root.dataset.theme = theme
  root.dataset.themeMode = mode.value
  root.style.colorScheme = theme
  updateThemeColor(theme)
}

function onSystemChange(event: MediaQueryListEvent): void {
  systemDark.value = event.matches
  apply()
}

function init(): void {
  if (initialized.value || typeof window === 'undefined') return
  initialized.value = true
  apply()
  if (query) {
    if (typeof query.addEventListener === 'function') {
      query.addEventListener('change', onSystemChange)
    } else if (typeof query.addListener === 'function') {
      // Safari < 14
      query.addListener(onSystemChange)
    }
  }
}

export function setThemeMode(next: ThemeMode): void {
  mode.value = next
  try {
    localStorage.setItem(STORAGE_KEY, next)
  } catch {
    /* ignore storage failures */
  }
  apply()
}

export function useTheme() {
  init()
  return {
    mode: readonly(mode),
    resolved: readonly(resolvedTheme),
    setMode: setThemeMode,
  }
}
