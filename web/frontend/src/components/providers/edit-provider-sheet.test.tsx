import { fireEvent, render, screen } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"

import type { ProviderInfo } from "@/api/providers"

import { EditProviderSheet } from "./edit-provider-sheet"

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}))

vi.mock("@/api/providers", async () => ({
  updateProvider: vi.fn(),
}))

function provider(over: Partial<ProviderInfo> = {}): ProviderInfo {
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

// The sheet syncs its form during render, on the transition from one provider
// to another, so it has to be mounted empty and then handed a provider — which
// is also how the page uses it: it mounts once with null and sets `editing`
// when a card is clicked.
function renderSheet(over: Partial<ProviderInfo> = {}) {
  const props = { open: true, onClose: () => {}, onSaved: () => {} }
  const view = render(<EditProviderSheet provider={null} {...props} />)
  view.rerender(<EditProviderSheet provider={provider(over)} {...props} />)
  return view
}

// Every one of these is an HTTP wire knob and the CLI factory reads none of
// them: a CLI provider is built from its command, workspace, arguments and
// environment alone. Shown on a CLI form they were five controls that did
// nothing, and an off switch reads as a feature available but disabled — which
// is how response_format_json came to look like the reason a CLI was not
// returning JSON. (It always does; --output-format json is in the argv.)
const HTTP_ONLY = [
  "providers.field.proxy",
  "providers.field.strictCompat",
  "providers.field.requireReasoningContent",
  "providers.field.noParallelToolCalls",
  "providers.field.responseFormatJSON",
]

// ADVANCED_TOGGLE opens the disclosure the knobs live in. Asserting on the
// toggle itself is what proves they are gone rather than merely collapsed.
const ADVANCED_TOGGLE = "models.advanced.toggle"

describe("EditProviderSheet", () => {
  it("gives a CLI provider no advanced section at all", () => {
    renderSheet({ protocol: "claude-cli", command: "/usr/bin/claude" })
    expect(screen.queryByText(ADVANCED_TOGGLE)).toBeNull()
    for (const label of HTTP_ONLY) {
      expect(screen.queryByText(label)).toBeNull()
    }
    // The command is what a CLI provider actually has, and it stays.
    expect(screen.getByText("providers.field.command")).toBeTruthy()
  })

  it("treats the deprecated gemini-cli alias as a CLI provider", () => {
    // An upgraded config still naming gemini-cli must not fall back to the
    // HTTP form and offer knobs its factory ignores.
    renderSheet({ protocol: "gemini-cli" })
    expect(screen.queryByText(ADVANCED_TOGGLE)).toBeNull()
    expect(screen.getByText("providers.field.command")).toBeTruthy()
  })

  it("still offers them for an HTTP provider", () => {
    renderSheet({ protocol: "openai-chat", base_url: "https://api.example/v1" })
    fireEvent.click(screen.getByText(ADVANCED_TOGGLE))
    for (const label of HTTP_ONLY) {
      expect(screen.getByText(label)).toBeTruthy()
    }
  })
})
