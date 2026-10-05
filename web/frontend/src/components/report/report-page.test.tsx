import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import { SidebarProvider } from "@/components/ui/sidebar"

import { ReportPage } from "./report-page"

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (key: string) => key,
  }),
}))

function renderPage() {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  // SidebarProvider because PageHeader reads the sidebar context.
  return render(
    <QueryClientProvider client={qc}>
      <SidebarProvider>
        <ReportPage />
      </SidebarProvider>
    </QueryClientProvider>,
  )
}

const sample = {
  identity: {
    name: "ClawEh",
    version: "0.6.0+abcdef12",
    build: "2026-09-21T12:00:00+0000",
    platform: "linux/amd64 on testbox",
    generated_at: "2026-09-21T12:00:00Z",
  },
  assessment: [
    {
      action: true,
      item: "Channels accepting any sender",
      status: "telegram-bob (allow_from contains *).",
    },
    {
      action: false,
      item: "Audit log",
      status: "Audit log at /tmp/audit.db (90-day retention).",
    },
  ],
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe("ReportPage", () => {
  it("renders the identity line, the assessment rows and the PDF button", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({ ok: true, json: async () => sample }),
    )
    renderPage()

    await waitFor(() =>
      expect(screen.getByTestId("report-identity").textContent).toBe(
        "ClawEh 0.6.0+abcdef12 · pages.report.build 2026-09-21T12:00:00+0000 · linux/amd64 on testbox",
      ),
    )
    expect(screen.getByText("pages.report.col.action")).toBeTruthy()
    expect(screen.getByText("pages.report.col.item")).toBeTruthy()
    expect(screen.getByText("pages.report.col.status")).toBeTruthy()

    const rows = screen.getAllByTestId("report-row")
    expect(rows).toHaveLength(2)
    // The action row is marked and visually distinct; the awareness row is plain.
    expect(rows[0].getAttribute("data-action")).toBe("true")
    expect(rows[0].textContent).toContain("Channels accepting any sender")
    expect(screen.getByLabelText("pages.report.action_needed")).toBeTruthy()
    expect(rows[1].getAttribute("data-action")).toBeNull()
    expect(rows[1].textContent).toContain(
      "Audit log at /tmp/audit.db (90-day retention).",
    )

    const link = screen.getByTestId("report-download")
    expect(link.getAttribute("href")).toBe("/api/report/pdf")
    expect(link.textContent).toContain("pages.report.download")
  })

  it("omits the build segment when the binary is unstamped", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: true,
        json: async () => ({
          ...sample,
          identity: { ...sample.identity, build: "" },
        }),
      }),
    )
    renderPage()
    await waitFor(() =>
      expect(screen.getByTestId("report-identity").textContent).toBe(
        "ClawEh 0.6.0+abcdef12 · linux/amd64 on testbox",
      ),
    )
  })

  it("shows an error when the assessment cannot be loaded", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValue({ ok: false, status: 500, json: async () => ({}) }),
    )
    renderPage()
    await waitFor(() =>
      expect(screen.getByText("pages.report.load_error")).toBeTruthy(),
    )
    expect(screen.queryByTestId("report-download")).toBeNull()
  })
})
