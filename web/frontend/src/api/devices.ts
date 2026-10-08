// API client for the external-device gateway (pairing). The listener settings
// are edited on the Network page through PATCH /api/config.

export interface DeviceStatus {
  payload: string
  ips: string[]
  port: number
  protocol: string
  enabled: boolean
  hasToken: boolean
  word_token: string
  listen_host: string
  listen_port: number
  listen_lan: boolean
  external_url: string
  connect_url: string
  warnings: string[]
  qr_png?: string
  qr_ascii?: string
}

export interface PendingDevice {
  request_id: string
  device_id: string
  display_name: string
  platform: string
  client_id: string
  role: string
  remote_ip: string
  created_at_ms: number
}

export interface PairedDevice {
  device_id: string
  display_name: string
  platform: string
  client_mode: string
  roles: string[]
  scopes: string[]
  agent_id: string
  // agent_id names an agent that no longer exists or is disabled; the device
  // talks to the default agent.
  agent_missing: boolean
  approved_at_ms: number
  last_seen_at_ms: number
}

export interface AgentOption {
  id: string
  name: string
}

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const res = await fetch(path, options)
  if (!res.ok) {
    let message = `API error: ${res.status} ${res.statusText}`
    try {
      const body = (await res.json()) as { error?: string }
      if (typeof body.error === "string" && body.error.trim() !== "") {
        message = body.error
      }
    } catch {
      // keep fallback
    }
    throw new Error(message)
  }
  return res.json() as Promise<T>
}

const jsonPost = (body: unknown): RequestInit => ({
  method: "POST",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify(body),
})

export const getDeviceStatus = () => request<DeviceStatus>("/api/devices/pair")

// generateDevicePairing provisions a token, enables the channel, and returns the
// rendered QR.
export const generateDevicePairing = () =>
  request<DeviceStatus>("/api/devices/pair", { method: "POST" })

// regenerateWordToken mints a fresh typeable passphrase (the long QR token is
// unchanged) and returns the refreshed status.
export const regenerateWordToken = () =>
  request<DeviceStatus>("/api/devices/word-token/regenerate", {
    method: "POST",
  })

export const listPendingDevices = () =>
  request<{ pending: PendingDevice[] }>("/api/devices/pending")
export const approveDevice = (id: string) =>
  request<unknown>(`/api/devices/pending/${encodeURIComponent(id)}/approve`, {
    method: "POST",
  })
export const rejectDevice = (id: string) =>
  request<unknown>(`/api/devices/pending/${encodeURIComponent(id)}/reject`, {
    method: "POST",
  })

export const listPairedDevices = () =>
  request<{ devices: PairedDevice[]; agents: AgentOption[] }>("/api/devices")
// assignDeviceAgent sets the agent a device routes to ("" = default agent).
export const assignDeviceAgent = (id: string, agentId: string) =>
  request<unknown>(
    `/api/devices/${encodeURIComponent(id)}/agent`,
    jsonPost({ agent_id: agentId }),
  )
export const removeDevice = (id: string) =>
  request<unknown>(`/api/devices/${encodeURIComponent(id)}`, {
    method: "DELETE",
  })
