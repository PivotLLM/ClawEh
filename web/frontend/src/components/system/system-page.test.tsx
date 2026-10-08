import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import { patchAppConfig } from "@/api/channels"
import { SidebarProvider } from "@/components/ui/sidebar"

import { SystemPage } from "./system-page"

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}))

vi.mock("@/api/channels", () => ({
  patchAppConfig: vi.fn().mockResolvedValue({ status: "ok" }),
  HUMAN_AGENTS_QUERY_KEY: ["agents-human-problems"],
  getHumanAgents: vi.fn().mockResolvedValue({ problems: [], human_agents: [] }),
}))

// The page links to /config/raw; the router is not mounted here.
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to }: { children: React.ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))

const patched = vi.mocked(patchAppConfig)

const sample = {
  agents: {
    list: [{ id: "alice", name: "Alice" }],
    defaults: { max_tokens: 4096 },
  },
  backup: { enabled: true, at: "02:30", retain_days: 14, dest: "/mnt/backup" },
}

// Renders the page and returns the fetch mock. `qc` lets a test start from a
// cache that already holds the config.
function renderPage(
  qc = new QueryClient({ defaultOptions: { queries: { retry: false } } }),
) {
  // The page loads /api/config; the model-default sections also load
  // /api/models through useChatModels and index into its `models` array.
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input)
    const body = url.includes("/api/models")
      ? { models: [], default_model: "" }
      : sample
    return { ok: true, json: async () => body }
  })
  vi.stubGlobal("fetch", fetchMock)
  // SidebarProvider because PageHeader reads the sidebar context.
  render(
    <QueryClientProvider client={qc}>
      <SidebarProvider>
        <SystemPage />
      </SidebarProvider>
    </QueryClientProvider>,
  )
  return fetchMock
}

// A client whose cache already holds the config, as when the page is reached
// from another page that fetched the same key.
function cachedClient() {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  })
  qc.setQueryData(["config"], sample)
  return qc
}

/** The `backup` block of the most recent PATCH. */
function lastBackupPatch() {
  const patch = patched.mock.calls.at(-1)?.[0] as
    { backup?: Record<string, unknown> } | undefined
  return patch?.backup
}

afterEach(() => {
  vi.unstubAllGlobals()
  patched.mockClear()
})

describe("SystemPage backup block", () => {
  // The page rebuilds `backup` from its form fields on every save. A field the
  // form does not carry is silently dropped from the config the moment any
  // other field is edited — which is what happened to backup.dest.
  it("keeps backup.dest in the saved block", async () => {
    renderPage()
    const dest = await screen.findByTestId("backup-dest")
    expect((dest as HTMLInputElement).value).toBe("/mnt/backup")

    fireEvent.change(dest, { target: { value: "/mnt/other" } })

    // The save is debounced (600 ms); wait on the call rather than the clock.
    await waitFor(() => expect(patched).toHaveBeenCalledTimes(1), {
      timeout: 3000,
    })
    expect(lastBackupPatch()).toEqual({
      enabled: true,
      at: "02:30",
      retain_days: 14,
      dest: "/mnt/other",
    })
    await screen.findByText("Saved ✓")
  })

  // Clearing the field must reach the server: the PATCH is a merge, so a key
  // left out is a key left alone.
  // The listeners belong to the Network page; a patch from here that carried
  // gateway.* would overwrite what was saved there.
  it("never writes gateway settings", async () => {
    renderPage()
    const dest = await screen.findByTestId("backup-dest")
    fireEvent.change(dest, { target: { value: "/mnt/other" } })
    await waitFor(() => expect(patched).toHaveBeenCalledTimes(1), {
      timeout: 3000,
    })
    const patch = patched.mock.calls.at(-1)?.[0] as Record<string, unknown>
    expect(patch).not.toHaveProperty("gateway")
    expect(patch).not.toHaveProperty("channels")
  })

  it("sends an empty dest so the field can be cleared", async () => {
    renderPage()
    const dest = await screen.findByTestId("backup-dest")

    fireEvent.change(dest, { target: { value: "  " } })

    await waitFor(() => expect(patched).toHaveBeenCalledTimes(1), {
      timeout: 3000,
    })
    expect(lastBackupPatch()).toMatchObject({ dest: "" })
    await screen.findByText("Saved ✓")
  })
})

describe("SystemPage with the config already cached", () => {
  // The page used to seed its form only when the query returned a new object.
  // With the config already cached that never happened, the page showed the
  // defaults, and the next edit saved those defaults over the configuration.
  it("shows the configuration, not the defaults", async () => {
    const fetchMock = renderPage(cachedClient())
    const dest = await screen.findByTestId("backup-dest")
    expect((dest as HTMLInputElement).value).toBe("/mnt/backup")
    expect(screen.getByDisplayValue("4096")).toBeTruthy()
    const urls = fetchMock.mock.calls.map((c) => String(c[0]))
    expect(urls.some((u) => u.includes("/api/config"))).toBe(false)
  })

  it("an edit saves the configured values with the change", async () => {
    renderPage(cachedClient())
    const dest = await screen.findByTestId("backup-dest")
    fireEvent.change(dest, { target: { value: "/mnt/other" } })
    await waitFor(() => expect(patched).toHaveBeenCalledTimes(1), {
      timeout: 3000,
    })
    expect(lastBackupPatch()).toEqual({
      enabled: true,
      at: "02:30",
      retain_days: 14,
      dest: "/mnt/other",
    })
    const patch = patched.mock.calls.at(-1)?.[0] as {
      agents: { defaults: { max_tokens: number } }
    }
    expect(patch.agents.defaults.max_tokens).toBe(4096)
  })
})
