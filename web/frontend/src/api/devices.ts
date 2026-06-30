// API client for the external-device gateway (pairing + network settings).

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
  roles: string[]
  scopes: string[]
  approved_at_ms: number
  last_seen_at_ms: number
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

export interface DeviceSettings {
  listen_lan?: boolean
  external_url?: string
  enabled?: boolean
}
export const saveDeviceSettings = (s: DeviceSettings) =>
  request<DeviceStatus>("/api/devices/settings", jsonPost(s))

// regenerateWordToken mints a fresh typeable passphrase (the long QR token is
// unchanged) and returns the refreshed status.
export const regenerateWordToken = () =>
  request<DeviceStatus>("/api/devices/word-token/regenerate", { method: "POST" })

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
  request<{ devices: PairedDevice[] }>("/api/devices")
export const removeDevice = (id: string) =>
  request<unknown>(`/api/devices/${encodeURIComponent(id)}`, {
    method: "DELETE",
  })
