<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Monitor, Moon, RefreshCw, Sun } from 'lucide-vue-next'
import PageHeader from '@/components/ui/PageHeader.vue'
import SystemManagement from '@/components/SystemManagement.vue'
import AccountSecurity from '@/components/AccountSecurity.vue'
import StatusBadge from '@/components/ui/StatusBadge.vue'
import { fetchHealth } from '@/api/endpoints'
import { errorMessage } from '@/api/client'
import { session } from '@/stores/session'
import { useTheme } from '@/composables/useTheme'
import type { ThemeMode } from '@/composables/useTheme'
import { useLogout } from '@/composables/useLogout'
import { formatTime } from '@/utils/format'
import type { HealthResponse } from '@/api/types'

const { mode, resolved, setMode } = useTheme()
const { signOut, pending } = useLogout()

const themeOptions: Array<{ value: ThemeMode; label: string; icon: typeof Monitor; hint: string }> = [
  { value: 'auto', label: '跟随系统', icon: Monitor, hint: '随浏览器或系统外观实时切换' },
  { value: 'light', label: '浅色', icon: Sun, hint: '始终使用浅色企业控制台' },
  { value: 'dark', label: '深色', icon: Moon, hint: '始终使用深色控制台' },
]

const username = computed(() => session.user?.username || '—')
const initial = computed(() => username.value.slice(0, 1).toUpperCase())

const health = ref<HealthResponse | null>(null)
const healthLoading = ref(false)
const healthError = ref('')

const buildVersion = __APP_VERSION__
const buildTime = __BUILD_TIME__
const buildMode = import.meta.env.MODE

async function loadHealth(): Promise<void> {
  healthLoading.value = true
  healthError.value = ''
  try {
    health.value = await fetchHealth()
  } catch (err) {
    healthError.value = errorMessage(err)
  } finally {
    healthLoading.value = false
  }
}

onMounted(loadHealth)
</script>

