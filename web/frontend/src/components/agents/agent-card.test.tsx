import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"

import { AgentCard } from "./agent-card"

// t resolves keys against the English strings, so the tests read what an
// operator sees.
vi.mock("react-i18next", async () => {
  const en = (await import("@/i18n/locales/en.json")).default as Record<
    string,
    unknown
  >
  const t = (key: string, opts?: Record<string, unknown>) => {
    const value = key
      .split(".")
      .reduce<unknown>(
        (o, k) => (o as Record<string, unknown> | undefined)?.[k],
        en,
      )
    if (typeof value !== "string") return key
    return value.replace(/\{\{(\w+)\}\}/g, (_, name: string) =>
      String(opts?.[name] ?? ""),
    )
  }
  return { useTranslation: () => ({ t }) }
})

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to }: { children: React.ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))

vi.mock("./message-tokens-section", () => ({
  MessageTokensSection: () => null,
}))

function renderCard(
  ignoredMounts: string[],
  extra: Partial<React.ComponentProps<typeof AgentCard>> = {},
) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <AgentCard
        label="alice"
        enabled
        selectedModels={[]}
        skills={[]}
        tools={[]}
        availableSkills={[]}
        availableTools={{ tools: [], default_tools: [] }}
        models={[]}
        mounts={[
          { name: "files", path: "/srv/a", notify: false, writable: false },
          { name: "notes", path: "/srv/b", notify: false, writable: false },
        ]}
        onMountsChange={() => {}}
        ignoredMounts={ignoredMounts}
        onModelsChange={() => {}}
        onSkillsChange={() => {}}
        onToolsChange={() => {}}
        {...extra}
      />
    </QueryClientProvider>,
  )
}

// Every switch and setting the card can show, wired so each one renders.
const allSettings: Partial<React.ComponentProps<typeof AgentCard>> = {
  onMessageChange: () => {},
  onTemperatureChange: () => {},
  onEventRetentionDaysChange: () => {},
  onRetiredRetentionDaysChange: () => {},
  onShareCommonChange: () => {},
  onGlobalCronChange: () => {},
  onMaestroChange: () => {},
  onFusionChange: () => {},
  onForumChange: () => {},
  onCogmemChange: () => {},
  onSummarizationModelsChange: () => {},
  onSetDefaultBinding: () => {},
}

// Settings that do nothing for an agent that represents a person.
const personIgnores = [
  "Allow forum",
  "Fusion (REST-API tools)",
  "Maestro",
  "Global cron",
  "Cognitive memory",
  "Share common directory",
  "Temperature",
  "Event memory retention (days)",
  "Retired memory retention (days)",
  "Rotating Tokens",
  "Mounts (external folders, beside files/)",
  "Memory models (optional)",
]

describe("AgentCard mounts", () => {
  it("marks a mount ignored for a reserved name in its row", () => {
    renderCard(["files"])
    const notes = screen.getAllByTestId("ignored-mount")
    expect(notes).toHaveLength(1)
    expect(notes[0].textContent).toBe('Ignored: "files" is a reserved name.')
  })

  it("shows no note when no mount is ignored", () => {
    renderCard([])
    expect(screen.queryAllByTestId("ignored-mount")).toHaveLength(0)
  })
})

describe("AgentCard for a person", () => {
  it("shows every setting for an ordinary agent", () => {
    renderCard([], allSettings)
    const missing = personIgnores.filter((l) => screen.queryByText(l) === null)
    expect(missing).toEqual([])
  })

  it("hides the settings that have no effect for a person", () => {
    renderCard([], { ...allSettings, human: true })
    const shown = personIgnores.filter((l) => screen.queryByText(l) !== null)
    expect(shown).toEqual([])
    expect(
      screen.queryByText("Represents a person: answers are typed by hand."),
    ).not.toBeNull()
    // The default channel is where the person is asked: it stays.
    expect(screen.queryByText("Default Channel")).not.toBeNull()
  })

  it("names the page a human-agent note links to", () => {
    renderCard([], {
      human: true,
      humanNotes: [
        {
          agent: "bob",
          kind: "not_running",
          message: "Bob has no chat.",
          link: "/channels",
        },
      ],
    })
    const note = screen.getByTestId("human-agent-problem")
    expect(note.textContent).toBe("Not running: Bob has no chat. Channels")
    expect(note.querySelector("a")?.getAttribute("href")).toBe("/channels")
  })
})
