// API client for gateway log endpoints.

interface GatewayLogsResponse {
  logs?: string[]
  count?: number
  error?: string
}

const BASE_URL = ""

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const res = await fetch(`${BASE_URL}${path}`, options)
  if (!res.ok) {
    throw new Error(`API error: ${res.status} ${res.statusText}`)
  }
  return res.json() as Promise<T>
}

// getGatewayLogs fetches the last `lines` entries of the unified claw.log.
export async function getGatewayLogs(
  lines: number,
): Promise<GatewayLogsResponse> {
  return request<GatewayLogsResponse>(
    `/api/gateway/logs?lines=${encodeURIComponent(lines)}`,
  )
}

export type { GatewayLogsResponse }
