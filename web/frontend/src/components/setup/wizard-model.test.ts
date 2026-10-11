import { describe, expect, it } from "vitest"

import { isValidAgentId } from "@/components/agents/agent-model"

import { uniqueAgentId, wizardAgentList } from "./wizard-model"

describe("uniqueAgentId", () => {
  it("derives the id by the agent id rule, keeping the name's case", () => {
    expect(uniqueAgentId("Alice Smith", new Set())).toBe("Alice-Smith")
    expect(uniqueAgentId("_Bob_", new Set())).toBe("Bob")
    expect(uniqueAgentId("!!!", new Set())).toBe("agent")
    expect(uniqueAgentId("b".repeat(70), new Set())).toBe("b".repeat(64))
  })

  it("appends a number when the id is taken, within 64 characters", () => {
    expect(uniqueAgentId("Alice", new Set(["alice", "ALICE-2"]))).toBe(
      "Alice-3",
    )
    const long = "b".repeat(64)
    const id = uniqueAgentId(long, new Set([long]))
    expect(id).toBe(`${"b".repeat(62)}-2`)
    expect(isValidAgentId(id)).toBe(true)
  })
})

// The wizard's agent starts with no tools: an agent has only the tools its
// list names, and the operator picks them on the Agents page.
describe("wizardAgentList", () => {
  it("adds a new agent with no tools", () => {
    const list = wizardAgentList([], "Alice", "GPT", false)
    expect(list).toEqual([
      { id: "Alice", name: "Alice", models: ["GPT"], default: true },
    ])
    expect(list[0]).not.toHaveProperty("tools")
  })

  it("adds a second agent when a model is already configured", () => {
    const list = wizardAgentList(
      [{ id: "alice", default: true, tools: ["file_read"] }],
      "Bob",
      "GPT",
      true,
    )
    expect(list[1]).toEqual({ id: "Bob", name: "Bob", models: ["GPT"] })
    expect(list[0].tools).toEqual(["file_read"])
  })

  it("sets up the default agent in place without granting tools", () => {
    const list = wizardAgentList(
      [{ id: "claw", name: "Claw", default: true }],
      "Alice",
      "GPT",
      false,
    )
    expect(list).toEqual([
      { id: "claw", name: "Alice", default: true, models: ["GPT"] },
    ])
  })
})
