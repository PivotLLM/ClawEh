import { describe, expect, it } from "vitest"

import { isValidAgentId } from "@/components/agents/agent-model"

import { uniqueAgentId } from "./wizard-model"

describe("uniqueAgentId", () => {
  it("derives the id by the agent id rule", () => {
    expect(uniqueAgentId("Alice Smith", new Set())).toBe("alice-smith")
    expect(uniqueAgentId("_Bob_", new Set())).toBe("bob")
    expect(uniqueAgentId("!!!", new Set())).toBe("agent")
    expect(uniqueAgentId("b".repeat(70), new Set())).toBe("b".repeat(64))
  })

  it("appends a number when the id is taken, within 64 characters", () => {
    expect(uniqueAgentId("Alice", new Set(["alice", "alice-2"]))).toBe(
      "alice-3",
    )
    const long = "b".repeat(64)
    const id = uniqueAgentId(long, new Set([long]))
    expect(id).toBe(`${"b".repeat(62)}-2`)
    expect(isValidAgentId(id)).toBe(true)
  })
})
