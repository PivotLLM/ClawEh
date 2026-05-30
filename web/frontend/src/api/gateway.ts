// API client for gateway log endpoints.

interface GatewayLogsResponse {
  logs?: string[]
  log_total?: number
  log_run_id?: number
}

interface GatewayActionResponse {
  status: string
  pid?: number
  log_total?: number
  log_run_id?: number
}

const BASE_URL = ""

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const res = await fetch(`${BASE_URL}${path}`, options)
  if (!res.ok) {
    throw new Error(`API error: ${res.status} ${res.statusText}`)
  }
  return res.json() as Promise<T>
}

export async function getGatewayLogs(options?: {
  log_offset?: number
  log_run_id?: number
}): Promise<GatewayLogsResponse> {
  const params = new URLSearchParams()
  if (options?.log_offset !== undefined) {
    params.set("log_offset", options.log_offset.toString())
  }
  if (options?.log_run_id !== undefined) {
    params.set("log_run_id", options.log_run_id.toString())
  }
  const queryString = params.toString() ? `?${params.toString()}` : ""
  return request<GatewayLogsResponse>(`/api/gateway/logs${queryString}`)
}

export async function clearGatewayLogs(): Promise<GatewayActionResponse> {
  return request<GatewayActionResponse>("/api/gateway/logs/clear", {
    method: "POST",
  })
}

export type { GatewayLogsResponse, GatewayActionResponse }
