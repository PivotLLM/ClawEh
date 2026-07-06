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
