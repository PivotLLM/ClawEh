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
  /** Whether any model reaching this CLI is enabled — what the switch shows. */
  enabled: boolean
  /** Whether a provider for this CLI exists at all. */
  configured: boolean
  /** Config index of that provider, for the edit sheet. -1 when there is none. */
  provider_index: number
  /** Models running through this CLI, and how many of them are enabled. */
  models: number
  models_enabled: number
}

// listCLIs reports every supported CLI agent (claude/codex/agy/cursor): whether
// its binary is installed, and how it is currently configured. Rows come back
// for CLIs that are not installed too — the Providers page greys those out, and
// the setup wizard offers only the installed ones.
export async function listCLIs(): Promise<CLIInfo[]> {
  return request<CLIInfo[]>("/api/system/clis")
}

// setCLIEnabled turns a CLI agent on or off. On creates the provider and model
// if they are missing; off disables every model reaching that CLI. Nothing is
// deleted either way, so the switch is reversible.
export async function setCLIEnabled(
  protocol: string,
  enabled: boolean,
): Promise<void> {
  await request(`/api/system/clis/${encodeURIComponent(protocol)}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ enabled }),
  })
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
  go_version?: string
  os: string
  arch: string
  // os_name is a human OS name ("Ubuntu 24.04.4 LTS"), empty when the host
  // does not say — fall back to os.
  os_name?: string
  goroutines: number
  agents: number
  /** Enabled models, and providers that are actually usable — not totals. */
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
