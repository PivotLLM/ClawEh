import { render, screen } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"

import type { ProviderInfo } from "@/api/providers"

import { ProviderCard } from "./provider-card"

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}))

function makeProvider(over: Partial<ProviderInfo> = {}): ProviderInfo {
  return {
    index: 0,
    name: "Test",
    protocol: "openai-chat",
    api_key: "",
    ready: false,
    model_count: 0,
    ...over,
  }
}

function renderCard(over: Partial<ProviderInfo> = {}) {
  return render(
    <ProviderCard
      provider={makeProvider(over)}
      onEdit={() => {}}
      onDelete={() => {}}
    />,
  )
}

describe("ProviderCard", () => {
  // The card used to decide readiness itself, as `command !== ""` for a CLI
  // provider. That was wrong in both directions, so the whole point of these
  // tests is that it now reports what the backend resolved and nothing else.
  it("labels a CLI provider whose binary resolves, with no path set", () => {
    renderCard({
      protocol: "antigravity-cli",
      ready: true,
      resolved_command: "/home/eric/bin/agy",
    })
    expect(screen.getByText("providers.status.configured")).toBeTruthy()
    // Blank command, but the card still says which binary will run.
    expect(screen.getByText("/home/eric/bin/agy")).toBeTruthy()
  })

  it("does not call a CLI provider configured because a stale path is set", () => {
    renderCard({
      protocol: "claude-cli",
      command: "/gone/bin/claude",
      ready: false,
    })
    expect(screen.getByText("providers.status.unconfigured")).toBeTruthy()
  })

  it("shows the key icon only for providers that have a key", () => {
    // The deprecated gemini-cli alias must still render as a CLI card: no key,
    // so no key icon beside its Configured label.
    const cli = renderCard({ protocol: "gemini-cli", ready: true })
    expect(screen.getByText("providers.status.configured")).toBeTruthy()
    expect(cli.container.querySelector("svg.size-3")).toBeNull()
    cli.unmount()

    const http = renderCard({ protocol: "openai-chat", ready: true })
    expect(http.container.querySelector("svg.size-3")).toBeTruthy()
  })

  it("marks an HTTP provider unconfigured without a key", () => {
    renderCard({ protocol: "openai-chat", api_key: "" })
    expect(screen.getByText("providers.status.unconfigured")).toBeTruthy()
  })
})
