import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import type { VoiceSTTResponse } from "@/api/voice"
import { SidebarProvider } from "@/components/ui/sidebar"

import { VoicePage } from "./voice-page"

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to }: { children: React.ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))

const configured: VoiceSTTResponse = {
  stt: [{ provider: "groq", enabled: true, model: "" }],
  providers: [
    { name: "groq", default_model: "whisper-large-v3", ready: true },
    { name: "my-openrouter", default_model: "", ready: true },
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

function newClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } })
}

function labelOfRow(i: number): string {
  const row = document
    .getElementById(`stt-provider-${i}`)
    ?.closest("div.rounded-md.border")
  const text = row?.textContent ?? ""
  const m = /Not used: [^.]*\.|Primary|Fallback/.exec(text)
  return m ? m[0] : ""
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
    renderPage(newClient())

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

  it("offers the configured providers in the select and suggests the default model", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => ({ ok: true, json: async () => configured })),
    )
    renderPage(newClient())

    const select = (await screen.findByLabelText(
      "Provider",
    )) as HTMLSelectElement
    expect(Array.from(select.options).map((o) => o.value)).toEqual([
      "groq",
      "my-openrouter",
    ])
    expect(
      (document.getElementById("stt-model-0") as HTMLInputElement).placeholder,
    ).toBe("whisper-large-v3")
    // No key or endpoint fields: they live on the Providers page.
    expect(screen.queryByLabelText(/API key/)).toBe(null)
    expect(screen.queryByLabelText(/Endpoint/)).toBe(null)
  })

  it("shows the empty state only when nothing is configured", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => ({
        ok: true,
        // The Go handler encodes an empty list as null when nothing is set.
        json: async () => ({ stt: null, providers: configured.providers }),
      })),
    )
    renderPage(newClient())

    await screen.findByText(/No transcription backends configured/)
    // Add backend is held until the provider list has arrived.
    await waitFor(() =>
      expect(
        (screen.getByText("Add backend") as HTMLButtonElement).disabled,
      ).toBe(false),
    )
    expect(screen.queryByLabelText("Provider")).toBe(null)
    expect(screen.queryByText(/Add a provider on the/)).toBe(null)
  })

  it("disables Add backend and points at the Providers page when no provider can transcribe", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => ({
        ok: true,
        json: async () => ({ stt: null, providers: null }),
      })),
    )
    renderPage(newClient())

    const hint = await screen.findByText(/Add a provider on the/)
    expect(hint.textContent).toBe("Add a provider on the Providers page first.")
    expect(hint.querySelector("a")?.getAttribute("href")).toBe("/providers")
    expect(
      (screen.getByText("Add backend") as HTMLButtonElement).disabled,
    ).toBe(true)
  })

  it("labels the primary and fallback backends and says why a backend is not used", async () => {
    const data: VoiceSTTResponse = {
      stt: [
        // Provider deleted from the Providers page (or no longer HTTP).
        { provider: "old-groq", enabled: true, model: "" },
        // Listed, but without an API key.
        { provider: "my-openai", enabled: true, model: "" },
        // Listed and ready, but the service has no known default model.
        { provider: "my-openrouter", enabled: true, model: "" },
        { provider: "groq", enabled: true, model: "" },
        { provider: "my-openrouter", enabled: true, model: "whisper-1" },
        { provider: "groq", enabled: false, model: "" },
      ],
      providers: [
        { name: "groq", default_model: "whisper-large-v3", ready: true },
        { name: "my-openai", default_model: "whisper-1", ready: false },
        { name: "my-openrouter", default_model: "", ready: true },
      ],
    }
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => ({ ok: true, json: async () => data })),
    )
    renderPage(newClient())

    await screen.findByText("Primary")
    expect(labelOfRow(0)).toBe("Not used: provider not configured.")
    expect(labelOfRow(1)).toBe("Not used: provider has no API key.")
    expect(labelOfRow(2)).toBe("Not used: needs a model.")
    expect(labelOfRow(3)).toBe("Primary")
    expect(labelOfRow(4)).toBe("Fallback")
    expect(labelOfRow(5)).toBe("")

    // The unlisted provider stays selected so the row is not rewritten.
    const select0 = document.getElementById(
      "stt-provider-0",
    ) as HTMLSelectElement
    expect(select0.value).toBe("old-groq")
    expect(Array.from(select0.options).map((o) => o.value)).toEqual([
      "old-groq",
      "groq",
      "my-openai",
      "my-openrouter",
    ])
    // A service with no known default asks for the model.
    expect(
      (document.getElementById("stt-model-2") as HTMLInputElement).placeholder,
    ).toBe("Required")
  })

  it("saves only provider, enabled and model for each backend", async () => {
    const fetchMock = vi.fn(async (_url: string, init?: RequestInit) => ({
      ok: true,
      json: async () =>
        init?.method === "PUT" ? { status: "ok" } : configured,
    }))
    vi.stubGlobal("fetch", fetchMock)
    renderPage(newClient())

    await screen.findByLabelText("Provider")
    fireEvent.click(screen.getByText("Add backend"))

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2), {
      timeout: 3000,
    })
    const [url, init] = fetchMock.mock.calls[1]
    expect(url).toBe("/api/voice/stt")
    expect(init?.method).toBe("PUT")
    expect(JSON.parse(init?.body as string)).toEqual({
      stt: [
        { provider: "groq", enabled: true, model: "" },
        // The new row takes the first listed provider; only the first row is
        // enabled by default.
        { provider: "groq", enabled: false, model: "" },
      ],
    })
    await screen.findByText("Saved ✓")
  })
})
