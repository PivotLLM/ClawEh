// API client for named-provider management.

export interface ProviderInfo {
  index: number
  name: string
  protocol: string
  base_url?: string
  api_key: string
  proxy?: string
  auth_method?: string
  strict_compat?: boolean
  no_parallel_tool_calls?: boolean
  response_format_json?: boolean
  command?: string
  // model_count is how many models reference this provider.
  model_count: number
}

interface ProvidersListResponse {
  providers: ProviderInfo[]
  total: number
}

interface ProviderActionResponse {
  status: string
  index?: number
}

const BASE_URL = ""

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const res = await fetch(`${BASE_URL}${path}`, options)
  if (!res.ok) {
    // Surface the server's error body (e.g. 409 "provider in use") so callers
    // can show a meaningful message.
    let detail = ""
    try {
      detail = (await res.text()).trim()
    } catch {
      // ignore
    }
    throw new Error(detail || `API error: ${res.status} ${res.statusText}`)
  }
  return res.json() as Promise<T>
}

export async function getProviders(): Promise<ProvidersListResponse> {
  return request<ProvidersListResponse>("/api/providers")
}

export async function addProvider(
  provider: Partial<ProviderInfo>,
): Promise<ProviderActionResponse> {
  return request<ProviderActionResponse>("/api/providers", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(provider),
  })
}

export async function updateProvider(
  index: number,
  provider: Partial<ProviderInfo>,
): Promise<ProviderActionResponse> {
  return request<ProviderActionResponse>(`/api/providers/${index}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(provider),
  })
}

export async function deleteProvider(
  index: number,
): Promise<ProviderActionResponse> {
  return request<ProviderActionResponse>(`/api/providers/${index}`, {
    method: "DELETE",
  })
}

export type { ProvidersListResponse, ProviderActionResponse }
