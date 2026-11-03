<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import {
  Activity,
  ClipboardList,
  RefreshCw,
  Play,
  ArrowUpRight,
  Bell,
  Clock,
  Server,
  Network,
} from 'lucide-vue-next'
import {
  s,
  host,
  api,
  run,
  query,
  scope,
  focusTask,
  changeTarget,
  handoff,
  when,
  label,
} from './store'
import Evidence from './Evidence.vue'
const props = defineProps({ reports: Boolean }),
  mon = ref({}),
  reports = ref([]),
  notes = ref([]),
  opened = ref(''),
  filter = ref('all')
let interval
async function load() {
  if (!s.host) return
  const h = s.host
  const [m, r, n] = await Promise.all([
    api('/monitor' + query({ host_id: h })),
    api('/reports' + query(scope())),
    api('/notifications'),
  ])
  mon.value = m
  reports.value = r
  notes.value = n.filter(
    (a) => a.host_id === h && (!s.service || s.service === a.service_id),
  )
  if (!opened.value) opened.value = r[0]?.id || ''
}
onMounted(() => {
  run('monitor', load)
  interval = setInterval(() => {
    if (!document.hidden) run('monitor', load)
  }, 30000)
})
onUnmounted(() => clearInterval(interval))
const changes = computed(() =>
  [
    ...notes.value.map((n) => ({
      at: n.created_at,
      name: nm(n.name) + ' · ' + label(n.after),
    })),
    ...s.tasks
      .filter((t) => ['forward', 'p2p', 'deploy'].includes(t.kind))
      .map((t) => ({
        at: t.updated_at,
        name: t.kind + ' / ' + t.action + ' · ' + label(t.status),
      })),
  ]
    .sort((a, b) => Date.parse(b.at) - Date.parse(a.at))
    .slice(0, 10),
)
const latest = computed(() => mon.value.latest || {}),
  metric = computed(() => latest.value.metrics || {}),
  rep = computed(() => reports.value.find((r) => r.id === opened.value)),
  recent = computed(() => reports.value[0])
const names = {
  systemd: '运行中的系统服务',
  docker: 'Docker 容器',
  listeners: '监听端口',
  nat: '系统 NAT',
  firewall: '防火墙放行',
  bt_version: '宝塔版本',
  bt_rules: '宝塔转发规则',
  uptime: '运行时长',
  load: '系统负载',
  cpu: 'CPU 使用率',
  memory: '内存',
  disk: '根分区',
  network: '网络收发',
  ssh: 'SSH 连接',
}
const nm = (k) =>
  names[k] ||
  (k?.includes(':')
    ? ({ public: '公网入口', health: '应用健康', bt_target: '宝塔转发目标' }[k.split(':')[0]] ||
        k.split(':')[0]) +
      ' · ' +
      (s.services.find((a) => a.id === k.split(':')[1])?.name ||
        k.split(':')[1])
    : k)
function itemService(item) {
  return /^(health|public|bt_target):(.+)$/.exec(item.name)?.[2]
}
function inScope(report, item) {
  if (!s.service || report.service_id) return true
  const id = itemService(item)
  // Aggregate gostc evidence refers to a rule ID, not its owning service.
  return id ? id === s.service : !item.name.startsWith('gostc:')
}
const trigger = (t) =>
  ({ manual: '手动巡检', scheduled: '定时巡检', deployment: '部署后巡检' })[
    t
  ] || t
