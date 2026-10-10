import { fireEvent, render, screen } from "@testing-library/react"
import { useState } from "react"
import { describe, expect, it, vi } from "vitest"

import cases from "../../../../../config/testdata/agent_id_cases.json"
import { AgentIdField } from "./agent-id-field"
import {
  agentIdProblem,
  isValidAgentId,
  newAgentIdProblem,
  normalizeAgentId,
} from "./agent-model"

// t resolves keys against the English strings, so the tests read what an
// operator sees.
vi.mock("react-i18next", async () => {
  const en = (await import("@/i18n/locales/en.json")).default as Record<
    string,
    unknown
  >
  const t = (key: string) => {
    const value = key
      .split(".")
      .reduce<unknown>(
        (o, k) => (o as Record<string, unknown> | undefined)?.[k],
        en,
      )
    return typeof value === "string" ? value : key
  }
  return { useTranslation: () => ({ t }) }
})

function Harness({ initial = "" }: { initial?: string }) {
  const [value, setValue] = useState(initial)
  return (
    <AgentIdField value={value} existingIds={["bob"]} onChange={setValue} />
  )
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
    expect(normalizeAgentId("_x")).toBe("x")
    expect(normalizeAgentId("", "agent")).toBe("agent")
  })
})

// The table config/agent_id_rule_test.go checks the Go rule against, so the
// two copies cannot drift.
describe("agent id rule (shared cases)", () => {
  it.each(cases)("$input", ({ input, normalized, valid, problem }) => {
    expect(isValidAgentId(input)).toBe(valid)
    expect(normalizeAgentId(input)).toBe(normalized)
    expect(agentIdProblem(input)).toBe(problem)
  })
})

describe("newAgentIdProblem", () => {
  it("refuses an id another agent has, as the server does", () => {
    expect(newAgentIdProblem("alice", ["alice", "bob"])).toBe(
      'Agent id "alice" is used twice; give each agent its own id.',
    )
    expect(newAgentIdProblem("carol", ["alice", "bob"])).toBeNull()
    expect(newAgentIdProblem("Alice", ["alice"])).toBe(agentIdProblem("Alice"))
  })
})

describe("AgentIdField", () => {
  it("shows the refusal for an id already in use", () => {
    render(<Harness />)
    fireEvent.change(screen.getByLabelText("Agent ID"), {
      target: { value: "bob" },
    })
    expect(
      screen.getByText(
        'Agent id "bob" is used twice; give each agent its own id.',
      ),
    ).toBeTruthy()
    expect(screen.getByPlaceholderText("Agent ID (e.g. alice)")).toBeTruthy()
  })

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
