import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import { SidebarProvider } from "@/components/ui/sidebar"

import { StatusPage } from "./status-page"

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (key: string, opts?: Record<string, unknown>) =>
      opts && "value" in opts ? `${key}:${String(opts.value)}` : key,
  }),
}))

function renderPage() {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  // SidebarProvider because PageHeader reads the sidebar context. Wrapping the
  // real provider rather than mocking the header keeps the test honest about
  // the component actually rendering in its tree.
  return render(
    <QueryClientProvider client={qc}>
      <SidebarProvider>
        <StatusPage />
      </SidebarProvider>
    </QueryClientProvider>,
  )
}

const sample = {
  version: "0.5.0+abc",
  build: "2026-09-07T02:00:00-0400",
  uptime_seconds: 3661,
  uptime: "1h1m1s",
  pid: 4242,
  memory_bytes: 66_100_000,
  go_version: "go1.27.1",
  os: "linux",
  arch: "amd64",
  os_name: "Ubuntu 24.04.4 LTS",
  goroutines: 39,
  agents: 7,
  models: 3,
  providers: 24,
  channels: 5,
  cli_providers: true,
  mcp_host: true,
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe("StatusPage", () => {
  it("renders the figures an operator came for", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({ ok: true, json: async () => sample }),
    )
    renderPage()

    // Bytes are shown in the unit a person would quote, not raw.
    await waitFor(() => expect(screen.getByText("63.0 MB")).toBeTruthy())
    expect(screen.getByText("1h1m1s")).toBeTruthy()
    expect(screen.getByText("7")).toBeTruthy() // assistants
    expect(screen.getByText("4242")).toBeTruthy() // pid
    expect(screen.getByText("0.5.0+abc")).toBeTruthy()
  })

  it("names the host in one line, and falls back when it has no name", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({ ok: true, json: async () => sample }),
    )
    const { unmount } = renderPage()
    await waitFor(() =>
      expect(screen.getByText("Ubuntu 24.04.4 LTS on amd64")).toBeTruthy(),
    )
    expect(screen.getByText("go1.27.1")).toBeTruthy()
    unmount()

    // A host that publishes no pretty name still has to produce a usable line
    // rather than " on amd64".
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: true,
        json: async () => ({ ...sample, os_name: "" }),
      }),
    )
    renderPage()
    await waitFor(() => expect(screen.getByText("linux on amd64")).toBeTruthy())
  })

  it("leaves the allocator out of it", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({ ok: true, json: async () => sample }),
    )
    renderPage()
    // The Go heap is a subset of RSS and a diagnostic detail; this page answers
    // "how big is it", so the memory tile carries no second figure.
    await waitFor(() => expect(screen.getByText("63.0 MB")).toBeTruthy())
    expect(screen.getByTestId("status-memory").textContent).toBe(
      "pages.status.memory63.0 MB",
    )
  })

  it("reports a failure instead of rendering zeroes", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValue({ ok: false, status: 500, json: async () => ({}) }),
    )
    renderPage()

    // A status page that silently shows 0 MB and 0 assistants would read as a
    // dead instance rather than a failed request.
    await waitFor(() =>
      expect(screen.getByText("pages.status.load_error")).toBeTruthy(),
    )
    expect(screen.queryByTestId("status-grid")).toBeNull()
  })

  it("shows a dash rather than 0 B when a figure is unavailable", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: true,
        json: async () => ({ ...sample, memory_bytes: 0 }),
      }),
    )
    renderPage()
    // RSS is unreadable on a platform without /proc; "0 B" would be a lie.
    await waitFor(() => expect(screen.getByText("—")).toBeTruthy())
  })
})
