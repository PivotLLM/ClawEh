import { fireEvent, render, screen } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"

import { MayMessageSelect } from "./may-message-select"

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}))

const agents = [
  { id: "alice", name: "Alice" },
  { id: "bob", name: "Bob" },
]

function renderSelect(
  value: string[],
  list: { id: string; name?: string }[] = agents,
) {
  const onChange = vi.fn()
  render(
    <MayMessageSelect
      agentName="Alice"
      agentId="alice"
      agents={list}
      value={value}
      messageGranted
      listUsed
      onChange={onChange}
    />,
  )
  return onChange
}

const box = (name: string) =>
  screen.getByRole("checkbox", { name }) as HTMLButtonElement

describe("MayMessageSelect", () => {
  it("disables the agent boxes while All agents is on, but not a stale entry", () => {
    renderSelect(["*", "ghost"])
    expect(box("agents.mayMessageAll").getAttribute("aria-checked")).toBe(
      "true",
    )
    expect(box("Bob").disabled).toBe(true)
    expect(box("Bob").getAttribute("aria-checked")).toBe("true")
    // A saved entry naming no configured agent stays removable.
    expect(box("ghost").disabled).toBe(false)
  })

  it("enables the agent boxes once All agents is turned off, keeping named entries", () => {
    const onChange = renderSelect(["*", "bob"])
    fireEvent.click(box("agents.mayMessageAll"))
    expect(onChange).toHaveBeenLastCalledWith(["bob"])
    expect(box("Bob").disabled).toBe(false)
    expect(box("Bob").getAttribute("aria-checked")).toBe("true")
  })

  it("ticking an agent adds its id", () => {
    const onChange = renderSelect([])
    fireEvent.click(box("Bob"))
    expect(onChange).toHaveBeenLastCalledWith(["bob"])
  })

  it("says there are no other agents when the agent is the only one", () => {
    renderSelect([], [{ id: "alice", name: "Alice" }])
    expect(screen.getByText("agents.mayMessageNoOthers")).toBeTruthy()
    expect(screen.getAllByRole("checkbox")).toHaveLength(1)
  })
})
