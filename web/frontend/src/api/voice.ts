// API client for speech-to-text (voice transcription) backend configuration.

export interface STTProvider {
  provider: string
  enabled: boolean
  api_key?: string
  base_url?: string
  model?: string
}

export interface STTPreset {
  provider: string
  base_url: string
  model: string
}

export interface VoiceSTTResponse {
  stt: STTProvider[]
  presets: STTPreset[]
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

// getVoiceSTT normalises the two lists at the boundary: Go encodes an empty
// slice as null, and every consumer expects an array.
export const getVoiceSTT = async (): Promise<VoiceSTTResponse> => {
  const data = await request<Partial<VoiceSTTResponse>>("/api/voice/stt")
  return { stt: data.stt ?? [], presets: data.presets ?? [] }
}

export const saveVoiceSTT = (stt: STTProvider[]) =>
  request<{ status: string }>("/api/voice/stt", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ stt }),
  })
