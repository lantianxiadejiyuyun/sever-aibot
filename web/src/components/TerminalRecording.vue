<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { Terminal } from '@xterm/xterm'
import '@xterm/xterm/css/xterm.css'
import { errorMessage } from '@/api/client'
const props = defineProps<{ sessionId: string }>()
const emit = defineEmits<{ close: [] }>()
const host = ref<HTMLElement | null>(null)
const error = ref(''), loaded = ref(false), playing = ref(false), elapsed = ref(0), duration = ref(0), speed = ref(1)
type Event = [number, 'o' | 'r', string]
let events: Event[] = [], terminal: Terminal | undefined, index = 0, frame = 0, last = 0, cols = 120, rows = 30, generation = 0
const controller = new AbortController()
function pause() { playing.value = false; generation++; cancelAnimationFrame(frame) }
async function tick(now: number) {
  if (!playing.value) return
  const epoch = generation
  elapsed.value = Math.min(duration.value, elapsed.value + (now - last) / 1000 * speed.value); last = now
  while (index < events.length && events[index]![0] <= elapsed.value) {
    const event = events[index++]!
    if (event[1] === 'o' && terminal) { const target = terminal; await new Promise<void>(resolve => target.write(event[2],resolve)); if (epoch !== generation) return }
    else { const size = event[2].match(/^(\d+)x(\d+)$/); if (size) { const c=Number(size[1]),r=Number(size[2]); if(c>=1&&c<=500&&r>=1&&r<=200)terminal?.resize(c,r) } }
  }
  if (index === events.length) pause()
  else frame = requestAnimationFrame(tick)
}
function play() { if (!loaded.value || playing.value || index === events.length) return; playing.value = true; last = performance.now(); frame = requestAnimationFrame(tick) }
function createTerminal() { terminal?.dispose(); if (!host.value) return; host.value.replaceChildren(); terminal = new Terminal({ cols, rows, disableStdin:true, cursorBlink:false, fontSize:13, scrollback:3000 }); terminal.open(host.value) }
function restart() { pause(); index = 0; elapsed.value = 0; createTerminal(); play() }
onMounted(async () => {
  try {
    const response = await fetch(`/api/v1/access/sessions/${encodeURIComponent(props.sessionId)}/recording`, { credentials:'same-origin', signal:controller.signal })
    if (!response.ok) {
      const body = await response.json().catch(() => null)
      throw new Error(body?.error?.message || '录制读取失败，请确认会话权限。')
    }
    const text = await response.text()
    if (text.length > 32 * 1024 * 1024) throw new Error('录制过大，请下载后在本地回放。')
    const lines = text.trim().split('\n'), header = JSON.parse(lines.shift() || '{}')
    if (header.version !== 2) throw new Error('不支持的录制格式')
    cols = Math.max(1,Math.min(500,Number(header.width)||120)); rows = Math.max(1,Math.min(200,Number(header.height)||30))
    events = lines.filter(Boolean).map(line => JSON.parse(line)).filter((e): e is Event => Array.isArray(e)&&e.length===3&&Number.isFinite(e[0])&&e[0]>=0&&(e[1]==='o'||e[1]==='r')&&typeof e[2]==='string').sort((a,b)=>a[0]-b[0])
    duration.value = events.at(-1)?.[0] ?? 0
    await nextTick(); if (controller.signal.aborted || !host.value) return
    createTerminal(); loaded.value = true
  } catch (e) { if (!controller.signal.aborted) error.value = errorMessage(e) }
})
onBeforeUnmount(() => { controller.abort(); pause(); terminal?.dispose(); events = [] })
</script>
<template>
<section class="panel recording" aria-label="终端录制回放">
  <div class="panel-body">
    <h3>终端录制回放</h3>
    <p class="muted">只播放已记录的终端输出，不会连接远程设备或发送输入。</p>
    <p v-if="error" class="notice notice-danger" role="alert">{{ error }}</p>
    <div class="playback-controls">
      <button class="btn" :disabled="!loaded" @click="playing ? pause() : play()">{{ playing ? '暂停' : '播放' }}</button>
      <button class="btn" :disabled="!loaded" @click="restart">重新播放</button>
      <label>速度 <select v-model.number="speed" class="input"><option :value="1">1×</option><option :value="2">2×</option><option :value="4">4×</option><option :value="8">8×</option></select></label>
      <span role="status">{{ elapsed.toFixed(1) }} / {{ duration.toFixed(1) }} 秒</span>
      <button class="btn" @click="emit('close')">关闭回放</button>
    </div>
  </div>
  <div ref="host" class="recording-screen"></div>
</section>
</template>
<style scoped>
.recording{margin-top:20px;min-width:0}.playback-controls{display:flex;gap:10px;align-items:center;flex-wrap:wrap}.playback-controls label{display:flex;gap:8px;align-items:center}.recording-screen{background:#000;padding:12px;overflow:auto;max-height:650px}
</style>
