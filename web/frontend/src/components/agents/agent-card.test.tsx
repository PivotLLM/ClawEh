import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { fireEvent, render, screen } from "@testing-library/react"
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
  onCharacterCardChange: () => {},
  onCCv3Change: () => {},
  onCogmemChange: () => {},
  onSummarizationModelsChange: () => {},
  onSetDefaultBinding: () => {},
}

// Settings that do nothing for an agent that represents a person.
const personIgnores = [
  "Allow forum",
  "Allow character card tools",
  "Character card",
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

describe("AgentCard character card", () => {
  it("warns when the saved card is not in the library", () => {
    renderCard([], {
      ...allSettings,
      characterCard: "ghost",
      availableCards: [{ id: "alice", name: "Alice" }],
    })
    const note = screen.getByTestId("character-card-missing")
    expect(note.textContent).toContain(
      "This card does not exist in the library.",
    )
    expect(note.querySelector("a")?.getAttribute("href")).toBe("/agent/cards")
    expect(screen.queryByText("ghost (missing)")).not.toBeNull()
  })

  it("shows no warning for a card the library has", () => {
    renderCard([], {
      ...allSettings,
      characterCard: "alice",
      availableCards: [{ id: "alice", name: "Alice" }],
    })
    expect(screen.queryByTestId("character-card-missing")).toBeNull()
    expect(screen.queryByText("Alice (alice)")).not.toBeNull()
  })

  it("shows no warning when no card is set", () => {
    renderCard([], { ...allSettings, characterCard: "" })
    expect(screen.queryByTestId("character-card-missing")).toBeNull()
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

  // An agent has only the tools its list names: an empty list is labelled
  // "No tools", and "Default tools" writes the standard set into the list,
  // keeping the shell switch's setting.
  const catalog = {
    tools: [
      { name: "file_read", description: "" },
      { name: "msg_send", description: "" },
      { name: "web_fetch", description: "" },
    ],
    default_tools: ["file_read", "msg_send"],
  }

  it("labels an agent with no tools", () => {
    renderCard([], { availableTools: catalog, tools: [] })
    expect(screen.queryByText("Internal tools: No tools")).not.toBeNull()
  })

  it("Default tools writes the standard set into the list", () => {
    const onToolsChange = vi.fn()
    renderCard([], { availableTools: catalog, tools: [], onToolsChange })
    fireEvent.click(screen.getByRole("button", { name: "Default tools" }))
    expect(onToolsChange).toHaveBeenCalledWith(["file_read", "msg_send"])
  })

  it("Default tools keeps shell commands allowed", () => {
    const onToolsChange = vi.fn()
    renderCard([], {
      availableTools: catalog,
      tools: ["web_fetch", "shell_exec"],
      onToolsChange,
    })
    fireEvent.click(screen.getByRole("button", { name: "Default tools" }))
    expect(onToolsChange).toHaveBeenCalledWith([
      "file_read",
      "msg_send",
      "shell_exec",
    ])
  })

  it("asks before deleting an agent", () => {
    const onDelete = vi.fn()
    renderCard([], { name: "Alice", onDelete })
    fireEvent.click(screen.getByRole("button", { name: "Delete agent alice" }))
    expect(onDelete).not.toHaveBeenCalled()
    expect(screen.getByText("Delete Alice?")).toBeTruthy()
    expect(screen.getByText("Its files stay on disk.")).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }))
    expect(onDelete).not.toHaveBeenCalled()
    expect(screen.queryByText("Delete Alice?")).toBeNull()

    fireEvent.click(screen.getByRole("button", { name: "Delete agent alice" }))
    fireEvent.click(screen.getByRole("button", { name: "Delete" }))
    expect(onDelete).toHaveBeenCalledTimes(1)
  })
})

describe("AgentCard May message", () => {
  const agents = [
    { id: "alice", name: "Alice" },
    { id: "Bob" },
    { id: "carol", name: "Carol" },
  ]
  const mayMessage = (
    extra: Partial<React.ComponentProps<typeof AgentCard>> = {},
  ) => {
    const onAllowAgentsChange = vi.fn()
    renderCard([], {
      name: "Alice",
      configuredAgents: agents,
      onAllowAgentsChange,
      ...extra,
    })
    return onAllowAgentsChange
  }
  const box = (label: string) =>
    screen.getByRole("checkbox", { name: label }) as HTMLElement

  it("lists the other agents by name, else id, with the saved ones ticked", () => {
    mayMessage({ allowAgents: ["bob"] })
    expect(screen.queryByText("May message")).not.toBeNull()
    expect(screen.queryByRole("checkbox", { name: "Alice" })).toBeNull()
    expect(box("Bob").getAttribute("aria-checked")).toBe("true")
    expect(box("Carol").getAttribute("aria-checked")).toBe("false")
    expect(box("All agents").getAttribute("aria-checked")).toBe("false")
  })

  it("saves a ticked agent by its configured id and keeps the rest", () => {
    const onChange = mayMessage({ allowAgents: ["BOB"] })
    fireEvent.click(box("Carol"))
    expect(onChange).toHaveBeenLastCalledWith(["BOB", "carol"])
    fireEvent.click(box("Bob"))
    expect(onChange).toHaveBeenLastCalledWith(["carol"])
  })

  it("All agents writes [\"*\"] and ticks every agent", () => {
    const onChange = mayMessage({ allowAgents: ["bob"] })
    fireEvent.click(box("All agents"))
    expect(onChange).toHaveBeenLastCalledWith(["*"])
    expect(box("Carol").getAttribute("aria-checked")).toBe("true")
  })

  it("keeps an entry that names no other agent visible and removable", () => {
    const onChange = mayMessage({ allowAgents: ["dave", "bob"] })
    expect(box("dave").getAttribute("aria-checked")).toBe("true")
    expect(screen.queryByText("(not another configured agent)")).not.toBeNull()
    fireEvent.click(box("dave"))
    expect(onChange).toHaveBeenLastCalledWith(["bob"])
  })

  it("says the agent can message no one when the tool is granted", () => {
    mayMessage({ tools: ["agent_message"], allowAgents: [] })
    expect(screen.getByTestId("may-message-note").textContent).toBe(
      "Alice can't message any agent yet.",
    )
  })

  it("says the list has no effect without the tool", () => {
    mayMessage({ tools: ["file_read"], allowAgents: ["bob"] })
    expect(screen.getByTestId("may-message-note").textContent).toBe(
      "No effect until agent_message is ticked above.",
    )
  })

  it("treats a denied agent_message as not granted", () => {
    mayMessage({
      tools: ["*"],
      denyTools: ["agent_*"],
      allowAgents: ["bob"],
    })
    expect(screen.getByTestId("may-message-note").textContent).toBe(
      "No effect until agent_message is ticked above.",
    )
  })

  it("shows no no-effect note when agent_spawn reads the list", () => {
    mayMessage({ tools: ["agent_spawn"], allowAgents: ["bob"] })
    expect(screen.queryByTestId("may-message-note")).toBeNull()
  })

  it("shows no no-effect note when the forum reads the list", () => {
    mayMessage({ forum: true, allowAgents: ["bob"] })
    expect(screen.queryByTestId("may-message-note")).toBeNull()
  })

  it("shows no note when the tool and the list agree", () => {
    mayMessage({ tools: ["agent_message"], allowAgents: ["bob"] })
    expect(screen.queryByTestId("may-message-note")).toBeNull()
  })
})
