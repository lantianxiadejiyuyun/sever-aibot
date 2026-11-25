import { request } from './client'
import type { Forward } from './types'

export interface TrafficDay { day: string; uploadBytes: number; downloadBytes: number }
export interface ForwardRuntime {
  forwardId: string; revision: number; state: string; lastError: string
  nodeOnline: boolean; agentOnline: boolean; p2pStatus: string; trafficCollection: string
}
export const forwardRuntimeApi = {
  start: (id: string) => request<Forward>(`/forwards/${encodeURIComponent(id)}/start`, { method: 'POST' }),
  stop: (id: string) => request<Forward>(`/forwards/${encodeURIComponent(id)}/stop`, { method: 'POST' }),
  runtime: (id: string) => request<ForwardRuntime>(`/forwards/${encodeURIComponent(id)}/runtime`),
  traffic: (id: string, days = 30) => request<{ items: TrafficDay[]; timezone: string }>(`/forwards/${encodeURIComponent(id)}/traffic`, { query: { days } }),
}
