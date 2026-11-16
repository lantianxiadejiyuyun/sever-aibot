import { readonly, reactive } from 'vue'

export interface ConfirmOptions {
  title: string
  message: string
  detail?: string
  confirmText?: string
  cancelText?: string
  danger?: boolean
}

interface ConfirmState extends ConfirmOptions {
  open: boolean
}

const state = reactive<ConfirmState>({
  open: false,
  title: '',
  message: '',
  detail: '',
  confirmText: '确认',
  cancelText: '取消',
  danger: false,
})

let resolver: ((value: boolean) => void) | null = null

export function confirmAction(options: ConfirmOptions): Promise<boolean> {
  if (resolver) {
    resolver(false)
    resolver = null
  }
  Object.assign(state, {
    open: true,
    title: options.title,
    message: options.message,
    detail: options.detail ?? '',
    confirmText: options.confirmText ?? '确认',
    cancelText: options.cancelText ?? '取消',
    danger: options.danger ?? false,
  })
  return new Promise<boolean>((resolve) => {
    resolver = resolve
  })
}

export function resolveConfirm(value: boolean): void {
  state.open = false
  resolver?.(value)
  resolver = null
}

export function useConfirm() {
  return {
    state: readonly(state),
    confirm: confirmAction,
    resolve: resolveConfirm,
  }
}
