import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import { getAppConfig, patchAppConfig } from "@/api/channels"
import { SidebarProvider } from "@/components/ui/sidebar"

import { MCPConfigPage } from "./mcp-page"

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}))

vi.mock("@/api/channels", () => ({
  getAppConfig: vi.fn(),
  patchAppConfig: vi.fn().mockResolvedValue({ status: "ok" }),
}))

const configFetch = vi.mocked(getAppConfig)
const patched = vi.mocked(patchAppConfig)

const sample = {
  mcp_host: {
    enabled: true,
    listen: "0.0.0.0:6000",
    endpoint_path: "/tools",
    internal_tools: ["file_"],
    external_tools: ["*"],
  },
  tools: {
    discovery: { enabled: true, ttl_max: 20, visible_budget: 40 },
    mcp: {
      reconnect_cooldown_seconds: 45,
      call_timeout_seconds: 120,
      liveness_probe_seconds: 15,
      servers: { alpha: { type: "http", url: "http://alpha:9000/mcp" } },
    },
  },
}

// A client whose cache already holds the config, as when the page is reached
// from another page that fetched the same key.
function renderCached() {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  })
  qc.setQueryData(["app-config"], sample)
  // SidebarProvider because PageHeader reads the sidebar context.
  return render(
    <QueryClientProvider client={qc}>
      <SidebarProvider>
        <MCPConfigPage />
      </SidebarProvider>
    </QueryClientProvider>,
  )
}

afterEach(() => {
  vi.clearAllMocks()
})

describe("MCPConfigPage with the config already cached", () => {
  // The page used to seed its form only when the query returned a new object.
  // With the config already cached that never happened, the page showed the
  // defaults, and the next edit saved those defaults over the configuration.
  it("shows the configured listen address, not the default", async () => {
    renderCached()

    expect(await screen.findByDisplayValue("0.0.0.0:6000")).toBeTruthy()
    expect(screen.getByDisplayValue("/tools")).toBeTruthy()
    await waitFor(() => expect(configFetch).not.toHaveBeenCalled())
  })

  it("an edit saves the configured values with the change, and never the server list", async () => {
    renderCached()

    const path = await screen.findByDisplayValue("/tools")
    fireEvent.change(path, { target: { value: "/mcp2" } })

    // The save is debounced (600 ms); wait on the call rather than the clock.
    await waitFor(() => expect(patched).toHaveBeenCalledTimes(1), {
      timeout: 3000,
    })
    const patch = patched.mock.calls[0][0]
    expect(patch.mcp_host).toEqual({
      enabled: true,
      auto_enable: true,
      listen: "0.0.0.0:6000",
      endpoint_path: "/mcp2",
      internal_tools: ["file_"],
      external_tools: ["*"],
    })
    expect(patch.tools).toEqual({
      discovery: {
        enabled: true,
        ttl_max: 20,
        visible_budget: 40,
        always_shown_namespaces: [],
      },
      mcp: {
        reconnect_cooldown_seconds: 45,
        call_timeout_seconds: 120,
        liveness_probe_seconds: 15,
      },
    })
  })
})
