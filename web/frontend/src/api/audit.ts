// Audit log API — the append-only record of tool calls, config writes and
// authentication events. Read-only from the WebUI: rows are never edited.

export type AuditKind = "tool_call" | "config_write" | "auth"

export const AUDIT_KINDS: AuditKind[] = ["tool_call", "config_write", "auth"]

export interface AuditEvent {
  id: number
  /** RFC 3339 timestamp. */
  ts: string
  kind: AuditKind | string
  /** Authenticated operator, for config writes and auth events. */
  actor?: string
  session?: string
  channel?: string
  /** Chat sender for a tool call; client IP for an HTTP-originated event. */
  sender?: string
  agent?: string
  tool?: string
  /** Changed config keys, or the auth action (login/logout/lockout). */
  summary?: string
  /** JSON: redacted tool arguments, or the changed key list. */
  details?: string
  outcome?: string
  duration_ms: number
  /** Correlates the row with the turn's log lines. */
  turn_id?: string
}

export interface AuditListResponse {
  events: AuditEvent[]
  /** Pass as before_id to fetch the next (older) page; 0 when this page was empty. */
  next_before_id: number
  /** Events the store had to drop because its write queue was full. */
  dropped: number
}

export interface AuditFilter {
  kind?: string
  agent?: string
  session?: string
  since?: string
  until?: string
  limit?: number
  before_id?: number
}

// listAudit fetches one page of audit rows, newest first. cache: "no-store" so
// a refresh always reaches the server rather than replaying a cached page.
export async function listAudit(
  filter: AuditFilter = {},
): Promise<AuditListResponse> {
  const params = new URLSearchParams()
  for (const [k, v] of Object.entries(filter)) {
    if (v !== undefined && v !== null && v !== "" && v !== 0) {
      params.set(k, String(v))
    }
  }
  const qs = params.toString()
  const res = await fetch(`/api/audit${qs ? `?${qs}` : ""}`, {
    cache: "no-store",
  })
  if (!res.ok) {
    let message = `API error: ${res.status} ${res.statusText}`
    try {
      const body = (await res.json()) as { error?: string }
      if (typeof body.error === "string" && body.error.trim() !== "") {
        message = body.error
      }
    } catch {
      // Keep the status-line message when the body is not JSON.
    }
    throw new Error(message)
  }
  return res.json() as Promise<AuditListResponse>
}
