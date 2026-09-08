import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"

import type { CLIInfo } from "@/api/system"

import { CLIAgentsSection } from "./cli-agents-section"

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (key: string, opts?: Record<string, unknown>) =>
      opts ? `${key}:${JSON.stringify(opts)}` : key,
  }),
}))

const setCLIEnabled = vi.fn()
const listCLIs = vi.fn()
vi.mock("@/api/system", async () => ({
  listCLIs: (...a: unknown[]) => listCLIs(...a),
  setCLIEnabled: (...a: unknown[]) => setCLIEnabled(...a),
}))

function cli(over: Partial<CLIInfo> = {}): CLIInfo {
  return {
    protocol: "antigravity-cli",
    label: "Antigravity CLI",
    binary: "agy",
    installed: true,
    enabled: false,
    configured: false,
    provider_index: -1,
    models: 0,
    models_enabled: 0,
    required_args: ["--dangerously-skip-permissions"],
    ...over,
  }
}

function renderSection(rows: CLIInfo[]) {
  listCLIs.mockResolvedValue(rows)
  setCLIEnabled.mockResolvedValue(undefined)
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <CLIAgentsSection providers={[]} onEdit={() => {}} />
    </QueryClientProvider>,
  )
}

describe("CLIAgentsSection", () => {
  it("switches a CLI on with one click and no fields", async () => {
    renderSection([cli()])
    fireEvent.click(await screen.findByTestId("cli-switch-antigravity-cli"))
    await waitFor(() =>
      expect(setCLIEnabled).toHaveBeenCalledWith("antigravity-cli", true),
    )
  })

  it("lists a CLI whose binary is missing, greyed and unswitchable", async () => {
    // Hiding it would look like ClawEh does not support the CLI, when the truth
    // is that the host does not have it.
    renderSection([cli({ installed: false })])
    const row = await screen.findByTestId("cli-row-antigravity-cli")
    expect(row.className).toContain("opacity-55")
    expect(
      screen.getByTestId("cli-switch-antigravity-cli").hasAttribute("disabled"),
    ).toBe(true)
    expect(screen.getByText(/notFound/)).toBeTruthy()
  })

  it("shows the resolved path once installed", async () => {
    renderSection([cli({ path: "/home/eric/.local/bin/agy" })])
    expect(
      await screen.findByText("/home/eric/.local/bin/agy"),
    ).toBeTruthy()
  })

  it("says how many models a switch governs when it is more than one", async () => {
    // Turning the CLI off disables all of them, so the count must be visible
    // before the switch is flipped, not after.
    renderSection([
      cli({ protocol: "claude-cli", configured: true, models: 3, models_enabled: 2, enabled: true }),
    ])
    expect(await screen.findByText(/modelCount/)).toBeTruthy()
  })

  it("does not clutter a single-model row with a count", async () => {
    renderSection([cli({ configured: true, models: 1, models_enabled: 1, enabled: true })])
    await screen.findByTestId("cli-row-antigravity-cli")
    expect(screen.queryByText(/modelCount/)).toBeNull()
  })

  it("shows the flags it will run with, so nobody has to guess", async () => {
    // These auto-approve tool use. Someone deciding whether to switch a CLI on
    // is entitled to read them here rather than find them in a process listing.
    renderSection([
      cli({ required_args: ["--yolo"], extra_args: ["--verbose"] }),
    ])
    const row = await screen.findByTestId("cli-row-antigravity-cli")
    expect(row.textContent).toContain("--yolo --verbose")
  })

  it("offers editing only once a provider exists", async () => {
    const { unmount } = renderSection([cli()])
    await screen.findByTestId("cli-row-antigravity-cli")
    expect(screen.queryByTestId("cli-edit-antigravity-cli")).toBeNull()
    unmount()

    renderSection([cli({ configured: true, provider_index: 3 })])
    expect(await screen.findByTestId("cli-edit-antigravity-cli")).toBeTruthy()
  })

  it("surfaces a failure rather than silently leaving the switch unchanged", async () => {
    listCLIs.mockResolvedValue([cli()])
    setCLIEnabled.mockRejectedValue(new Error("config is read-only"))
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    render(
      <QueryClientProvider client={qc}>
        <CLIAgentsSection providers={[]} onEdit={() => {}} />
      </QueryClientProvider>,
    )
    fireEvent.click(await screen.findByTestId("cli-switch-antigravity-cli"))
    expect(await screen.findByText("config is read-only")).toBeTruthy()
  })
})
