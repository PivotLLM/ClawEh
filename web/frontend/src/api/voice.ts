// API client for speech-to-text (voice transcription) backend configuration.
import { errorMessage } from "./error-message"

// STTProvider is one transcription backend: a reference to a provider on the
// Providers page (which holds the key and endpoint) plus an optional model.
export interface STTProvider {
  provider: string
  enabled: boolean
  model: string
}

// STTProviderOption is a configured provider that can serve transcription.
// default_model is "" when the service has no known default; ready is false
// when the provider has no API key.
export interface STTProviderOption {
  name: string
  default_model: string
  ready: boolean
}

export interface VoiceSTTResponse {
  stt: STTProvider[]
  providers: STTProviderOption[]
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
  // Go leaves a blank model out of the JSON; the page treats it as a string.
  const stt = (data.stt ?? []).map((s) => ({ ...s, model: s.model ?? "" }))
  return { stt, providers: data.providers ?? [] }
}

export const saveVoiceSTT = (stt: STTProvider[]) =>
  request<{ status: string }>("/api/voice/stt", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ stt }),
  })
