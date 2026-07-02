// API client for per-agent named message-API tokens (long-lived webhook tokens).

export interface MessageToken {
  id: string
  name: string
  token: string
  created_at_ms: number
}

export interface MessageTokenList {
  tokens: MessageToken[]
  // Absolute base URL ending in /api/message/ — a full endpoint is base + token.
  endpoint_base: string
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

export const listMessageTokens = (agentId: string) =>
  request<MessageTokenList>(
    `/api/agents/${encodeURIComponent(agentId)}/message-tokens`,
  )

export const createMessageToken = (agentId: string, name: string) =>
  request<MessageToken>(
    `/api/agents/${encodeURIComponent(agentId)}/message-tokens`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name }),
    },
  )

export const deleteMessageToken = (agentId: string, tokenId: string) =>
  request<unknown>(
    `/api/agents/${encodeURIComponent(agentId)}/message-tokens/${encodeURIComponent(tokenId)}`,
    { method: "DELETE" },
  )
