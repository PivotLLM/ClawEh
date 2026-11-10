import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
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
    text: [{ model: "gone" }],
    image: [{ model: "sonnet", image_api: "images" }],
    tts: [],
    stt: [{ model: "whisper" }, { model: "sonnet" }],
    vision: [{ model: "sonnet" }],
    classify: [],
  },
  defaults: { result_text_chars: 8000 },
  models: [
    { name: "whisper", enabled: true, support: { stt: "yes" } },
    {
      name: "sonnet",
      enabled: true,
      support: { stt: "no", vision: "yes", image: "unknown" },
    },
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

const rowsOf = (use: string) =>
  within(screen.getByTestId(`task-list-${use}`))
    .queryAllByTestId(new RegExp(`^task-model-${use}-`))
    .map((el) => el.dataset.testid?.slice(`task-model-${use}-`.length))

afterEach(() => {
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

describe("TaskModelsPage", () => {
  it("shows the six lists in order, each with its rows in order, without saving", async () => {
    const { fetchMock } = mockFetch()
    renderPage(newClient())
    await screen.findByTestId("task-list-stt")

    const titles = screen
      .getAllByTestId(/^task-list-(text|image|tts|stt|vision|classify)$/)
      .map((el) => el.querySelector("[data-slot=card-title]")?.textContent)
    expect(titles).toEqual([
      "Generate text",
      "Generate images",
      "Text to speech",
      "Transcription",
      "Describe images",
      "Classification",
    ])
    expect(
      screen.getByText("Turns voice messages and audio into text."),
    ).toBeTruthy()
    expect(rowsOf("stt")).toEqual(["whisper", "sonnet"])
    expect(rowsOf("vision")).toEqual(["sonnet"])
    expect(rowsOf("tts")).toEqual([])
    expect(screen.getByTestId("task-list-empty-tts").textContent).toBe(
      "No models.",
    )
    expect(screen.getByTestId("task-list-empty-classify").textContent).toBe(
      "No models. Classifiers can't run.",
    )
    expect(screen.queryByTestId("task-list-empty-stt")).toBeNull()
    expect(
      (
        screen.getByLabelText(
          "Text in results (characters)",
        ) as HTMLInputElement
      ).value,
    ).toBe("2000")
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it("disables Move up on the first row and Move down on the last, per list", async () => {
    mockFetch()
    renderPage(newClient())
    const up = await screen.findByRole("button", {
      name: "Move whisper up in Transcription",
    })
    expect((up as HTMLButtonElement).disabled).toBe(true)
    expect(
      (
        screen.getByRole("button", {
          name: "Move sonnet down in Transcription",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(true)
    // sonnet is alone in Describe images: both ends.
    expect(
      (
        screen.getByRole("button", {
          name: "Move sonnet up in Describe images",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(true)
  })

  it("marks a model whose provider can't serve that list, and only there", async () => {
    mockFetch()
    renderPage(newClient())
    const note = await screen.findByTestId("task-model-unsupported-stt-sonnet")
    expect(note.textContent).toBe("sonnet's provider can't do this.")
    expect(
      screen.queryByTestId("task-model-unsupported-vision-sonnet"),
    ).toBeNull()
    expect(
      screen.queryByTestId("task-model-unsupported-image-sonnet"),
    ).toBeNull()
    expect(
      screen.queryByTestId("task-model-unsupported-stt-whisper"),
    ).toBeNull()
    expect(
      screen.getByText("Not on the Models page, so it is not used."),
    ).toBeTruthy()
  })

  it("offers each list the enabled models it does not hold, sorted", async () => {
    mockFetch()
    renderPage(newClient())
    const options = async (title: string) =>
      [
        ...(
          (await screen.findByLabelText(
            `Model to add to ${title}`,
          )) as HTMLSelectElement
        ).options,
      ].map((o) => o.value)
    expect(await options("Transcription")).toEqual(["tts-1"])
    expect(await options("Describe images")).toEqual(["tts-1", "whisper"])
    expect(await options("Text to speech")).toEqual([
      "sonnet",
      "tts-1",
      "whisper",
    ])
  })

  it("shows Images through only on Generate images and Voice/Format only on Text to speech", async () => {
    mockFetch()
    renderPage(newClient())
    const api = (await screen.findByLabelText(
      "Images through for sonnet",
    )) as HTMLSelectElement
    expect(api.value).toBe("images")
    expect(screen.getAllByLabelText(/^Images through for/)).toHaveLength(1)
    expect(screen.queryByLabelText(/^Voice for/)).toBeNull()
  })

  it("saves every list after edits: add to two lists, reorder, settings, remove", async () => {
    const { puts } = mockFetch()
    renderPage(newClient())
    await screen.findByTestId("task-list-stt")

    fireEvent.click(
      screen.getByRole("button", { name: "Move sonnet up in Transcription" }),
    )
    fireEvent.change(screen.getByLabelText("Model to add to Text to speech"), {
      target: { value: "tts-1" },
    })
    fireEvent.click(
      screen.getByRole("button", { name: "Add model to Text to speech" }),
    )
    fireEvent.change(await screen.findByLabelText("Voice for tts-1"), {
      target: { value: "alloy" },
    })
    fireEvent.change(screen.getByLabelText("Format for tts-1"), {
      target: { value: "mp3" },
    })
    fireEvent.change(screen.getByLabelText("Model to add to Generate images"), {
      target: { value: "tts-1" },
    })
    fireEvent.click(
      screen.getByRole("button", { name: "Add model to Generate images" }),
    )
    fireEvent.change(screen.getByLabelText("Images through for sonnet"), {
      target: { value: "chat" },
    })
    fireEvent.change(screen.getByLabelText("Images through for tts-1"), {
      target: { value: "images" },
    })
    fireEvent.click(
      screen.getByRole("button", { name: "Remove gone from Generate text" }),
    )

    await waitFor(() => expect(puts.length).toBe(1), { timeout: 3000 })
    expect(puts[0]).toEqual({
      result_text_chars: 2000,
      text: [],
      image: [{ model: "sonnet" }, { model: "tts-1", image_api: "images" }],
      tts: [
        { model: "tts-1", tts_defaults: { voice: "alloy", format: "mp3" } },
      ],
      stt: [{ model: "sonnet" }, { model: "whisper" }],
      vision: [{ model: "sonnet" }],
      classify: [],
    })
    expect(await screen.findByText("Saved ✓")).toBeTruthy()
  })

  it("does not save a limit that is not a whole number", async () => {
    const { puts } = mockFetch()
    renderPage(newClient())
    await screen.findByTestId("task-list-stt")
    const field = screen.getByLabelText("Text in results (characters)")
    fireEvent.change(field, { target: { value: "-4" } })
    expect(
      screen.getByText("Enter a whole number, or leave it blank."),
    ).toBeTruthy()
    await new Promise((r) => setTimeout(r, 800))
    expect(puts).toEqual([])
  })

  it("still saves list edits while Text in results is invalid, with the last valid limit", async () => {
    const { puts } = mockFetch()
    renderPage(newClient())
    await screen.findByTestId("task-list-stt")
    fireEvent.change(screen.getByLabelText("Text in results (characters)"), {
      target: { value: "12a" },
    })
    fireEvent.click(
      screen.getByRole("button", { name: "Remove whisper from Transcription" }),
    )
    await waitFor(() => expect(puts.length).toBe(1), { timeout: 3000 })
    expect(puts[0]).toMatchObject({
      result_text_chars: 2000,
      stt: [{ model: "sonnet" }],
    })
    expect(
      screen.getByText("Enter a whole number, or leave it blank."),
    ).toBeTruthy()
  })

  it("notes that echo does nothing while Transcription is empty", async () => {
    const { puts } = mockFetch()
    renderPage(newClient())
    await screen.findByTestId("task-list-stt")
    fireEvent.click(screen.getByRole("switch", { name: "Echo transcription" }))
    expect(screen.queryByTestId("task-echo-no-model")).toBeNull()
    fireEvent.click(
      screen.getByRole("button", { name: "Remove whisper from Transcription" }),
    )
    fireEvent.click(
      screen.getByRole("button", { name: "Remove sonnet from Transcription" }),
    )
    expect(screen.getByTestId("task-echo-no-model").textContent).toBe(
      "No model in Transcription, so nothing is transcribed.",
    )
    await waitFor(() => expect(puts.length).toBe(1), { timeout: 3000 })
    expect(puts[0]).toMatchObject({ echo_transcription: true, stt: [] })
  })
})

describe("TaskModelsPage leaving with an edit pending", () => {
  it("saves the edit at once instead of dropping it", async () => {
    const { puts } = mockFetch()
    const view = renderPage(newClient())
    await screen.findByTestId("task-list-stt")
    fireEvent.click(
      screen.getByRole("button", { name: "Add model to Classification" }),
    )
    expect(puts).toEqual([])
    view.unmount()
    await waitFor(() => expect(puts.length).toBe(1))
    expect((puts[0] as { classify: unknown }).classify).toEqual([
      { model: "sonnet" },
    ])
  })

  it("shows the saved lists on return, even when a refetch started before the save answers after it", async () => {
    // A slow server: the PUT is applied after 50 ms, a GET answers after
    // 300 ms with the block as it was when the GET arrived.
    let server = structuredClone(configured)
    vi.stubGlobal(
      "fetch",
      vi.fn(async (_url: string, init?: RequestInit) => {
        if (init?.method === "PUT") {
          const body = JSON.parse(String(init.body))
          await new Promise((r) => setTimeout(r, 50))
          server = { ...server, task_models: body }
          return { ok: true, json: async () => ({ status: "ok" }) }
        }
        const snapshot = structuredClone(server)
        await new Promise((r) => setTimeout(r, 300))
        return { ok: true, json: async () => snapshot }
      }),
    )
    const qc = newClient()
    const first = renderPage(qc)
    await screen.findByTestId("task-list-stt", undefined, { timeout: 2000 })
    fireEvent.click(
      screen.getByRole("button", { name: "Add model to Classification" }),
    )
    first.unmount() // saves on leaving
    renderPage(qc) // back at once: its refetch sees the block before the save
    await new Promise((r) => setTimeout(r, 700))
    expect(
      within(screen.getByTestId("task-list-classify")).queryByTestId(
        "task-model-classify-sonnet",
      ),
    ).not.toBeNull()
  })

  it("saves nothing when there is no pending edit", async () => {
    const { puts } = mockFetch()
    const view = renderPage(newClient())
    await screen.findByTestId("task-list-stt")
    view.unmount()
    await new Promise((r) => setTimeout(r, 50))
    expect(puts).toEqual([])
  })
})
