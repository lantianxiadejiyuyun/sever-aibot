import { readonly, reactive } from 'vue'

export type ToastKind = 'success' | 'error' | 'warning' | 'info'

export interface ToastItem {
  id: number
  kind: ToastKind
  message: string
  detail?: string
  timeout: number
}

const toasts = reactive<ToastItem[]>([])
let nextId = 1

export function dismissToast(id: number): void {
  const index = toasts.findIndex((toast) => toast.id === id)
  if (index >= 0) toasts.splice(index, 1)
}

export function pushToast(kind: ToastKind, message: string, detail?: string, timeout = 4200): number {
  const id = nextId++
  toasts.push({ id, kind, message, detail, timeout })
  if (timeout > 0) {
    window.setTimeout(() => dismissToast(id), timeout)
  }
  return id
}

export function useToast() {
  return {
    toasts: readonly(toasts),
    success: (message: string, detail?: string) => pushToast('success', message, detail),
    error: (message: string, detail?: string) => pushToast('error', message, detail, 5600),
    warning: (message: string, detail?: string) => pushToast('warning', message, detail, 5600),
    info: (message: string, detail?: string) => pushToast('info', message, detail),
    dismiss: dismissToast,
  }
}
