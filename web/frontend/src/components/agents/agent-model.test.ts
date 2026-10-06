import { describe, expect, it } from "vitest"

import {
  agentsPayload,
  applyMaestroEdits,
  maestroEditsFromAgent,
  maestroFromRaw,
  maestroPayload,
  cliBypassWarnings,
  fusionAccessView,
  mcpAccessEntries,
  mcpAccessView,
  parseAgentsConfig,
  toggleAccessEntry,
} from "./agent-model"

describe("maestro block", () => {
  it("parses the object form and drops invalid values", () => {
    expect(
      maestroFromRaw({
        enabled: true,
        max_concurrent: 3,
        rate_limit_requests: 0,
        rate_limit_period: "x",
        allow_parallel: false,
      }),
    ).toEqual({
      enabled: true,
      max_concurrent: 3,
      rate_limit_requests: undefined,
      rate_limit_period: undefined,
      allow_parallel: false,
    })
  })

  it("treats the retired boolean form as a disabled block", () => {
    expect(maestroFromRaw(true)).toEqual({ enabled: false })
    expect(maestroFromRaw(false)).toEqual({ enabled: false })
    expect(maestroFromRaw(undefined)).toBeUndefined()
    expect(maestroFromRaw(null)).toBeUndefined()
  })

  it("sends enabled always and the rest only when set", () => {
    expect(maestroPayload({ enabled: true })).toEqual({ enabled: true })
    expect(
      maestroPayload({
        enabled: false,
        max_concurrent: 2,
        rate_limit_requests: 4,
        rate_limit_period: 30,
        allow_parallel: false,
      }),
    ).toEqual({
      enabled: false,
      max_concurrent: 2,
      rate_limit_requests: 4,
      rate_limit_period: 30,
      allow_parallel: false,
    })
    // allow_parallel true is the default and stays implicit.
    expect(maestroPayload({ enabled: true, allow_parallel: true })).toEqual({
      enabled: true,
    })
  })

  it("round-trips runner edits into the block without touching enabled", () => {
    const agent = {
      id: "a",
      maestro: { enabled: true, max_concurrent: 2, allow_parallel: false },
    }
    const edits = maestroEditsFromAgent(agent)
    expect(edits).toEqual({
      maxConcurrent: 2,
      rateLimitRequests: undefined,
      rateLimitPeriod: undefined,
      allowParallel: false,
    })
    expect(
      applyMaestroEdits(agent.maestro, {
        ...edits,
        allowParallel: true,
        rateLimitPeriod: 90,
      }),
    ).toEqual({
      enabled: true,
      max_concurrent: 2,
      rate_limit_requests: undefined,
      rate_limit_period: 90,
      allow_parallel: undefined,
    })
    // No block: edits never create one (the enabled switch does).
    expect(applyMaestroEdits(undefined, edits)).toBeUndefined()
  })
})

describe("mcp access", () => {
  const servers = ["fusion", "GitHub"]

  it("checks configured servers case-insensitively", () => {
    expect(mcpAccessView(["Fusion"], servers)).toEqual([
      { name: "fusion", checked: true, configured: true },
      { name: "GitHub", checked: false, configured: true },
    ])
  })

  it("shows an entry that names no configured server checked and flagged", () => {
    const rows = mcpAccessView(["oldserver", "fusion_trello"], servers)
    expect(rows.slice(2)).toEqual([
      { name: "oldserver", checked: true, configured: false },
      { name: "fusion_trello", checked: true, configured: false },
    ])
  })

  it("drops blank entries and round-trips the rest", () => {
    const rows = mcpAccessView([" ", "fusion", "old"], servers)
    expect(mcpAccessEntries(rows)).toEqual(["fusion", "old"])
  })

  it("unchecking removes the entry", () => {
    const rows = mcpAccessView(["fusion", "github"], servers).map((s) =>
      s.name === "fusion" ? { ...s, checked: false } : s,
    )
    expect(mcpAccessEntries(rows)).toEqual(["GitHub"])
  })

  it("leaves entries owned by a Fusion service to the Fusion list", () => {
    const rows = mcpAccessView(
      ["github", "wxca", "microsoft365_calendar", "old"],
      servers,
      ["wxca", "microsoft365"],
    )
    expect(rows.slice(2)).toEqual([{ name: "old", checked: true, configured: false }])
  })
})

describe("fusion services", () => {
  const services = ["microsoft365", "wxca"]

  it("checks named services case-insensitively and ignores MCP entries", () => {
    expect(fusionAccessView(["WXCA", "github"], services)).toEqual([
      { name: "microsoft365", checked: false, configured: true },
      { name: "wxca", checked: true, configured: true },
    ])
  })

  it("shows a group within a service as its own unflagged row", () => {
    const rows = fusionAccessView(["microsoft365_calendar"], services)
    expect(rows).toEqual([
      { name: "microsoft365", checked: false, configured: true },
      { name: "wxca", checked: false, configured: true },
      { name: "microsoft365_calendar", checked: true, configured: true },
    ])
  })

  it("ticking a service adds it beside the MCP entries", () => {
    expect(toggleAccessEntry(["github"], "wxca")).toEqual(["github", "wxca"])
  })

  it("a name that is both a server and a service is one entry shown in both lists", () => {
    const servers = ["simpledoc", "github"]
    const both = ["simpledoc", ...services]
    let entries = ["simpledoc"]
    expect(mcpAccessView(entries, servers, both)[0]).toEqual({ name: "simpledoc", checked: true, configured: true })
    expect(fusionAccessView(entries, both)[0]).toEqual({ name: "simpledoc", checked: true, configured: true })
    // Unticking it in either list removes the one entry, so both rows clear.
    entries = toggleAccessEntry(entries, "simpledoc")
    expect(entries).toEqual([])
    expect(mcpAccessView(entries, servers, both)[0].checked).toBe(false)
    expect(fusionAccessView(entries, both)[0].checked).toBe(false)
    // Ticking it again restores both.
    entries = toggleAccessEntry(entries, "simpledoc")
    expect(fusionAccessView(entries, both)[0].checked).toBe(true)
  })

  it("toggling removes case-insensitively and never duplicates", () => {
    expect(toggleAccessEntry(["WXCA", "github"], "wxca")).toEqual(["github"])
    expect(toggleAccessEntry(["github"], "github")).toEqual([])
  })
})

