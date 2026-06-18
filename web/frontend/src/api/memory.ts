// Memory API — read-only cognitive-memory browser

export interface MemoryStoreItem {
  id: string
  agent: string
  updated: string
  size_bytes: number
}

export interface MemoryMemory {
  id: string
  type: string
  text: string
  status: string
  confidence: number
  priority: number
  source: string
  origin: string
  created: string
  updated: string
}

export interface MemoryDomain {
  id: string
  sticky: boolean
  name: string
  status: string
  summary: string
  triggers?: string
  keyword_triggers?: string
  memories: MemoryMemory[]
}

export interface MemoryRun {
  trigger: string
  status: string
  ops_applied: number
  started_at: string
  error?: string
}

export interface MemoryDetail {
  id: string
  agent: string
  active_domains: number
  active_memories: number
  pending: number
  last_run: MemoryRun | null
  domains: MemoryDomain[]
  pending_list: MemoryMemory[]
}

export async function getMemoryStores(): Promise<MemoryStoreItem[]> {
  const res = await fetch("/api/memory")
  if (!res.ok) throw new Error(`Failed to fetch memory stores: ${res.status}`)
  const data = await res.json()
  return data.sessions ?? []
}

export async function getMemoryStore(id: string): Promise<MemoryDetail> {
  const res = await fetch(`/api/memory/${encodeURIComponent(id)}`)
  if (!res.ok) throw new Error(`Failed to fetch memory store ${id}: ${res.status}`)
  return res.json()
}
