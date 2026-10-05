import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import dayjs from "dayjs"
import relativeTime from "dayjs/plugin/relativeTime"
import { afterEach, describe, expect, it, vi } from "vitest"

import { getAppConfig, getMCPStatus, patchAppConfig } from "@/api/channels"
import { SidebarProvider } from "@/components/ui/sidebar"

import { MCPServersPage } from "./mcp-servers-page"

// The app extends dayjs in its i18n setup, which is not loaded here.
dayjs.extend(relativeTime)

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}))

vi.mock("@/api/channels", () => ({
  getAppConfig: vi.fn(),
  getMCPStatus: vi.fn(),
  patchAppConfig: vi.fn().mockResolvedValue({ status: "ok" }),
  reconnectMCPServer: vi.fn(),
}))

vi.mock("@/api/system", () => ({
  reloadGateway: vi.fn(),
}))

const configFetch = vi.mocked(getAppConfig)
const statusFetch = vi.mocked(getMCPStatus)
const patched = vi.mocked(patchAppConfig)

const sample = {
  tools: {
    mcp: {
      servers: {
        alpha: { type: "http", url: "http://alpha:9000/mcp", enabled: true },
        beta: {
          type: "stdio",
          command: "npx",
          args: ["beta-mcp"],
          enabled: false,
        },
      },
    },
  },
}

function renderPage(qc: QueryClient) {
  // SidebarProvider because PageHeader reads the sidebar context.
  return render(
    <QueryClientProvider client={qc}>
      <SidebarProvider>
        <MCPServersPage />
      </SidebarProvider>
    </QueryClientProvider>,
  )
}

// A client whose cache already holds the config, as when the page is reached
// from another page that fetched the same key.
function cachedClient() {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  })
  qc.setQueryData(["app-config"], sample)
  return qc
}

afterEach(() => {
  vi.clearAllMocks()
})

describe("MCPServersPage with the config already cached", () => {
  // The page used to seed its list only when the query returned a new object.
  // With the config already cached that never happened, the page said no
  // servers were configured, and the next edit saved that empty list.
  it("lists the configured servers, not the empty state", async () => {
    statusFetch.mockResolvedValue({ servers: [] })
    renderPage(cachedClient())

    expect(await screen.findByRole("button", { name: "alpha" })).toBeTruthy()
    expect(screen.getByRole("button", { name: "beta" })).toBeTruthy()
    expect(screen.queryByText("pages.mcp.client_empty")).toBe(null)
    // The first server is selected and its fields are filled.
    expect(screen.getByDisplayValue("http://alpha:9000/mcp")).toBeTruthy()
    await waitFor(() => expect(configFetch).not.toHaveBeenCalled())
  })

  it("an edit saves the changed server and keeps the others", async () => {
    statusFetch.mockResolvedValue({ servers: [] })
    renderPage(cachedClient())

    const url = await screen.findByDisplayValue("http://alpha:9000/mcp")
    fireEvent.change(url, { target: { value: "http://alpha:9001/mcp" } })

    // The save is debounced (600 ms); wait on the call rather than the clock.
    await waitFor(() => expect(patched).toHaveBeenCalledTimes(1), {
      timeout: 3000,
    })
    const patch = patched.mock.calls[0][0] as {
      tools: { mcp: { servers: Record<string, unknown> } }
    }
    expect(patch.tools.mcp.servers).toEqual({
      alpha: { enabled: true, type: "http", url: "http://alpha:9001/mcp" },
      beta: {
        enabled: false,
        type: "stdio",
        command: "npx",
        args: ["beta-mcp"],
      },
    })
  })
})

describe("MCPServersPage failure reason", () => {
  it("shows why the selected server is down, and nothing when it is not", async () => {
    configFetch.mockResolvedValue(sample)
    statusFetch.mockResolvedValue({
      servers: [
        {
          name: "alpha",
          state: "cooldown",
          tool_count: 0,
          last_error: "dial tcp 127.0.0.1:9000: connection refused",
          last_error_at: new Date(Date.now() - 3 * 60 * 1000).toISOString(),
        },
        { name: "beta", state: "connected", tool_count: 3 },
      ],
    })
    renderPage(
      new QueryClient({ defaultOptions: { queries: { retry: false } } }),
    )

    const line = await screen.findByTestId("mcp-last-error")
    expect(line.textContent).toBe(
      "dial tcp 127.0.0.1:9000: connection refused (3 minutes ago)",
    )

    fireEvent.click(screen.getByRole("button", { name: "beta" }))
    await waitFor(() =>
      expect(screen.queryByTestId("mcp-last-error")).toBe(null),
    )
  })
})