describe("cli bypass warnings", () => {
  const models = [
    { model_name: "Claude CLI Opus", provider: "Claude CLI", extra_args: ["--dangerously-skip-permissions", "--no-chrome"] },
    { model_name: "Codex", provider: "Codex CLI", extra_args: ["--dangerously-bypass-approvals-and-sandbox"] },
    { model_name: "OR Flash", provider: "OpenRouter" },
  ]
  const providers = [
    { name: "Claude CLI", protocol: "claude-cli" },
    { name: "Codex CLI", protocol: "codex-cli", bypass_restrictions: true },
    { name: "OpenRouter", protocol: "openai-chat" },
  ]
  const clis = [
    { protocol: "claude-cli", bypass_args: ["--dangerously-skip-permissions"] },
    { protocol: "codex-cli", bypass_args: ["--dangerously-bypass-approvals-and-sandbox"] },
  ]

  it("flags a model whose bypass flag is dropped because the provider setting is off", () => {
    expect(cliBypassWarnings(["Claude CLI Opus", "Codex", "OR Flash"], [], models, providers, clis)).toEqual([
      { model: "Claude CLI Opus", provider: "Claude CLI", flag: "--dangerously-skip-permissions" },
    ])
  })

  it("falls back to the default chain when the agent has none", () => {
    expect(cliBypassWarnings([], ["Claude CLI Opus"], models, providers, clis)).toHaveLength(1)
    expect(cliBypassWarnings([], ["OR Flash"], models, providers, clis)).toEqual([])
  })

  it("is quiet for unknown models, HTTP providers and missing data", () => {
    expect(cliBypassWarnings(["Nope"], [], models, providers, clis)).toEqual([])
    expect(cliBypassWarnings(["Claude CLI Opus"], [], undefined, undefined, [])).toEqual([])
  })
})

describe("agentsPayload", () => {
  // Keys the Agents page has no control for. PATCH /api/config replaces
  // agents.list wholesale, so each of them must come back in the payload.
  const unedited = (id: string) => ({
    workspace: `/srv/${id}`,
    subagents: { allow_agents: ["alice", "bob"], models: ["fast"] },
    memory: { enabled: true, prompt_budget_tokens: 900 },
    compression: { min_percent: 40 },
    context_eviction: { keep_turns: 3 },
    archive_message_count: 500,
    archive_days: 30,
    summary_max_count: 7,
    summary_retention_days: 90,
    archive_content_max_bytes: 4096,
    a_key_added_later: { nested: [1, 2] },
  })
  const loaded = {
    agents: {
      defaults: { models: ["fast"] },
      list: [
        {
          id: "alice",
          name: "Alice",
          models: ["fast", "slow"],
          tools: ["file_read"],
          maestro: { enabled: true, max_concurrent: 2, a_runner_key: 9 },
          ...unedited("alice"),
        },
        { id: "bob", name: "Bob", tools: ["*"], ...unedited("bob") },
      ],
    },
  }

  it("keeps every field the page does not edit, for every agent", () => {
    const cfg = parseAgentsConfig(loaded)
    // An edit to Alice, as handleSaveAgent applies it: the entry is spread
    // and only the edited fields are replaced.
    const list = (cfg.list ?? []).map((a) =>
      a.id === "alice"
        ? {
            ...a,
            models: undefined,
            tools: ["file_read", "file_write"],
            maestro: { ...a.maestro!, max_concurrent: 4 },
          }
        : a,
    )
    const out = agentsPayload({ ...cfg, list }) as {
      agents: { list: Record<string, unknown>[] }
    }
    const byId = Object.fromEntries(out.agents.list.map((a) => [a.id, a]))

    for (const id of ["alice", "bob"]) {
      expect(byId[id]).toMatchObject(unedited(id))
    }
    expect(byId.alice.subagents).toEqual({
      allow_agents: ["alice", "bob"],
      models: ["fast"],
    })
    expect(byId.bob.workspace).toBe("/srv/bob")

    // The edits themselves land, and a cleared field is removed rather than
    // resurrected from the loaded copy.
    expect(byId.alice.tools).toEqual(["file_read", "file_write"])
    expect(byId.alice).not.toHaveProperty("models")
    expect(byId.alice.maestro).toEqual({
      enabled: true,
      max_concurrent: 4,
      a_runner_key: 9,
    })
    expect(byId.bob.tools).toEqual(["*"])
    expect(byId.bob).not.toHaveProperty("maestro")
  })

  it("writes an agent added on the page from its edited fields only", () => {
    const out = agentsPayload({
      defaults: {},
      list: [{ id: "bob", name: "Bob", tools: [] }],
    }) as { agents: { list: Record<string, unknown>[] } }
    expect(out.agents.list).toEqual([
      {
        id: "bob",
        name: "Bob",
        tools: [],
        message: null,
        mcp_tools: [],
        deny_tools: [],
        mounts: [],
      },
    ])
    expect(out).not.toHaveProperty("agents.defaults")
  })
})
