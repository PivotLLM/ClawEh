// API client for model list management.

export interface ModelInfo {
  index: number
  model_name: string
  model: string
  api_base?: string
  api_key: string
  proxy?: string
  auth_method?: string
  // Advanced fields
  connect_mode?: string
  workspace?: string
  rpm?: number
  max_tokens?: number
  max_tokens_field?: string
  request_timeout?: number
  thinking_level?: string
  no_tools?: boolean
  // Shape 3 per-LLM custom fields. extra_body accepts an explicit `null` on
  // save to clear a previously-stored value; the backend handler merge-loads
  // the request JSON into the existing entry, so an absent field would
  // otherwise preserve the old value rather than removing it.
  reasoning_effort?: string
  extra_body?: Record<string, unknown> | null
  // drop_params accepts an explicit empty array on save to clear a previously
  // stored value, mirroring extra_body's null-to-clear semantics.
  drop_params?: string[] | null
  enabled: boolean
  // Meta
  configured: boolean
  is_default: boolean
}

interface ModelsListResponse {
  models: ModelInfo[]
  total: number
  default_model: string
}

interface ModelActionResponse {
  status: string
  index?: number
  default_model?: string
}

const BASE_URL = ""

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const res = await fetch(`${BASE_URL}${path}`, options)
  if (!res.ok) {
    throw new Error(`API error: ${res.status} ${res.statusText}`)
  }
  return res.json() as Promise<T>
}

export async function getModels(): Promise<ModelsListResponse> {
  return request<ModelsListResponse>("/api/models")
}

export async function addModel(
  model: Partial<ModelInfo>,
): Promise<ModelActionResponse> {
  return request<ModelActionResponse>("/api/models", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(model),
  })
}

export async function updateModel(
  index: number,
  model: Partial<ModelInfo>,
): Promise<ModelActionResponse> {
  return request<ModelActionResponse>(`/api/models/${index}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(model),
  })
}

export async function deleteModel(index: number): Promise<ModelActionResponse> {
  return request<ModelActionResponse>(`/api/models/${index}`, {
    method: "DELETE",
  })
}

export async function setDefaultModel(
  modelName: string,
): Promise<ModelActionResponse> {
  return request<ModelActionResponse>("/api/models/default", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ model_name: modelName }),
  })
}

export type { ModelsListResponse, ModelActionResponse }
