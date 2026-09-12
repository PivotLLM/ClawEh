// Memory API — browsing and curation of cognitive memory.
//
// Curation is the point of the write half. The assistant picks a memory's type
// when it writes it and gets it wrong often enough — a trip log filed as a
// fact, its own bookkeeping filed as a rule — that correcting it by hand has to
// be practical, including over hundreds of rows at once.

/** The five memory types. Only `event` changes behaviour: an event is never
 *  loaded into the prompt and is reached by search. */
export const MEMORY_TYPES = [
  "fact",
  "preference",
  "rule",
  "operational",
  "event",
] as const

export type MemoryType = (typeof MEMORY_TYPES)[number]

/** One-line explanation per type, shown where the operator picks one. */
export const MEMORY_TYPE_HINTS: Record<MemoryType, string> = {
  fact: "Something true, and still true next month.",
  preference: "How you like things done.",
  rule: "A hard directive governing the assistant's output or behaviour.",
  operational:
    "The assistant's own housekeeping — where it files things, how it works.",
  event:
    "Something that happened at a point in time. Never loaded into the prompt; reachable by search.",
}

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
  /** "active" or "retired". */
  status: string
  confidence: number
  /** Where the memory came from: "chat", "consolidation" or "user".
   *  "user" means the operator wrote it by hand — the one piece of provenance
   *  that is verifiable rather than self-reported. */
  origin: string
  file_ref: string
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
  last_used?: string
  memories: MemoryMemory[]
}

export interface MemoryRun {
  trigger: string
  status: string
  ops_applied: number
  started_at: string
  /** Why the run failed. Absent on a successful run. */
  error?: string
  /** Information about a run that SUCCEEDED, e.g. an auto-repair. */
  note?: string
}

export interface MemoryDetail {
  id: string
  agent: string
  active_domains: number
  active_memories: number
  /** Retired memories, counted even when they are not listed, so the page can
   *  offer to show them with a number on it. */
  retired_count: number
  last_run: MemoryRun | null
  domains: MemoryDomain[]
}

export interface BulkResult {
  changed: number
  failed?: Record<string, string>
}

export interface ImportResult {
  domains_created: number
  domains_matched: number
  memories_created: number
  memories_skipped: number
}

export async function getMemoryStores(): Promise<MemoryStoreItem[]> {
  const res = await fetch("/api/memory")
  if (!res.ok) throw new Error(`Failed to fetch memory stores: ${res.status}`)
  const data = await res.json()
  return data.sessions ?? []
}

export async function getMemoryStore(
  id: string,
  includeRetired = false,
): Promise<MemoryDetail> {
  const url = `/api/memory/${encodeURIComponent(id)}${includeRetired ? "?include_retired=1" : ""}`
  const res = await fetch(url)
  if (!res.ok)
    throw new Error(`Failed to fetch memory store ${id}: ${res.status}`)
  return res.json()
}

/** Reads the response body for an error message, falling back to the status.
 *  The API answers with plain text, which is more use than "400". */
async function failure(res: Response, what: string): Promise<Error> {
  const body = (await res.text()).trim()
  return new Error(body !== "" ? body : `${what}: ${res.status}`)
}

export async function patchMemoryItem(
  storeId: string,
  memoryId: string,
  patch: { type?: string; status?: string },
): Promise<MemoryMemory> {
  const res = await fetch(
    `/api/memory/${encodeURIComponent(storeId)}/memories/${encodeURIComponent(memoryId)}`,
    {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(patch),
    },
  )
  if (!res.ok) throw await failure(res, `Failed to update memory ${memoryId}`)
  return res.json()
}

export async function createMemoryDomain(
  storeId: string,
  body: { name: string; sticky?: boolean; summary?: string },
): Promise<MemoryDomain> {
  const res = await fetch(
    `/api/memory/${encodeURIComponent(storeId)}/domains`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    },
  )
  if (!res.ok) throw await failure(res, "Failed to create domain")
  return res.json()
}

export async function createMemoryItem(
  storeId: string,
  domainId: string,
  body: { type: string; text: string },
): Promise<MemoryMemory> {
  const res = await fetch(
    `/api/memory/${encodeURIComponent(storeId)}/domains/${encodeURIComponent(domainId)}/memories`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    },
  )
  if (!res.ok) throw await failure(res, "Failed to create memory")
  return res.json()
}

export async function bulkMemoryAction(
  storeId: string,
  body: {
    action: "retype" | "retire" | "restore" | "delete"
    type?: string
    ids: string[]
  },
): Promise<BulkResult> {
  const res = await fetch(`/api/memory/${encodeURIComponent(storeId)}/bulk`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  })
  if (!res.ok) throw await failure(res, "Bulk action failed")
  return res.json()
}

/** The export URL. Used as an href so the browser handles the download, rather
 *  than pulling the whole document into memory to re-offer it as a blob. */
export function memoryExportURL(storeId: string): string {
  return `/api/memory/${encodeURIComponent(storeId)}/export`
}

export async function importMemory(
  storeId: string,
  yaml: string,
  mode: "merge" | "replace",
): Promise<ImportResult> {
  const res = await fetch(
    `/api/memory/${encodeURIComponent(storeId)}/import?mode=${mode}`,
    {
      method: "POST",
      headers: { "Content-Type": "application/yaml" },
      body: yaml,
    },
  )
  if (!res.ok) throw await failure(res, "Import failed")
  return res.json()
}

export async function deleteMemoryDomain(
  storeId: string,
  domainId: string,
): Promise<void> {
  const res = await fetch(
    `/api/memory/${encodeURIComponent(storeId)}/domains/${encodeURIComponent(domainId)}`,
    { method: "DELETE" },
  )
  if (!res.ok)
    throw new Error(`Failed to delete domain ${domainId}: ${res.status}`)
}

export async function deleteMemoryItem(
  storeId: string,
  memoryId: string,
): Promise<void> {
  const res = await fetch(
    `/api/memory/${encodeURIComponent(storeId)}/memories/${encodeURIComponent(memoryId)}`,
    { method: "DELETE" },
  )
  if (!res.ok)
    throw new Error(`Failed to delete memory ${memoryId}: ${res.status}`)
}
