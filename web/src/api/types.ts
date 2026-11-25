export interface User {
  id?: string
  username: string
}

export interface AuthSession {
  user: User
  csrfToken: string
  expiresAt: string
}

export interface ListResponse<T> {
  items: T[]
  total: number
}

export interface Device {
  keyRetrievable?: boolean
  id: string
  name: string
  remark: string
  tags: string
  status: string
  version: string
  os: string
  arch: string
  lastSeenAt?: string | null
  createdAt: string
  updatedAt: string
}

export interface NodeFeatures {
  web: boolean
  forward: boolean
  tunnel: boolean
  p2p: boolean
  proxy: boolean
}

export interface Node {
  rendezvousAddress: string
  id: string
  name: string
  remark: string
  tags: string
  addr: string
  protocol: string
  httpPort: number
  replaceAddress: string
  forwardPorts: string
  domain: string
  urlTemplate: string
  denyDomainPrefix: string
  allowWildcard: boolean
  features: NodeFeatures
  maxPoolCount: number
  p2pDisableForward: boolean
  rules: string
  limitTotalGb: number
  limitKind: string
  limitResetDay: number
  limiterMbps: number
  concurrency: number
  connections: number
  status: string
  lastSeenAt?: string | null
  createdAt: string
  updatedAt: string
}

export type ForwardKind = 'port' | 'host' | 'tunnel' | 'p2p' | 'proxy'

export interface Forward {
  id: string
  name: string
  kind: ForwardKind | string
  deviceId: string
  nodeId: string
  clientId: string
  targetIp: string
  targetPort: number
  remotePort: number
  domain: string
  state: 'saved' | 'applying' | 'applied' | 'observed' | 'error' | string
  revision: number
  lastError: string
  tags: string
  options: string
  trafficBytes: number
  createdAt: string
  updatedAt: string
}

export interface OverviewCounts {
  devices: number
  devicesOnline: number
  nodes: number
  nodesOnline: number
  forwards: number
  forwardsActive: number
}

export interface AuditLog {
  id: number
  actor: string
  action: string
  target: string
  detail: string
  ip: string
  userAgent: string
  result: string
  createdAt: string
}

export interface OverviewResponse {
  counts: OverviewCounts
  recentAudit: AuditLog[]
}

export interface DeviceCreateResponse {
  device: Device
  deviceKey: string
}

export interface NodeCreateResponse {
  node: Node
  nodeKey: string
}

export interface RotateKeyResponse {
  deviceId: string
  deviceKey: string
}

export interface RotateNodeKeyResponse {
  nodeId: string
  nodeKey: string
}

export interface HealthResponse {
  status: string
  db: boolean
  agentsOnline: number
  nodesOnline?: number
  time: string
  version: string
  sessionCookie: boolean
}
