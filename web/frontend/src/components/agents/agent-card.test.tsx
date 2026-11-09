import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import {
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react"
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
        allowedFolders={[
          { path: "/srv/src", notify: true },
          { path: "/srv/ref", notify: false },
        ]}
        onAllowedFoldersChange={() => {}}
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
  onClassifiersChange: () => {},
  onTaskModelGrantChange: () => {},
  onFusionChange: () => {},
  onForumChange: () => {},
  onCharacterCardChange: () => {},
  onCCv3Change: () => {},
  onCogmemChange: () => {},
  onSummarizationModelsChange: () => {},
  onSubagentModelsChange: () => {},
  onSetDefaultBinding: () => {},
}

// Settings that do nothing for an agent that represents a person.
const personIgnores = [
  "Allow classifiers",
  "Allow task models",
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
  "Allowed folders",
  "Memory models (optional)",
  "Sub-Agent models (tried in order)",
]

describe("AgentCard allowed folders", () => {
  it("shows one row per folder with its path and notify box, and no name or write control", () => {
    renderCard()
    expect(screen.getByText("Allowed folders")).toBeTruthy()
    expect(
      screen.getByText(
        "File tools can use these folders. Write access follows file permissions.",
      ),
    ).toBeTruthy()
    const paths = screen.getAllByLabelText("Folder path") as HTMLInputElement[]
    expect(paths.map((p) => p.value)).toEqual(["/srv/src", "/srv/ref"])
    const notify = screen.getAllByRole("checkbox", {
      name: "Notify on new files",
    })
    expect(notify.map((c) => c.getAttribute("aria-checked"))).toEqual([
      "true",
      "false",
    ])
    expect(screen.queryByText(/write/i, { selector: "label" })).toBeNull()
  })

  it("adds an empty row and removes a row", () => {
    const onChange = vi.fn()
    renderCard({ onAllowedFoldersChange: onChange })
    fireEvent.click(screen.getByText("Add folder"))
    expect(onChange).toHaveBeenLastCalledWith([
      { path: "/srv/src", notify: true },
      { path: "/srv/ref", notify: false },
      { path: "", notify: false },
    ])
    fireEvent.click(screen.getAllByLabelText("Remove folder")[0])
    expect(onChange).toHaveBeenLastCalledWith([
      { path: "/srv/ref", notify: false },
    ])
  })
})

