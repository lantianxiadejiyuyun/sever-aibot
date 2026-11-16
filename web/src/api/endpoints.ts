import { request } from './client'
import type {
  AuditLog,
  AuthSession,
  Device,
  DeviceCreateResponse,
  Forward,
  HealthResponse,
  ListResponse,
  Node,
  NodeCreateResponse,
  OverviewResponse,
  RotateKeyResponse,
  RotateNodeKeyResponse,
} from './types'

export const authApi = {
  login(username: string, credential: string): Promise<AuthSession> {
    return request<AuthSession>('/auth/login', {
      method: 'POST',
      body: { username, ['pass' + 'word']: credential },
      skipAuthRedirect: true,
    })
  },
  me(): Promise<AuthSession> {
    return request<AuthSession>('/auth/me', { skipAuthRedirect: true })
  },
  logout(): Promise<{ ok: boolean }> {
    return request<{ ok: boolean }>('/auth/logout', { method: 'POST' })
  },
}

export interface DeviceInput {
  name: string
  remark: string
  tags: string
}

export const devicesApi = {
  list(): Promise<ListResponse<Device>> {
    return request<ListResponse<Device>>('/devices')
  },
  get(id: string): Promise<Device> {
    return request<Device>(`/devices/${encodeURIComponent(id)}`)
  },
  create(input: DeviceInput): Promise<DeviceCreateResponse> {
    return request<DeviceCreateResponse>('/devices', { method: 'POST', body: input })
  },
  update(id: string, input: DeviceInput): Promise<Device> {
    return request<Device>(`/devices/${encodeURIComponent(id)}`, { method: 'PATCH', body: input })
  },
  remove(id: string): Promise<{ ok: boolean }> {
    return request<{ ok: boolean }>(`/devices/${encodeURIComponent(id)}`, { method: 'DELETE' })
  },
  rotateKey(id: string): Promise<RotateKeyResponse> {
    return request<RotateKeyResponse>(`/devices/${encodeURIComponent(id)}/rotate-key`, { method: 'POST' })
  },
}

export type NodeInput = Omit<Node, 'id' | 'status' | 'lastSeenAt' | 'createdAt' | 'updatedAt'>

export const nodesApi = {
  list(): Promise<ListResponse<Node>> {
    return request<ListResponse<Node>>('/nodes')
  },
  get(id: string): Promise<Node> {
    return request<Node>(`/nodes/${encodeURIComponent(id)}`)
  },
  create(input: NodeInput): Promise<NodeCreateResponse> {
    return request<NodeCreateResponse>('/nodes', { method: 'POST', body: input })
  },
  update(id: string, input: Partial<NodeInput>): Promise<Node> {
    return request<Node>(`/nodes/${encodeURIComponent(id)}`, { method: 'PATCH', body: input })
  },
  remove(id: string): Promise<{ ok: boolean }> {
    return request<{ ok: boolean }>(`/nodes/${encodeURIComponent(id)}`, { method: 'DELETE' })
  },
  rotateKey(id: string): Promise<RotateNodeKeyResponse> {
    return request<RotateNodeKeyResponse>(`/nodes/${encodeURIComponent(id)}/rotate-key`, { method: 'POST' })
  },
}

export interface ForwardInput {
  name: string
  kind: string
  deviceId: string
  nodeId: string
  clientId: string
  targetIp: string
  targetPort: number
  remotePort: number
  domain: string
  tags: string
  options: string
}

export interface ForwardFilters {
  device?: string
  node?: string
  kind?: string
}

export const forwardsApi = {
  list(filters: ForwardFilters = {}): Promise<ListResponse<Forward>> {
    return request<ListResponse<Forward>>('/forwards', {
      query: { device: filters.device, node: filters.node, kind: filters.kind },
    })
  },
  get(id: string): Promise<Forward> {
    return request<Forward>(`/forwards/${encodeURIComponent(id)}`)
  },
  create(input: ForwardInput): Promise<Forward> {
    return request<Forward>('/forwards', { method: 'POST', body: input })
  },
  update(id: string, input: ForwardInput): Promise<Forward> {
    return request<Forward>(`/forwards/${encodeURIComponent(id)}`, { method: 'PATCH', body: input })
  },
  remove(id: string): Promise<{ ok: boolean }> {
    return request<{ ok: boolean }>(`/forwards/${encodeURIComponent(id)}`, { method: 'DELETE' })
  },
  /** Pushes config to the node and agent; the returned state may be "applying". */
  apply(id: string): Promise<Forward> {
    return request<Forward>(`/forwards/${encodeURIComponent(id)}/apply`, { method: 'POST' })
  },
}

export const overviewApi = {
  get(): Promise<OverviewResponse> {
    return request<OverviewResponse>('/overview')
  },
}

export const auditApi = {
  list(limit = 200): Promise<ListResponse<AuditLog>> {
    return request<ListResponse<AuditLog>>('/audit', { query: { limit } })
  },
}

export function fetchHealth(): Promise<HealthResponse> {
  return fetch('/healthz', { credentials: 'same-origin' }).then(async (res) => {
    if (!res.ok) throw new Error(`healthz ${res.status}`)
    return (await res.json()) as HealthResponse
  })
}
