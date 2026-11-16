<script setup lang="ts">
import { nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { X } from 'lucide-vue-next'

const props = withDefaults(
  defineProps<{
    open: boolean
    title: string
    subtitle?: string
    wide?: boolean
    closeOnBackdrop?: boolean
  }>(),
  { subtitle: '', wide: false, closeOnBackdrop: true },
)

const emit = defineEmits<{ close: [] }>()

const panel = ref<HTMLElement | null>(null)
let previousFocus: HTMLElement | null = null
let previousOverflow = ''
let active = false

const focusableSelector = [
  'a[href]',
  'button:not([disabled])',
  'textarea:not([disabled])',
  'input:not([disabled])',
  'select:not([disabled])',
  '[tabindex]:not([tabindex="-1"])',
].join(',')

function onKeydown(event: KeyboardEvent): void {
  if (!active) return
  if (event.key === 'Escape') {
    event.preventDefault()
    emit('close')
    return
  }
  if (event.key !== 'Tab' || !panel.value) return
  const nodes = Array.from(panel.value.querySelectorAll<HTMLElement>(focusableSelector)).filter(
    (el) => el.offsetParent !== null,
  )
  if (nodes.length === 0) {
    event.preventDefault()
    panel.value.focus()
    return
  }
  const first = nodes[0]
  const last = nodes[nodes.length - 1]
  if (event.shiftKey && document.activeElement === first) {
    event.preventDefault()
    last.focus()
  } else if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault()
    first.focus()
  }
}

function lock(): void {
  if (active) return
  active = true
  previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null
  previousOverflow = document.body.style.overflow
  document.body.style.overflow = 'hidden'
  window.addEventListener('keydown', onKeydown, true)
}

function unlock(): void {
  if (!active) return
  active = false
  window.removeEventListener('keydown', onKeydown, true)
  document.body.style.overflow = previousOverflow
  previousFocus?.focus?.()
  previousFocus = null
}

watch(
  () => props.open,
  async (open) => {
    if (open) {
      lock()
      await nextTick()
      const target =
        panel.value?.querySelector<HTMLElement>('[data-autofocus]') ||
        panel.value?.querySelector<HTMLElement>(focusableSelector) ||
        panel.value
      target?.focus()
    } else {
      unlock()
    }
  },
  { immediate: true },
)

onBeforeUnmount(unlock)

function onBackdropClick(event: MouseEvent): void {
  if (props.closeOnBackdrop && event.target === event.currentTarget) emit('close')
}
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="modal-backdrop" @click="onBackdropClick">
      <section
        ref="panel"
        class="modal-panel"
        :class="{ 'modal-wide': wide }"
        role="dialog"
        aria-modal="true"
        :aria-label="title"
        tabindex="-1"
      >
        <header class="modal-head">
          <div class="grow">
            <h2 class="modal-title">{{ title }}</h2>
            <p v-if="subtitle" class="modal-sub">{{ subtitle }}</p>
          </div>
          <button type="button" class="modal-close" aria-label="关闭对话框" @click="emit('close')">
            <X :size="19" aria-hidden="true" />
          </button>
        </header>
        <div class="modal-body">
          <slot />
        </div>
        <footer v-if="$slots.footer" class="modal-foot">
          <slot name="footer" />
        </footer>
      </section>
    </div>
  </Teleport>
</template>
