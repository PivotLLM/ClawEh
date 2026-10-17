import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, render, screen } from "@testing-library/react"
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

    // Straight away: on the first render, not after a fetch.
    const provider = screen.getByLabelText("Provider")
    expect((provider as HTMLSelectElement).value).toBe("groq")
    expect(screen.queryByText(/No transcription backends configured/)).toBe(
      null,
    )
    // Let the mount effects and any fetch they start run, then check that the
    // fresh cache was not fetched again.
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0))
    })
    expect(fetchMock).not.toHaveBeenCalled()
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

  it("labels the primary and fallback backends and marks a custom one with no endpoint", async () => {
    const data: VoiceSTTResponse = {
      stt: [
        {
          provider: "custom",
          enabled: true,
          api_key: "",
          base_url: "",
          model: "",
        },
        {
          provider: "groq",
          enabled: true,
          api_key: "",
          base_url: "",
          model: "",
        },
        {
          provider: "groq",
          enabled: true,
          api_key: "",
          base_url: "",
          model: "",
        },
      ],
      presets: configured.presets,
    }
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => ({ ok: true, json: async () => data })),
    )
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    renderPage(qc)

    await screen.findByText("Primary")
    expect(screen.getAllByText("Fallback")).toHaveLength(1)
    expect(
      screen.getByText("Not used: needs an endpoint and model."),
    ).toBeTruthy()
    // custom has no default endpoint or model to suggest.
    expect(
      (document.getElementById("stt-base-url-0") as HTMLInputElement)
        .placeholder,
    ).toBe("Required")
    expect(
      (document.getElementById("stt-model-0") as HTMLInputElement).placeholder,
    ).toBe("Required")
  })

  it("does not use a custom backend with an endpoint but no model", async () => {
    const data: VoiceSTTResponse = {
      stt: [
        {
          provider: "custom",
          enabled: true,
          api_key: "",
          base_url: "https://stt.example.com/v1",
          model: "",
        },
        {
          provider: "groq",
          enabled: true,
          api_key: "",
          base_url: "",
          model: "",
        },
      ],
      presets: configured.presets,
    }
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => ({ ok: true, json: async () => data })),
    )
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    renderPage(qc)

    const notUsed = await screen.findByText(
      "Not used: needs an endpoint and model.",
    )
    // The custom row is the one marked; the groq row after it is Primary.
    const customRow = notUsed.closest("div.rounded-md.border")
    expect(customRow?.querySelector("select")?.value).toBe("custom")
    expect(customRow?.textContent).not.toMatch(/Primary|Fallback/)
    const primary = screen.getByText("Primary").closest("div.rounded-md.border")
    expect(primary?.querySelector("select")?.value).toBe("groq")
    expect(screen.queryByText("Fallback")).toBeNull()
  })
})
