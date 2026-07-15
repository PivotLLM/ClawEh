// API client for channels navigation and channel-specific config flows.

export type ChannelConfig = Record<string, unknown>
export type AppConfig = Record<string, unknown>

export interface SupportedChannel {
  name: string
  display_name?: string
  config_key: string
  variant?: string
}

interface ChannelsCatalogResponse {
  channels: SupportedChannel[]
}

interface ConfigActionResponse {
  status: string
  errors?: string[]
}

const BASE_URL = ""

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const res = await fetch(`${BASE_URL}${path}`, options)
  if (!res.ok) {
    let message = `API error: ${res.status} ${res.statusText}`
    try {
      const body = (await res.json()) as {
        error?: string
        errors?: string[]
        status?: string
      }
      if (Array.isArray(body.errors) && body.errors.length > 0) {
        message = body.errors.join("; ")
      } else if (typeof body.error === "string" && body.error.trim() !== "") {
        message = body.error
      }
    } catch {
      // Keep default fallback message if response body is not JSON.
    }
    throw new Error(message)
  }
  return res.json() as Promise<T>
}

export async function getChannelsCatalog(): Promise<ChannelsCatalogResponse> {
  return request<ChannelsCatalogResponse>("/api/channels/catalog")
}

export async function getAppConfig(): Promise<AppConfig> {
  return request<AppConfig>("/api/config")
}

export async function patchAppConfig(
  patch: Record<string, unknown>,
): Promise<ConfigActionResponse> {
  return request<ConfigActionResponse>("/api/config", {
    method: "PATCH",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(patch),
  })
}

export interface AgentToolEntry {
  name: string
  description: string
  // suite, when set, marks an all-or-nothing tool suite (cogmem, maestro)
  // managed by the agent's per-suite toggle rather than this per-tool list.
  suite?: string
}

export interface AgentMCPServer {
  name: string
  pattern: string
}

export interface AgentToolCatalogResponse {
  tools: AgentToolEntry[]
  mcp_servers?: AgentMCPServer[]
  default_tools: string[]
}

export async function getAgentTools(): Promise<AgentToolCatalogResponse> {
  return request<AgentToolCatalogResponse>("/api/agents/tools")
}

// SecMsgLinkStatus mirrors the backend pairing reply. status is
// "pending" | "complete" | "error"; qr_png is a PNG data-URL for the pairing URI.
export interface SecMsgLinkStatus {
  status: string
  uri?: string
  qr_png?: string
  phone?: string
  error?: string
}

// requestSecMsgLink starts device linking for a configured secmsg channel and
// returns the pairing QR.
export async function requestSecMsgLink(
  name: string,
): Promise<SecMsgLinkStatus> {
  return request<SecMsgLinkStatus>(
    `/api/channels/secmsg/${encodeURIComponent(name)}/link`,
    { method: "POST" },
  )
}

// getSecMsgLinkStatus polls current pairing status for a configured channel.
export async function getSecMsgLinkStatus(
  name: string,
): Promise<SecMsgLinkStatus> {
  return request<SecMsgLinkStatus>(
    `/api/channels/secmsg/${encodeURIComponent(name)}/link`,
  )
}

// MCPServerStatus mirrors pkg/mcp.ServerStatus. state is
// "connected" | "reconnecting" | "cooldown"; cooldown_until is RFC3339 (only for
// the cooldown state). Servers absent from the response are disconnected.
export interface MCPServerStatus {
  name: string
  state: string
  transport?: string
  tool_count: number
  cooldown_until?: string
}

interface MCPStatusResponse {
  servers: MCPServerStatus[]
}

// getMCPStatus reports the live connection state of outbound MCP servers.
export async function getMCPStatus(): Promise<MCPStatusResponse> {
  return request<MCPStatusResponse>("/api/mcp/status")
}

export type { ChannelsCatalogResponse, ConfigActionResponse, MCPStatusResponse }
