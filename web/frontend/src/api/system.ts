async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const res = await fetch(path, options)
  if (!res.ok) {
    let message = `API error: ${res.status} ${res.statusText}`
    try {
      const body = (await res.json()) as {
        error?: string
        errors?: string[]
      }
      if (Array.isArray(body.errors) && body.errors.length > 0) {
        message = body.errors.join("; ")
      } else if (typeof body.error === "string" && body.error.trim() !== "") {
        message = body.error
      }
    } catch {
      // Keep fallback error message when response body is not JSON.
    }
    throw new Error(message)
  }
  return res.json() as Promise<T>
}

export interface CLIInfo {
  protocol: string
  label: string
  binary: string
  installed: boolean
  path?: string
  version?: string
}

// listCLIs reports which known CLI agents (claude/codex/gemini) are installed on
// the host, so the setup wizard can show what's available without the user
// configuring a CLI whose binary isn't on PATH.
export async function listCLIs(): Promise<CLIInfo[]> {
  return request<CLIInfo[]>("/api/system/clis")
}

export interface SetupStatus {
  // pristine: an auto-seeded config the user has never saved.
  pristine: boolean
  // has_usable_model: at least one enabled model has working credentials.
  has_usable_model: boolean
  // needs_setup: pristine with no usable model — drives the first-run redirect.
  needs_setup: boolean
}

// getSetupStatus reports whether this is a fresh install that should be sent to
// the setup wizard.
export async function getSetupStatus(): Promise<SetupStatus> {
  return request<SetupStatus>("/api/system/setup-status")
}

// reloadGateway forces an immediate config reload, bypassing the mtime-debounce.
// It resolves only once the reload has completed, so callers can wait before
// directing the user back into the app.
export async function reloadGateway(): Promise<void> {
  await request<unknown>("/api/gateway/reload", { method: "POST" })
}

// getVersion returns the running ClawEh build version (for the sidebar footer).
export async function getVersion(): Promise<string> {
  const res = await request<{ version: string }>("/api/system/version")
  return res.version
}

/** Runtime state of the running ClawEh process, for the Status page. */
export interface SystemStatus {
  version: string
  build?: string
  uptime_seconds: number
  uptime: string
  pid: number
  /** Resident set size: the physical RAM the process holds. */
  memory_bytes: number
  /** What Go itself has in use, i.e. how much of the resident figure is the
   *  program rather than its mapped binary. */
  heap_bytes: number
  goroutines: number
  agents: number
  models: number
  providers: number
  channels: number
  cli_providers: boolean
  mcp_host: boolean
}

export async function getSystemStatus(): Promise<SystemStatus> {
  const res = await fetch("/api/system/status")
  if (!res.ok) throw new Error(`Failed to fetch status: ${res.status}`)
  return res.json()
}
