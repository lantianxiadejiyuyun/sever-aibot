/** Only allow same-origin absolute paths as post-login redirect targets. */
export function safeRedirect(raw: unknown): string | null {
  if (typeof raw !== 'string') return null
  if (!raw.startsWith('/') || raw.startsWith('//')) return null
  if (raw === '/login' || raw.startsWith('/login?') || raw.startsWith('/login/')) return null
  return raw
}