describe("AgentCard character card", () => {
  it("warns when the saved card is not in the library", () => {
    renderCard({
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
    renderCard({
      ...allSettings,
      characterCard: "alice",
      availableCards: [{ id: "alice", name: "Alice" }],
    })
    expect(screen.queryByTestId("character-card-missing")).toBeNull()
    expect(screen.queryByText("Alice (alice)")).not.toBeNull()
  })

  it("shows no warning when no card is set", () => {
    renderCard({ ...allSettings, characterCard: "" })
    expect(screen.queryByTestId("character-card-missing")).toBeNull()
  })
})

describe("AgentCard classifiers", () => {
  const withSwitch = (
    classifiers: boolean,
    classifierModel: string,
  ): Partial<React.ComponentProps<typeof AgentCard>> => ({
    ...allSettings,
    classifiers,
    classifierModel,
    classifierModels: ["fast", "slow"],
    onClassifierModelChange: () => {},
  })

  it("shows the model selector only while the switch is on", () => {
    renderCard(withSwitch(false, ""))
    expect(screen.queryByText("Classifier model")).toBeNull()
  })

  it("shows the model with no warning when the agent can use it", () => {
    renderCard(withSwitch(true, "fast"))
    expect(screen.queryByText("Classifier model")).not.toBeNull()
    expect(screen.queryByTestId("classifier-model-unusable")).toBeNull()
  })

  it("hands the switch over to the page, on and off", () => {
    const changes: boolean[] = []
    for (const on of [false, true]) {
      renderCard({
        ...withSwitch(on, on ? "fast" : ""),
        onClassifiersChange: (v) => changes.push(v),
      })
      fireEvent.click(screen.getByRole("switch", { name: "Allow classifiers" }))
      cleanup()
    }
    expect(changes).toEqual([true, false])
  })

  it("shows no classifier settings without a handler", () => {
    renderCard({
      ...withSwitch(true, "fast"),
      onClassifiersChange: undefined,
    })
    expect(screen.queryByText("Allow classifiers")).toBeNull()
    expect(screen.queryByText("Classifier model")).toBeNull()
  })

  it("marks a model that is not a Classify model where it is set", () => {
    renderCard(withSwitch(true, "gone"))
    expect(screen.getByTestId("classifier-model-unusable").textContent).toBe(
      "Not a Classify model, so its classifiers fail. Pick another.",
    )
    expect(screen.queryByText("gone (not a Classify model)")).not.toBeNull()
    expect(screen.queryByTestId("classifier-no-classify-models")).toBeNull()
  })

  it("links to Task models when no model is ticked Classify", () => {
    renderCard({ ...withSwitch(false, ""), classifierModels: [] })
    const note = screen.getByTestId("classifier-no-classify-models")
    expect(note.textContent).toBe(
      "No model is ticked Classify. Tick one on Task models.",
    )
    expect(note.querySelector("a")?.getAttribute("href")).toBe("/task-models")
  })
})

describe("AgentCard for a person", () => {
  it("shows every setting for an ordinary agent", () => {
    renderCard(allSettings)
    const missing = personIgnores.filter((l) => screen.queryByText(l) === null)
    expect(missing).toEqual([])
  })

  it("hides the settings that have no effect for a person", () => {
    renderCard({ ...allSettings, human: true })
    const shown = personIgnores.filter((l) => screen.queryByText(l) !== null)
    expect(shown).toEqual([])
    expect(
      screen.queryByText("Represents a person: answers are typed by hand."),
    ).not.toBeNull()
    // The default channel is where the person is asked: it stays.
    expect(screen.queryByText("Default Channel")).not.toBeNull()
  })

  it("names the page a human-agent note links to", () => {
    renderCard({
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
    renderCard({ availableTools: catalog, tools: [] })
    expect(screen.queryByText("Internal tools: No tools")).not.toBeNull()
  })

  it("Default tools writes the standard set into the list", () => {
    const onToolsChange = vi.fn()
    renderCard({ availableTools: catalog, tools: [], onToolsChange })
    fireEvent.click(screen.getByRole("button", { name: "Default tools" }))
    expect(onToolsChange).toHaveBeenCalledWith(["file_read", "msg_send"])
  })

  it("Default tools keeps shell commands allowed", () => {
    const onToolsChange = vi.fn()
    renderCard({
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
    renderCard({ name: "Alice", onDelete })
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
    renderCard({
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

  it('All agents writes ["*"] and ticks every agent', () => {
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

describe("AgentCard Sub-Agent models", () => {
  const model = (name: string, index: number) =>
    ({
      index,
      model_name: name,
      model: name,
      provider: "p",
      enabled: true,
      configured: true,
      is_default: false,
    }) as React.ComponentProps<typeof AgentCard>["models"][number]
  const models = [model("fast", 0), model("slow", 1)]

  it("lists the saved worker models in order and says they are exclusive", () => {
    renderCard({
      models,
      subagentModels: ["slow", "fast"],
      onSubagentModelsChange: () => {},
    })
    const section = screen
      .getByText("Sub-Agent models (tried in order)")
      .closest("div")!
    const rows = [...section.querySelectorAll(".font-mono")].map(
      (el) => el.textContent,
    )
    expect(rows).toEqual(["slow", "fast"])
    expect(
      screen.queryByText(
        "Sub-agents this agent starts run on these models and no others.",
      ),
    ).not.toBeNull()
    expect(screen.queryByText("Sub-agents use the models above.")).toBeNull()
  })

  it("says workers use the agent's own models when the list is empty", () => {
    renderCard({ models, onSubagentModelsChange: () => {} })
    expect(
      screen.queryByText("Sub-agents use the models above."),
    ).not.toBeNull()
    expect(screen.queryByText("Add sub-agent model…")).not.toBeNull()
  })

  it("adding a model reports the new list", () => {
    const onChange = vi.fn()
    renderCard({
      models,
      subagentModels: ["fast"],
      onSubagentModelsChange: onChange,
    })
    const section = screen
      .getByText("Sub-Agent models (tried in order)")
      .closest("div")!
    const add = within(section).getByRole("combobox")
    fireEvent.keyDown(add, { key: "ArrowDown" })
    fireEvent.click(screen.getByRole("option", { name: "slow" }))
    expect(onChange).toHaveBeenCalledWith(["fast", "slow"])
  })

  it("removing a model reports the list without it", () => {
    const onChange = vi.fn()
    renderCard({
      models,
      subagentModels: ["fast", "slow"],
      onSubagentModelsChange: onChange,
    })
    const section = screen
      .getByText("Sub-Agent models (tried in order)")
      .closest("div")!
    fireEvent.click(within(section).getAllByTitle("Remove")[0])
    expect(onChange).toHaveBeenCalledWith(["slow"])
  })
})

describe("AgentCard task models", () => {
  it("ticks the granted kinds and hands a change to the page", () => {
    const changes: [string, boolean][] = []
    renderCard({
      ...allSettings,
      taskModelGrant: { image: true },
      taskKindsWithModels: ["text", "image", "tts", "stt"],
      onTaskModelGrantChange: (kind, on) => changes.push([kind, on]),
    })
    const images = screen.getByLabelText("Allow task models: Images")
    expect(images.getAttribute("data-state")).toBe("checked")
    const text = screen.getByLabelText("Allow task models: Text")
    expect(text.getAttribute("data-state")).toBe("unchecked")
    fireEvent.click(text)
    fireEvent.click(images)
    expect(changes).toEqual([
      ["text", true],
      ["image", false],
    ])
    expect(screen.queryByTestId("task-grant-no-model-text")).toBeNull()
  })

  it("links to Task models for a kind with no model ticked", () => {
    renderCard({ ...allSettings, taskKindsWithModels: ["text"] })
    expect(screen.queryByTestId("task-grant-no-model-text")).toBeNull()
    const note = screen.getByTestId("task-grant-no-model-stt")
    expect(note.textContent).toBe(
      "No model is ticked for this. Tick one on Task models.",
    )
    expect(note.querySelector("a")?.getAttribute("href")).toBe("/task-models")
  })
})

describe("AgentCard task models and agent_spawn", () => {
  it("says the boxes need agent_spawn when the agent lacks it", () => {
    renderCard({ ...allSettings, taskKindsWithModels: ["text"] })
    expect(screen.getByTestId("task-models-no-spawn").textContent).toBe(
      "Needs the agent_spawn tool.",
    )
  })

  it("says nothing when the agent has agent_spawn", () => {
    renderCard({
      ...allSettings,
      tools: ["agent_spawn"],
      taskKindsWithModels: ["text"],
    })
    expect(screen.queryByTestId("task-models-no-spawn")).toBeNull()
  })

  it("says it again when agent_spawn is denied", () => {
    renderCard({
      ...allSettings,
      tools: ["agent_spawn"],
      denyTools: ["agent_*"],
    })
    expect(screen.queryByTestId("task-models-no-spawn")).not.toBeNull()
  })
})

describe("AgentCard task models while saving", () => {
  it("disables the boxes while a grant change is saved", () => {
    const changes: string[] = []
    renderCard({
      ...allSettings,
      taskModelsSaving: true,
      onTaskModelGrantChange: (kind) => changes.push(kind),
    })
    for (const label of [
      "Text",
      "Images",
      "Speech (TTS)",
      "Transcription (STT)",
    ]) {
      const box = screen.getByLabelText(`Allow task models: ${label}`)
      expect(box.hasAttribute("disabled")).toBe(true)
      fireEvent.click(box)
    }
    expect(changes).toEqual([])
  })

  it("enables them again once saved", () => {
    renderCard({ ...allSettings, taskModelsSaving: false })
    expect(
      screen
        .getByLabelText("Allow task models: Images")
        .hasAttribute("disabled"),
    ).toBe(false)
  })
})
