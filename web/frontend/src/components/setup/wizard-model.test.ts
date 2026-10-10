import { describe, expect, it } from "vitest"

import { isValidAgentId } from "@/components/agents/agent-model"

import { uniqueAgentId } from "./wizard-model"

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
