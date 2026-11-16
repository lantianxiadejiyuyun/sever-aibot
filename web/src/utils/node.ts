import type { Node } from '@/api/types'

export const forwardKindOptions = [
  { value: 'port', label: '端口' },
  { value: 'host', label: '域名' },
  { value: 'tunnel', label: '隧道' },
  { value: 'p2p', label: 'P2P' },
  { value: 'proxy', label: '代理' },
] as const

export function kindLabel(kind: string): string {
  return forwardKindOptions.find((item) => item.value === kind)?.label || kind || '—'
}

export function nodeSupportsKind(node: Node | null | undefined, kind: string): boolean {
  if (!node) return false
  switch (kind) {
    case 'port':
      return node.features.forward
    case 'host':
      return node.features.web
    case 'tunnel':
      return node.features.tunnel
    case 'p2p':
      return node.features.p2p
    case 'proxy':
      return node.features.proxy
    default:
      return false
  }
}

export function capabilityItems(node: Node): Array<{ label: string; on: boolean }> {
  return [
    { label: 'Web', on: node.features.web },
    { label: '转发', on: node.features.forward },
    { label: '隧道', on: node.features.tunnel },
    { label: 'P2P', on: node.features.p2p },
    { label: '代理', on: node.features.proxy },
  ]
}

/** Accepts "20000-30000,40000,41000-41010" and checks each range. */
export function validPortSpec(spec: string): boolean {
  if (!spec.trim()) return true
  for (const raw of spec.split(',')) {
    const part = raw.trim()
    if (!part) return false
    const [loRaw, hiRaw, extra] = part.split('-')
    if (extra !== undefined) return false
    const lo = Number(loRaw)
    if (!Number.isInteger(lo) || lo < 1 || lo > 65535) return false
    if (hiRaw !== undefined) {
      const hi = Number(hiRaw)
      if (!Number.isInteger(hi) || hi < 1 || hi > 65535 || hi < lo) return false
    }
  }
  return true
}

export function optionLabel(node: Node): string {
  const caps = capabilityItems(node)
    .filter((item) => item.on)
    .map((item) => item.label)
    .join('/')
  return `${node.name}${caps ? ` · ${caps}` : ' · 无能力'}${
    node.status === 'online' ? ' · 在线' : ' · 离线'
  }`
}
