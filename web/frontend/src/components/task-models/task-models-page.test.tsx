import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import type { TaskModelsResponse } from "@/api/task-models"
import { SidebarProvider } from "@/components/ui/sidebar"

import { TaskModelsPage } from "./task-models-page"

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to }: { children: React.ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))

const configured: TaskModelsResponse = {
  task_models: {
    result_text_chars: 2000,
    models: [
      { model: "whisper", stt: true },
      { model: "sonnet", vision: true, stt: true },
      { model: "gone", text: true },
    ],
  },
  defaults: { result_text_chars: 8000 },
  models: [
    { name: "whisper", enabled: true, support: { stt: "yes" } },
    { name: "sonnet", enabled: true, support: { stt: "no", vision: "yes" } },
    { name: "tts-1", enabled: true, support: {} },
    { name: "off", enabled: false, support: {} },
  ],
}

function renderPage(qc: QueryClient) {
  // SidebarProvider because PageHeader reads the sidebar context.
  return render(
    <QueryClientProvider client={qc}>
      <SidebarProvider>
        <TaskModelsPage />
      </SidebarProvider>
    </QueryClientProvider>,
  )
}

function newClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } })
}

// mockFetch answers GET with the configured block and records every PUT body.
function mockFetch() {
  const puts: unknown[] = []
  const fetchMock = vi.fn(async (_url: string, init?: RequestInit) => {
    if (init?.method === "PUT") {
      puts.push(JSON.parse(String(init.body)))
      return { ok: true, json: async () => ({ status: "ok" }) }
    }
    return { ok: true, json: async () => structuredClone(configured) }
  })
  vi.stubGlobal("fetch", fetchMock)
  return { fetchMock, puts }
}

afterEach(() => {
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

describe("TaskModelsPage", () => {
  it("lists the task models in page order with their ticks, without saving", async () => {
    const { fetchMock } = mockFetch()
    renderPage(newClient())

    const stt = await screen.findByLabelText("whisper: Transcription (STT)")
    expect(stt.getAttribute("data-state")).toBe("checked")
    expect(
      screen.getByLabelText("sonnet: Vision").getAttribute("data-state"),
    ).toBe("checked")
    expect(
      screen.getByLabelText("sonnet: Text").getAttribute("data-state"),
    ).toBe("unchecked")
    const order = screen
      .getAllByTestId(/^task-model-(whisper|sonnet|gone)$/)
      .map((el) => el.dataset.testid)
    expect(order).toEqual([
      "task-model-whisper",
      "task-model-sonnet",
      "task-model-gone",
    ])
    expect(
      (
        screen.getByLabelText(
          "Text in results (characters)",
        ) as HTMLInputElement
      ).value,
    ).toBe("2000")
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it("marks a use the provider can't serve and a model not on the Models page", async () => {
    mockFetch()
    renderPage(newClient())
    const note = await screen.findByTestId("task-model-unsupported-sonnet")
    expect(note.textContent).toBe(
      "sonnet's provider can't do Transcription (STT).",
    )
    expect(screen.queryByTestId("task-model-unsupported-whisper")).toBeNull()
    expect(
      screen.getByText("Not on the Models page, so it is not used."),
    ).toBeTruthy()
  })

  it("offers only models not on the page", async () => {
    mockFetch()
    renderPage(newClient())
    const select = (await screen.findByLabelText(
      "Model to add",
    )) as HTMLSelectElement
    // Sorted by name, not in config order.
    expect([...select.options].map((o) => o.value)).toEqual(["off", "tts-1"])
  })

  it("saves the whole block after an edit: tick, reorder, add, TTS defaults", async () => {
    const { puts } = mockFetch()
    renderPage(newClient())
    await screen.findByLabelText("whisper: Transcription (STT)")

    fireEvent.click(screen.getByLabelText("Move sonnet up"))
    fireEvent.change(screen.getByLabelText("Model to add"), {
      target: { value: "tts-1" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Add model" }))
    fireEvent.click(screen.getByLabelText("tts-1: Speech (TTS)"))
    fireEvent.change(await screen.findByLabelText("Voice"), {
      target: { value: "alloy" },
    })
    fireEvent.click(screen.getByLabelText("Remove gone"))

    await waitFor(() => expect(puts.length).toBe(1), { timeout: 3000 })
    expect(puts[0]).toEqual({
      result_text_chars: 2000,
      models: [
        { model: "sonnet", stt: true, vision: true },
        { model: "whisper", stt: true },
        { model: "tts-1", tts: true, tts_defaults: { voice: "alloy" } },
      ],
    })
  })

  it("does not save a limit that is not a whole number", async () => {
    const { puts } = mockFetch()
    renderPage(newClient())
    await screen.findByLabelText("whisper: Transcription (STT)")
    const field = screen.getByLabelText("Text in results (characters)")
    fireEvent.change(field, { target: { value: "-4" } })
    expect(
      screen.getByText("Enter a whole number, or leave it blank."),
    ).toBeTruthy()
    await new Promise((r) => setTimeout(r, 800))
    expect(puts).toEqual([])
  })

  it("notes that echo does nothing without a Transcription model", async () => {
    const { puts } = mockFetch()
    renderPage(newClient())
    await screen.findByLabelText("whisper: Transcription (STT)")
    fireEvent.click(screen.getByLabelText("whisper: Transcription (STT)"))
    fireEvent.click(screen.getByLabelText("sonnet: Transcription (STT)"))
    fireEvent.click(screen.getByRole("switch", { name: "Echo transcription" }))
    expect(
      screen.getByText(
        "No model is ticked Transcription (STT), so nothing is transcribed.",
      ),
    ).toBeTruthy()
    await waitFor(() => expect(puts.length).toBe(1), { timeout: 3000 })
    expect(puts[0]).toMatchObject({ echo_transcription: true })
  })
})

describe("TaskModelsPage leaving with an edit pending", () => {
  it("saves the edit at once instead of dropping it", async () => {
    const { puts } = mockFetch()
    const view = renderPage(newClient())
    await screen.findByLabelText("whisper: Transcription (STT)")
    fireEvent.click(screen.getByLabelText("whisper: Text"))
    expect(puts).toEqual([])
    view.unmount()
    await waitFor(() => expect(puts.length).toBe(1))
    expect(
      (puts[0] as { models: { model: string; text?: boolean }[] }).models[0],
    ).toEqual({ model: "whisper", text: true, stt: true })
  })

  it("saves nothing when there is no pending edit", async () => {
    const { puts } = mockFetch()
    const view = renderPage(newClient())
    await screen.findByLabelText("whisper: Transcription (STT)")
    view.unmount()
    await new Promise((r) => setTimeout(r, 50))
    expect(puts).toEqual([])
  })
})
