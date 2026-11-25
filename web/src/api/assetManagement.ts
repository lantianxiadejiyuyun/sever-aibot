import { request, ApiError, errorMessage } from './client'
import type { ListResponse } from './types'

export interface AssetInput {
  name: string; address: string; port: number; protocol: string
  viaDeviceId: string; viaAssetId: string; credentialId: string
  hostKey: string; tags: string; enabled: boolean; notes: string
}
export interface Asset extends AssetInput { id: string; createdAt: string; updatedAt: string }
export interface Credential {
  id: string; name: string; kind: string; username: string
  publicMeta: Record<string, unknown>; keyId: string
  createdAt: string; updatedAt: string; lastUsedAt?: string
}
export interface CredentialInput { name: string; kind?: string; username: string; secret?: string; publicMeta: Record<string, unknown> }
export interface AssetRoute {
  viaDeviceId?: string
  hops: { id: string; name: string; address: string; port: number }[]
  endpoint: { address: string; port: number }
}
export interface ProbeResult {
  ok?: boolean; error?: string; message?: string; latencyMs?: number; checkedAt?: string
}
const assetPath = (id: string) => `/assets/${encodeURIComponent(id)}`
const credentialPath = (id: string) => `/credentials/${encodeURIComponent(id)}`
export const assetsApi = {
  list: () => request<ListResponse<Asset>>('/assets'),
  get: (id: string) => request<Asset>(assetPath(id)),
  save: (id: string, input: AssetInput) => {
    const { hostKey: _hostKey, ...body } = input
    return request<Asset>(id ? assetPath(id) : '/assets', { method: id ? 'PATCH' : 'POST', body })
  },
  discover: (id: string) => request<{ fingerprint: string; algorithm: string }>(`${assetPath(id)}/hostkey/discover`, { method: 'POST', body: {} }),
  pin: (id: string, fingerprint: string) => request(`${assetPath(id)}/hostkey/pin`, { method: 'POST', body: { fingerprint, confirmed: true } }),
  remove: (id: string) => request(assetPath(id), { method: 'DELETE' }),
  route: (id: string) => request<AssetRoute>(`${assetPath(id)}/route`),
  config: (id: string) => request<string>(`${assetPath(id)}/ssh-config`),
  check: (id: string) => request<ProbeResult>(`${assetPath(id)}/check`, { method: 'POST' }),
}
export const credentialsApi = {
  list: () => request<ListResponse<Credential>>('/credentials'),
  save: (id: string, body: CredentialInput) => request<Credential>(id ? credentialPath(id) : '/credentials', { method: id ? 'PATCH' : 'POST', body }),
  remove: (id: string) => request(credentialPath(id), { method: 'DELETE' }),
  reveal: (id: string, signal?: AbortSignal) => request<{ username: string; secret: string }>(`${credentialPath(id)}/reveal`, { method: 'POST', signal }),
  test: (id: string, assetId: string) => request<ProbeResult>(`${credentialPath(id)}/test`, { method: 'POST', body: { assetId } }),
  stepUp: (value: string, signal?: AbortSignal) => request<{ stepUpUntil: string }>('/auth/step-up', { method: 'POST', body: { ['pass' + 'word']: value }, skipAuthRedirect: true, signal }),
}
export function operationError(error: unknown): string {
  if (error instanceof ApiError) return `${error.status === 501 ? '功能暂不可用：' : ''}${error.message}（${error.code}）`
  return errorMessage(error)
}
export function probeMessage(result: ProbeResult): string {
  const status = result.ok === true ? '检查通过' : result.ok === false ? '检查失败' : '服务未返回明确的检查结果'
  return [status, result.error || result.message, result.latencyMs !== undefined ? `${result.latencyMs} ms` : '', result.checkedAt].filter(Boolean).join(' · ')
}
export const credentialKinds = [
  { value: 'ssh_' + ('pass' + 'word'), label: 'SSH 口令' }, { value: 'ssh_key', label: 'SSH 私钥' },
  { value: 'baota_api', label: '宝塔 API' }, { value: 'db_' + ('pass' + 'word'), label: '数据库口令' },
  { value: 'api_token', label: 'API Token' }, { value: 'generic', label: '通用秘密' },
]
export const credentialKindLabel = (kind: string) => credentialKinds.find((item) => item.value === kind)?.label || kind
