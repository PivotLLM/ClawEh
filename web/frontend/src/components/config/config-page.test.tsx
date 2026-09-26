import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import { patchAppConfig } from "@/api/channels"
import { SidebarProvider } from "@/components/ui/sidebar"

import { ConfigPage } from "./config-page"

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}))

vi.mock("@/api/channels", () => ({
  patchAppConfig: vi.fn().mockResolvedValue({ status: "ok" }),
}))

// The page links to /config/raw; the router is not mounted here.
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to }: { children: React.ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))

const patched = vi.mocked(patchAppConfig)

const sample = {
  agents: { list: [{ id: "alice", name: "Alice" }] },
  backup: { enabled: true, at: "02:30", retain_days: 14, dest: "/mnt/backup" },
}

function renderPage() {
  // The page loads /api/config; the model-default sections also load
  // /api/models through useChatModels and index into its `models` array.
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      const body = url.includes("/api/models")
        ? { models: [], default_model: "" }
        : sample
      return { ok: true, json: async () => body }
    }),
  )
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  // SidebarProvider because PageHeader reads the sidebar context.
  return render(
    <QueryClientProvider client={qc}>
      <SidebarProvider>
        <ConfigPage />
      </SidebarProvider>
    </QueryClientProvider>,
  )
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

describe("ConfigPage backup block", () => {
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