function mem(v) {
  const t = Number(v?.match(/MemTotal:\s*(\d+)/)?.[1]),
    a = Number(v?.match(/MemAvailable:\s*(\d+)/)?.[1])
  return t && Number.isFinite(a) ? ((100 * (t - a)) / t).toFixed(1) + '%' : '—'
}
function totals(str) {
  if (!str) return null
  let rx = 0,
    tx = 0
  let ok = false
  for (const line of str.trim().split('\n')) {
    const [dev, a, b] = line.trim().split(/\s+/)
    if (dev === 'lo' || !Number.isFinite(+a) || !Number.isFinite(+b)) continue
    rx += +a
    tx += +b
    ok = true
  }
  return ok ? [rx, tx] : null
}
const samples = computed(() =>
  [...(mon.value.history || [])]
    .filter((x) => !x.stale && x.collected_at)
    .sort((a, b) => Date.parse(a.collected_at) - Date.parse(b.collected_at)),
)
const rates = computed(() =>
  samples.value
    .slice(1)
    .map((b, i) => {
      const a = samples.value[i],
        delta =
          (Date.parse(b.collected_at) - Date.parse(a.collected_at)) / 1000,
        x = totals(a.metrics?.network),
        y = totals(b.metrics?.network)
      if (!x || !y || delta <= 0 || y[0] < x[0] || y[1] < x[1]) return null
      return {
        at: b.collected_at,
        rx: (y[0] - x[0]) / delta / 1024,
        tx: (y[1] - x[1]) / delta / 1024,
      }
    })
    .filter(Boolean)
    .slice(-12),
)
function points(key) {
  const max = Math.max(1, ...rates.value.flatMap((a) => [a.rx, a.tx]))
  return rates.value
    .map(
      (e, i) =>
        (i * 300) / Math.max(1, rates.value.length - 1) +
        ',' +
        (55 - (e[key] * 50) / max),
    )
    .join(' ')
}
async function inspect() {
  const t = await api('/inspections', 'POST', scope())
  focusTask(t)
}
async function reportAI(item) {
  const report = rep.value
  const target = itemService(item) || report.service_id || s.service
  if (report.host_id !== s.host || !inScope(report, item))
    throw Error('当前目标已变化，请重新选择报告。')
  if (target !== s.service) await changeTarget(report.host_id, target)
  handoff(
    '请分析巡检报告 ' +
      report.id +
      ' 中「' +
      nm(item.name) +
      '」的结果。采集时间：' +
      item.time +
      '，来源：' +
      item.source +
      '。请区分观测证据和推测。',
    report,
  )
}
</script>
<template>
  <section class="work-panel">
    <div class="panel-head">
      <div class="row">
        <span class="mini-icon"
          ><component :is="props.reports ? ClipboardList : Activity"
        /></span>
        <h2>{{ props.reports ? '巡检报告' : '服务监控' }}</h2>
      </div>
      <button
        class="plain small"
        @click="run('monitor', load)"
        :disabled="s.busy.monitor || !host"
      >
        <RefreshCw />刷新
      </button>
    </div>
    <div class="panel-inner">
      <div v-if="!host" class="empty-state">
        <Server />
        <h3>尚未选择服务器</h3>
        <p>选择服务器后查看采集记录。</p>
      </div>
      <template v-else
        ><div class="row split wrap">
          <div>
            <small class="eyebrow">{{
              props.reports ? 'INSPECTION LOG' : 'SYSTEM TELEMETRY'
            }}</small>
            <h3>{{ host.name }}</h3>
          </div>
          <button :disabled="s.busy.inspect" @click="run('inspect', inspect)">
            <Play />立即巡检
          </button>
        </div>
        <template v-if="!props.reports"
          ><div class="sample-time">
            <Clock />最后成功采集：{{ when(latest.collected_at)
            }}<b v-if="latest.collected_at && (mon.stale || latest.stale)"
              >历史数据 · 已过期</b
            >
          </div>
          <div class="metrics">
            <div
              v-for="[key, val] in [
                ['CPU 使用率', metric.cpu == null ? '—' : metric.cpu + '%'],
                ['内存使用', mem(metric.memory)],
                ['磁盘使用', metric.disk?.match(/\d+%/)?.[0] || '—'],
                ['系统负载', metric.load || '—'],
              ]"
            >
              <small>{{ key }}</small
              ><b>{{ val }}</b>
            </div>
          </div>
          <p class="hint">
            运行时长：{{
              metric.uptime
                ? Math.floor(Number(metric.uptime) / 3600) + ' 小时'
                : '未采集'
            }}
          </p>
          <h3><Network />网络趋势</h3>
          <div v-if="rates.length" class="network-chart">
            <svg
              v-if="rates.length > 1"
              viewBox="0 0 300 60"
              preserveAspectRatio="none"
              role="img"
              aria-label="网络接收与发送速率趋势"
            >
              <polyline
                :points="points('rx')"
                fill="none"
                stroke="#398065"
                stroke-width="2"
              />
              <polyline
                :points="points('tx')"
                fill="none"
                stroke="#b07a18"
                stroke-width="2"
              /></svg
            ><small
              >接收 {{ rates.at(-1).rx.toFixed(1) }} · 发送
              {{ rates.at(-1).tx.toFixed(1) }} KiB/s</small
            >
          </div>
          <div v-else class="muted-box">至少两次有效采集后显示收发速率。</div>
          <h3>服务与入口证据</h3>
          <details
            v-for="(value, key) in Object.fromEntries(
              [
                'systemd',
                'docker',
                'listeners',
                'bt_version',
                'firewall',
                'nat',
              ]
                .filter((k) => metric[k] != null)
                .map((k) => [k, metric[k]]),
            )"
          >
            <summary>{{ nm(key) }}</summary>
            <Evidence :value="value || '未发现条目'" />
          </details>
          <details
            v-for="a in recent?.items.filter((x) =>
              inScope(recent, x) && /^(ssh|health:|public:|bt_rules|bt_target:|gostc)/.test(x.name),
            )"
          >
            <summary>
              {{ nm(a.name) }}<span class="pill">{{ label(a.status) }}</span>
            </summary>
            <Evidence :value="a" />
          </details>
          <h3>最近变化</h3>
          <div class="muted-box" v-if="!changes.length">
            当前目标尚无异常、恢复或变更任务。
          </div>
          <div class="report-item" v-for="c in changes">
            <small>{{ when(c.at) }}</small
            >{{ c.name }}
          </div></template
        >
        <template v-else
          ><details v-if="notes.length">
            <summary><Bell />异常与恢复 · {{ notes.length }}</summary>
            <div v-for="n in notes" class="report-item">
              {{ nm(n.name) }}：{{ label(n.before) }} → {{ label(n.after)
              }}<small>{{ when(n.created_at) }}</small>
            </div>
          </details>
          <p v-if="!reports.length" class="muted-box">
            还没有巡检报告。发起巡检后，可在任务记录中查看进度。
          </p>
          <template v-else
            ><label
              >历史报告<select v-model="opened">
                <option v-for="r in reports" :value="r.id">
                  {{ when(r.created_at) }} · {{ trigger(r.trigger) }}
                </option>
              </select></label
            ><template v-if="rep"
              ><p class="hint">报告编号 {{ rep.id }}<template v-if="!rep.service_id && s.service"> · 整机报告，仅列出主机级及可归属到当前服务的项目。</template></p>
              <details>
                <summary>
                  与上次相比 · {{ rep.changes?.filter((x) => inScope(rep, x)).length || 0 }} 项变化
                </summary>
                <Evidence :value="rep.changes?.filter((x) => inScope(rep, x))" />
              </details>
              <div class="segmented">
                <button
                  v-for="[k, v] in [
                    ['all', '全部'],
                    ['abnormal', '异常'],
                    ['unverified', '未验证'],
                    ['ok', '正常'],
                  ]"
                  :class="{ active: filter === k }"
                  @click="filter = k"
                >
                  {{ v }}
                </button>
              </div>
              <article
                class="report-item"
                v-for="a in rep.items.filter(
                  (x) => inScope(rep, x) && (filter === 'all' || x.status === filter),
                )"
              >
                <div class="row split">
                  <b>{{ nm(a.name) }}</b
                  ><span
                    class="pill"
                    :class="{ alert: a.status === 'abnormal' }"
                    >{{ label(a.status) }}</span
                  >
                </div>
                <small>{{ a.source }} · {{ when(a.time) }}</small
                ><Evidence :value="a.evidence" /><button
                  class="plain small"
                  @click="run('reportAI', () => reportAI(a))"
                >
                  交给 AI <ArrowUpRight />
                </button>
              </article>
              <p class="hint">{{ rep.suggestion }}</p></template
            ></template
          ></template
        ></template
      >
    </div>
  </section>
</template>
