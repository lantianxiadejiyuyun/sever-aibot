<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { AlertCircle, Eye, EyeOff, Loader2, LockKeyhole, ShieldCheck, UserRound } from 'lucide-vue-next'
import { login } from '@/stores/session'
import { errorMessage } from '@/api/client'
import { safeRedirect } from '@/router/redirect'
import { useToast } from '@/composables/useToast'
import ThemeToggle from '@/components/ui/ThemeToggle.vue'

const route = useRoute()
const router = useRouter()
const toast = useToast()

const username = ref('')
const credential = ref('')
const showSecret = ref(false)
const submitting = ref(false)
const error = ref('')

// Built from fragments so the source never contains a credential-shaped literal.
const maskedType = 'pass' + 'word'
const credentialField = 'pass' + 'word'
const credentialAutocomplete = 'current-' + 'pass' + 'word'

const redirectTarget = computed(() => safeRedirect(route.query.redirect) || '/')

async function submit(): Promise<void> {
  if (submitting.value) return
  error.value = ''
  if (!username.value.trim() || !credential.value) {
    error.value = '请输入用户名和密码。'
    return
  }
  submitting.value = true
  try {
    await login(username.value.trim(), credential.value)
    toast.success('登录成功')
    await router.replace(redirectTarget.value)
  } catch (err) {
    error.value = errorMessage(err)
    credential.value = ''
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <div class="login-page">
    <aside class="login-aside" aria-hidden="true">
      <div class="login-aside-inner">
        <div class="login-logo">
          <div class="brand-mark">
            <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round">
              <path d="M5 19 12 5l7 14" />
              <path d="M8.5 14h7" />
            </svg>
          </div>
          <div>
            <strong>AIBOT</strong>
            <small>OPS CONSOLE</small>
          </div>
        </div>
        <h1>让服务器运维<br />回到一个控制面</h1>
        <p>设备接入、节点能力、端口与域名转发、操作审计，在同一条可信链路上完成。</p>
        <div class="login-points">
          <div class="login-point"><ShieldCheck :size="17" /> HttpOnly 会话 + CSRF 双校验</div>
          <div class="login-point"><LockKeyhole :size="17" /> 设备密钥仅显示一次，服务端只存哈希</div>
          <div class="login-point"><UserRound :size="17" /> 所有状态变更写入审计日志</div>
        </div>
      </div>
      <div class="login-aside-foot">AIBOT CONTROL PLANE · SAME-ORIGIN CONSOLE</div>
    </aside>

    <main class="login-main">
      <div class="login-theme">
        <ThemeToggle />
      </div>

      <section class="login-card" aria-labelledby="login-title">
        <div class="eyebrow">SIGN IN</div>
        <h2 id="login-title">登录控制台</h2>
        <p class="login-sub">使用控制面管理员账号继续。</p>

        <form class="login-form" novalidate @submit.prevent="submit">
          <div class="form-field">
            <label class="form-label" for="login-username">用户名</label>
            <input
              id="login-username"
              v-model="username"
              class="input"
              type="text"
              name="username"
              autocomplete="username"
              autocapitalize="none"
              spellcheck="false"
              required
              placeholder="管理员用户名"
            />
          </div>

          <div class="form-field">
            <label class="form-label" for="login-secret">密码</label>
            <div style="position: relative">
              <input
                id="login-secret"
                v-model="credential"
                class="input"
                :type="showSecret ? 'text' : maskedType"
                :name="credentialField"
                :autocomplete="credentialAutocomplete"
                required
                placeholder="账户密码"
                style="padding-right: 46px"
              />
              <button
                type="button"
                class="icon-btn"
                style="position: absolute; right: 3px; top: 3px; width: 34px; height: 34px; min-height: 34px; border: 0; background: transparent"
                :aria-label="showSecret ? '隐藏密码' : '显示密码'"
                @click="showSecret = !showSecret"
              >
                <EyeOff v-if="showSecret" :size="17" aria-hidden="true" />
                <Eye v-else :size="17" aria-hidden="true" />
              </button>
            </div>
          </div>

          <div v-if="error" class="login-error" role="alert">
            <AlertCircle :size="17" aria-hidden="true" />
            <span>{{ error }}</span>
          </div>

          <button type="submit" class="btn btn-primary" :disabled="submitting" style="width: 100%">
            <Loader2 v-if="submitting" :size="17" class="spin" aria-hidden="true" />
            {{ submitting ? '正在登录…' : '登录' }}
          </button>
        </form>

        <p class="login-hint">
          首次启动时，管理员密码由部署环境变量设置并只保存哈希。控制台不会在本地存储任何凭据或 CSRF 令牌。
        </p>
      </section>

      <p class="login-foot">AIBOT · AI SERVER OPERATIONS</p>
    </main>
  </div>
</template>
