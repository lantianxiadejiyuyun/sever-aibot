import { reactive } from 'vue'
import { authApi } from '@/api/endpoints'
import { clearCsrfToken, setCsrfToken } from '@/api/client'
import type { AuthSession, User } from '@/api/types'

interface SessionState {
  user: User | null
  expiresAt: string | null
  ready: boolean
  booting: boolean
}

export const session = reactive<SessionState>({
  user: null,
  expiresAt: null,
  ready: false,
  booting: false,
})

let bootPromise: Promise<void> | null = null

function accept(snapshot: AuthSession): void {
  session.user = snapshot.user
  session.expiresAt = snapshot.expiresAt
  setCsrfToken(snapshot.csrfToken)
}

export function clearSession(): void {
  session.user = null
  session.expiresAt = null
  clearCsrfToken()
}

/**
 * Resolve the session once per page load. The router awaits this before the
 * first guarded navigation. `/auth/me` keeps the CSRF token in memory only.
 */
export function bootstrapSession(): Promise<void> {
  if (session.ready) return Promise.resolve()
  if (bootPromise) return bootPromise

  bootPromise = (async () => {
    session.booting = true
    try {
      const snapshot = await authApi.me()
      accept(snapshot)
    } catch {
      clearSession()
    } finally {
      session.ready = true
      session.booting = false
      bootPromise = null
    }
  })()

  return bootPromise
}

export async function login(username: string, credential: string): Promise<User> {
  const snapshot = await authApi.login(username, credential)
  accept(snapshot)
  session.ready = true
  return snapshot.user
}

export async function logout(): Promise<void> {
  try {
    await authApi.logout()
  } finally {
    clearSession()
    session.ready = true
  }
}
