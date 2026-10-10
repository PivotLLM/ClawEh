// API client for speech-to-text (voice transcription) backend configuration.
import { errorMessage } from "./error-message"

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
    throw new Error(await errorMessage(res))
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
