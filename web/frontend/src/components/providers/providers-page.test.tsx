import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import { SidebarProvider } from "@/components/ui/sidebar"

import { ProvidersPage } from "./providers-page"

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (key: string, opts?: Record<string, unknown>) =>
      opts ? `${key}:${JSON.stringify(opts)}` : key,
  }),
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
        <ProvidersPage />
      </SidebarProvider>
    </QueryClientProvider>,
  )
}

// A CLI row as GET /api/system/clis returns it for a CLI with no bypass flags
// and no configured models: the nil slices come back as null.
const cursor = {
  protocol: "cursor-cli",
  label: "Cursor CLI",
  binary: "cursor-agent",
  installed: true,
  path: "/usr/local/bin/cursor-agent",
  enabled: true,
  configured: true,
  provider_index: 0,
  models: 1,
  models_enabled: 1,
  base_args: ["-p", "--output-format", "json"],
  required_args: null,
  bypass_args: null,
  bypass_restrictions: true,
  extra_args: null,
  trailing_args: null,
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe("ProvidersPage", () => {
  // Go encodes an empty slice as null. The page spreads `providers` to sort
  // it, and every CLI row spreads its argument lists to print the command line,
  // so a null anywhere threw "Spread syntax requires ...iterable" into the
  // route error boundary.
  it("renders with null provider and argument lists instead of crashing", async () => {
    stubFetch({
      "/api/providers": { providers: null, total: 0 },
      "/api/system/clis": [cursor],
    })
    renderPage()

    await screen.findByTestId("cli-row-cursor-cli")
    expect(screen.getByText("providers.empty")).toBeTruthy()
    expect(screen.queryByTestId("route-error")).toBe(null)
  })

  it("lists a CLI provider alongside its row", async () => {
    stubFetch({
      "/api/providers": {
        providers: [
          {
            index: 0,
            name: "cursor",
            protocol: "cursor-cli",
            api_key: "",
            ready: true,
            resolved_command: "/usr/local/bin/cursor-agent",
            model_count: 1,
          },
        ],
        total: 1,
      },
      "/api/system/clis": [cursor],
    })
    renderPage()

    const row = await screen.findByTestId("cli-row-cursor-cli")
    expect(row.textContent).toContain("-p --output-format json")
    // CLI providers live in the section, not the grid.
    expect(screen.getByText("providers.empty")).toBeTruthy()
    expect(screen.queryByTestId("route-error")).toBe(null)
  })
})
