import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import type { VoiceSTTResponse } from "@/api/voice"
import { SidebarProvider } from "@/components/ui/sidebar"

import { VoicePage } from "./voice-page"

const configured: VoiceSTTResponse = {
  stt: [
    {
      provider: "groq",
      enabled: true,
      api_key: "gsk_****abcd",
      base_url: "",
      model: "",
    },
  ],
  presets: [
    {
      provider: "groq",
      base_url: "https://api.groq.com/openai/v1",
      model: "whisper-large-v3",
    },
  ],
}

function renderPage(qc: QueryClient) {
  // SidebarProvider because PageHeader reads the sidebar context.
  return render(
    <QueryClientProvider client={qc}>
      <SidebarProvider>
        <VoicePage />
      </SidebarProvider>
    </QueryClientProvider>,
  )
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe("VoicePage", () => {
  it("lists the configured backend on first load", async () => {
    const fetchMock = vi.fn(async () => ({
      ok: true,
      json: async () => configured,
    }))
    vi.stubGlobal("fetch", fetchMock)
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    renderPage(qc)

    const provider = await screen.findByLabelText("Provider")
    expect((provider as HTMLSelectElement).value).toBe("groq")
    expect(screen.getByText("Enabled")).toBeTruthy()
    expect(screen.queryByText(/No transcription backends configured/)).toBe(
      null,
    )
    // Rendering must not write anything back.
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  // The page is reached a second time with the query already cached (the
  // operator visited it, left, and came back). The cached rows must be shown
  // straight away, not an empty list that the next edit would then save over
  // the real configuration.
  it("lists the configured backend when the query is already cached", async () => {
    const fetchMock = vi.fn(async () => ({
      ok: true,
      json: async () => configured,
    }))
    vi.stubGlobal("fetch", fetchMock)
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false, staleTime: Infinity } },
    })
    qc.setQueryData(["voice-stt"], configured)
    renderPage(qc)

    const provider = await screen.findByLabelText("Provider")
    expect((provider as HTMLSelectElement).value).toBe("groq")
    expect(screen.queryByText(/No transcription backends configured/)).toBe(
      null,
    )
    await waitFor(() => expect(fetchMock).not.toHaveBeenCalled())
  })

  it("shows the empty state only when nothing is configured", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => ({
        ok: true,
        // The Go handler encodes an empty list as null when nothing is set.
        json: async () => ({ stt: null, presets: null }),
      })),
    )
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    renderPage(qc)

    await screen.findByText(/No transcription backends configured/)
    expect(screen.queryByLabelText("Provider")).toBe(null)
  })
})
