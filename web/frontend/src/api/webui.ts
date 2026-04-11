// API client for WebUI Channel configuration.

interface WebUITokenResponse {
  token: string
  ws_url: string
  enabled: boolean
}

interface WebUISetupResponse {
  token: string
  ws_url: string
  enabled: boolean
  changed: boolean
}

const BASE_URL = ""

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const res = await fetch(`${BASE_URL}${path}`, options)
  if (!res.ok) {
    throw new Error(`API error: ${res.status} ${res.statusText}`)
  }
  return res.json() as Promise<T>
}

export async function getWebUIToken(): Promise<WebUITokenResponse> {
  return request<WebUITokenResponse>("/api/webui/token")
}

export async function regenWebUIToken(): Promise<WebUITokenResponse> {
  return request<WebUITokenResponse>("/api/webui/token", { method: "POST" })
}

export async function setupWebUI(): Promise<WebUISetupResponse> {
  return request<WebUISetupResponse>("/api/webui/setup", { method: "POST" })
}

export type { WebUITokenResponse, WebUISetupResponse }
