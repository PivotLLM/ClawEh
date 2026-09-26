import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import { SidebarProvider } from "@/components/ui/sidebar"

import { ModelsPage } from "./models-page"

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}))

// Answers by URL so the real API parsers run: they are what turn the server's
// nulls into arrays, and that is the behaviour under test.
function stubFetch(routes: Record<string, unknown>) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      const key = Object.keys(routes).find((k) => url.includes(k))
      if (!key) throw new Error(`unexpected fetch ${url}`)
      return { ok: true, json: async () => routes[key] }
    }),
  )
}

function renderPage() {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
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

describe("ModelsPage", () => {
  // Go encodes an empty slice as null. The page spreads `models` to sort it,
  // which threw "Spread syntax requires ...iterable not be null or undefined"
  // straight into the route error boundary on an install with no models.
  it("renders with a null model list instead of crashing", async () => {
    stubFetch({
      "/api/models": { models: null, total: 0, default_model: "" },
      "/api/providers": { providers: null, total: 0 },
    })
    renderPage()

    await screen.findByText("models.description")
    expect(screen.getByText("models.noDefaultHintPrefix")).toBeTruthy()
    expect(screen.queryByTestId("route-error")).toBe(null)
  })

  it("lists a model whose nullable fields are null", async () => {
    stubFetch({
      "/api/models": {
        models: [
          {
            index: 0,
            model_name: "probe-model",
            model: "gpt-x",
            provider: "openai",
            enabled: true,
            configured: true,
            is_default: true,
            drop_params: null,
            extra_body: null,
          },
        ],
        total: 1,
        default_model: "probe-model",
      },
      "/api/providers": { providers: null, total: 0 },
    })
    renderPage()

    expect((await screen.findAllByText("probe-model")).length).toBeGreaterThan(
      0,
    )
    expect(screen.queryByTestId("route-error")).toBe(null)
  })
})
