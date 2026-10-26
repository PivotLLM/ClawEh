// API client for the external-device gateway (pairing). The listener settings
// are edited on the Network page through PATCH /api/config.
import { errorMessage } from "./error-message"

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
  // agent_id normalized: the id agents[] lists it under ("" when unassigned).
  agent_ref?: string
  // The assigned agent's display name while it is configured.
  agent_name?: string
  // The service does not run the assigned agent; the device talks to the
  // default agent. agent_state says why.
  agent_missing: boolean
  agent_state?: "not_found" | "disabled" | "set_aside"
  // The name the assistant is told the device's messages come from ("" =
  // user_default, then the display name).
  user_name: string
  // The admin account's username, used when user_name is empty.
  user_default: string
  // The assistants this device may use ([] = all).
  allowed_agents: string[]
  // False when the assigned assistant is outside allowed_agents.
  agent_allowed: boolean
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
    throw new Error(await errorMessage(res))
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
// setDeviceUser sets the name the assistant is told the device's messages come
// from ("" = the admin account's username).
export const setDeviceUser = (id: string, userName: string) =>
  request<unknown>(
    `/api/devices/${encodeURIComponent(id)}/user`,
    jsonPost({ user_name: userName }),
  )
// setDeviceAgents limits the device to these assistants ([] = all).
export const setDeviceAgents = (id: string, allowedAgents: string[]) =>
  request<unknown>(
    `/api/devices/${encodeURIComponent(id)}/agents`,
    jsonPost({ allowed_agents: allowedAgents }),
  )
export const removeDevice = (id: string) =>
  request<unknown>(`/api/devices/${encodeURIComponent(id)}`, {
    method: "DELETE",
  })