<template>
  <PageHeader eyebrow="SYSTEM / SETTINGS" title="设置" />

  <div class="stack">
    <SystemManagement />
    <AccountSecurity />
    <section class="panel">
      <div class="panel-head">
        <div>
          <h2 class="panel-title">主题模式</h2>
          <p class="panel-sub">
            当前：{{ mode === 'auto' ? `跟随系统（${resolved === 'dark' ? '深色' : '浅色'}）` : resolved === 'dark' ? '深色' : '浅色' }}
          </p>
        </div>
        <div class="panel-actions">
          <span class="sample-badge">localStorage · aibot-theme-mode</span>
        </div>
      </div>
      <div class="panel-body">
        <div class="theme-options" role="group" aria-label="主题模式">
          <button
            v-for="option in themeOptions"
            :key="option.value"
            type="button"
            class="theme-option"
            :class="{ 'is-active': mode === option.value }"
            :aria-pressed="mode === option.value"
            @click="setMode(option.value)"
          >
            <span class="theme-swatch" :class="option.value" aria-hidden="true"></span>
            <span class="grow">
              <span class="flex" style="gap: 7px">
                <component :is="option.icon" :size="15" aria-hidden="true" />
                <strong style="font-size: 12.5px">{{ option.label }}</strong>
              </span>
              <span class="muted" style="display: block; font-size: 11px; margin-top: 1px">{{ option.hint }}</span>
            </span>
          </button>
        </div>
        <p class="form-hint" style="margin-top: 10px">
          默认跟随系统，并在系统外观变化时实时更新。手动选择会持久化到当前浏览器，不写入服务端。
        </p>
      </div>
    </section>

    <div class="detail-grid">
      <section class="panel">
        <div class="panel-head">
          <div>
            <h2 class="panel-title">账户</h2>
            <p class="panel-sub">当前控制台会话</p>
          </div>
        </div>
        <div class="panel-body">
          <div class="user-card" style="padding: 12px">
            <span class="avatar" aria-hidden="true">{{ initial }}</span>
            <div class="user-card-main">
              <strong>{{ username }}</strong>
              <small>超级管理员</small>
            </div>
            <StatusBadge status="online" label="已登录" />
          </div>
          <div class="meta-list" style="margin-top: 16px">
            <div class="meta-row">
              <span class="meta-label">会话过期时间</span>
              <span class="meta-value mono">{{ formatTime(session.expiresAt) }}</span>
            </div>
            <div class="meta-row">
              <span class="meta-label">会话 Cookie</span>
              <span class="meta-value">HttpOnly · SameSite=Lax</span>
            </div>
            <div class="meta-row">
              <span class="meta-label">CSRF 令牌</span>
              <span class="meta-value">仅保存在内存</span>
            </div>
          </div>
          <button type="button" class="btn btn-danger" style="margin-top: 16px" :disabled="pending" @click="signOut">
            {{ pending ? '正在退出…' : '退出登录' }}
          </button>
        </div>
      </section>

      <section class="panel">
        <div class="panel-head">
          <div>
            <h2 class="panel-title">控制面</h2>
            <p class="panel-sub">/healthz 实时结果</p>
          </div>
          <div class="panel-actions">
            <button type="button" class="btn btn-sm" :disabled="healthLoading" @click="loadHealth">
              <RefreshCw :size="14" :class="{ spin: healthLoading }" aria-hidden="true" />
              刷新
            </button>
          </div>
        </div>
        <div class="panel-body">
          <div v-if="health" class="meta-list">
            <div class="meta-row">
              <span class="meta-label">状态</span>
              <span class="meta-value"><StatusBadge :status="health.status" :label="health.status === 'ok' ? '正常' : health.status" /></span>
            </div>
            <div class="meta-row">
              <span class="meta-label">数据库</span>
              <span class="meta-value">{{ health.db ? '可用' : '不可用' }}</span>
            </div>
            <div class="meta-row">
              <span class="meta-label">在线 Agent</span>
              <span class="meta-value mono">{{ health.agentsOnline }}</span>
            </div>
            <div class="meta-row">
              <span class="meta-label">控制面版本</span>
              <span class="meta-value mono">{{ health.version }}</span>
            </div>
            <div class="meta-row">
              <span class="meta-label">安全 Cookie</span>
              <span class="meta-value">{{ health.sessionCookie ? '已启用' : '未启用' }}</span>
            </div>
          </div>
          <p v-else-if="healthError" class="form-error" role="alert">{{ healthError }}</p>
          <p v-else class="muted" style="font-size: 12.5px">正在读取控制面健康状态…</p>
        </div>
      </section>
    </div>

    <section class="panel">
      <div class="panel-head">
        <div>
          <h2 class="panel-title">构建信息</h2>
          <p class="panel-sub">当前前端构建产物</p>
        </div>
      </div>
      <div class="panel-body">
        <div class="kv-grid">
          <div class="kv">
            <div class="kv-label">控制台版本</div>
            <div class="kv-value mono">{{ buildVersion }}</div>
          </div>
          <div class="kv">
            <div class="kv-label">构建时间 (UTC)</div>
            <div class="kv-value mono">{{ buildTime }}</div>
          </div>
          <div class="kv">
            <div class="kv-label">构建模式</div>
            <div class="kv-value mono">{{ buildMode }}</div>
          </div>
          <div class="kv">
            <div class="kv-label">路由模式</div>
            <div class="kv-value">History（Go SPA fallback）</div>
          </div>
          <div class="kv">
            <div class="kv-label">API 来源</div>
            <div class="kv-value mono">same-origin · /api/v1</div>
          </div>
          <div class="kv">
            <div class="kv-label">凭证存储</div>
            <div class="kv-value">仅内存 CSRF · Cookie 会话</div>
          </div>
        </div>
        <div class="notice notice-info" style="margin-top: 16px">
          控制台不会把管理员密码、设备密钥、CSRF 令牌或任何会话值写入 localStorage。
        </div>
      </div>
    </section>
  </div>
</template>
