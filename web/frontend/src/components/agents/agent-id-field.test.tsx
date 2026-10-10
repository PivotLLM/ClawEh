import { fireEvent, render, screen } from "@testing-library/react"
import { useState } from "react"
import { describe, expect, it } from "vitest"

import { AgentIdField } from "./agent-id-field"
import { agentIdProblem, normalizeAgentId } from "./agent-model"

function Harness({ initial = "" }: { initial?: string }) {
  const [value, setValue] = useState(initial)
  return <AgentIdField value={value} onChange={setValue} />
}

describe("agentIdProblem", () => {
  it("accepts ids already in normal form", () => {
    for (const id of ["alice", "bob-2", "a_b", "main", "b".repeat(64)]) {
      expect(agentIdProblem(id)).toBeNull()
    }
  })

  it("gives the sentence the server gives", () => {
    const long = "a".repeat(65)
    expect(agentIdProblem("Alice.Smith")).toBe(
      'Agent id "Alice.Smith" may use only lower-case letters, digits, - and _; use "alice-smith".',
    )
    expect(agentIdProblem(" alice")).toBe(
      'Agent id " alice" may use only lower-case letters, digits, - and _; use "alice".',
    )
    expect(agentIdProblem("-alice")).toBe(
      'Agent id "-alice" must start with a letter or digit; use "alice".',
    )
    expect(agentIdProblem(long)).toBe(
      `Agent id "${long}" is longer than 64 characters; use "${"a".repeat(64)}".`,
    )
    expect(agentIdProblem("!!!")).toBe(
      'Agent id "!!!" may use only lower-case letters, digits, - and _.',
    )
    expect(agentIdProblem("")).toBe(
      'An agent has no id; give it one, such as "alice".',
    )
  })

  it("normalizes as config.NormalizeAgentID does", () => {
    expect(normalizeAgentId("  Bob.Smith  ")).toBe("bob-smith")
    expect(normalizeAgentId("")).toBe("main")
    expect(normalizeAgentId("_x")).toBe("_x")
  })
})

describe("AgentIdField", () => {
  it("shows the refusal while the id is not in normal form", () => {
    render(<Harness />)
    const input = screen.getByLabelText("Agent ID")
    expect(screen.queryByText(/Agent id/)).toBeNull()

    fireEvent.change(input, { target: { value: "Alice" } })
    expect(
      screen.getByText(
        'Agent id "Alice" may use only lower-case letters, digits, - and _; use "alice".',
      ),
    ).toBeTruthy()
    expect(input.getAttribute("aria-invalid")).toBe("true")

    fireEvent.change(input, { target: { value: "alice" } })
    expect(screen.queryByText(/Agent id/)).toBeNull()
    expect(input.getAttribute("aria-invalid")).toBe("false")
  })
})
