import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { fireEvent, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import { SidebarProvider } from "@/components/ui/sidebar"

import { ModelsPage } from "./models-page"

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}))

// The sheets render the Turn timeout note from the turnTimeout they are
// given; here they only record it, so the test checks what the page passes.
vi.mock("./add-model-sheet", () => ({
  AddModelSheet: ({
    open,
    turnTimeout,
  }: {
    open: boolean
    turnTimeout: number
  }) =>
    open ? <div data-testid="add-sheet">{`add:${turnTimeout}`}</div> : null,
}))
vi.mock("./edit-model-sheet", () => ({
  EditModelSheet: ({
    open,
    turnTimeout,
  }: {
    open: boolean
    turnTimeout: number
  }) =>
    open ? <div data-testid="edit-sheet">{`edit:${turnTimeout}`}</div> : null,
}))

function renderPage() {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      const body = url.includes("/api/models")
        ? {
            models: [
              {
                index: 0,
                model_name: "alice-model",
                model: "gpt-x",
                provider: "openai",
                enabled: true,
                configured: true,
                is_default: true,
                request_timeout: 1200,
              },
            ],
            total: 1,
            default_model: "alice-model",
            turn_timeout: 600,
          }
        : { providers: null, total: 0 }
      return { ok: true, json: async () => body }
    }),
  )
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <SidebarProvider>
        <ModelsPage />
      </SidebarProvider>
    </QueryClientProvider>,
  )
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe("ModelsPage turn timeout", () => {
  it("passes the install's turn timeout to the add sheet", async () => {
    renderPage()
    await screen.findAllByText("alice-model")
    fireEvent.click(screen.getByRole("button", { name: /models.add.button/ }))
    expect((await screen.findByTestId("add-sheet")).textContent).toBe("add:600")
  })

  it("passes the install's turn timeout to the edit sheet", async () => {
    renderPage()
    await screen.findAllByText("alice-model")
    fireEvent.click(screen.getByTitle("models.action.edit"))
    expect((await screen.findByTestId("edit-sheet")).textContent).toBe(
      "edit:600",
    )
  })
})
