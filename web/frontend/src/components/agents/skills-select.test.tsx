import { fireEvent, render, screen } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"

import { SkillsSelect } from "./skills-select"

const installed = [
  { name: "news", description: "", source: "global" },
  { name: "weather", description: "", source: "global" },
]

// An agent has only the skills its list names: an empty list is labelled
// "No skills", "All skills" writes "*", and a change from "*" writes the
// installed skills out explicitly.
describe("SkillsSelect", () => {
  it("labels an empty list No skills", () => {
    render(
      <SkillsSelect
        selected={[]}
        availableSkills={installed}
        onChange={() => {}}
      />,
    )
    expect(screen.queryByText("No skills")).not.toBeNull()
  })

  it("All skills writes every skill", () => {
    const onChange = vi.fn()
    render(
      <SkillsSelect
        selected={[]}
        availableSkills={installed}
        onChange={onChange}
      />,
    )
    fireEvent.click(screen.getByRole("button", { name: "All skills" }))
    expect(onChange).toHaveBeenCalledWith(["*"])
  })

  it("turning one skill off from All skills lists the rest", () => {
    const onChange = vi.fn()
    render(
      <SkillsSelect
        selected={["*"]}
        availableSkills={installed}
        onChange={onChange}
      />,
    )
    fireEvent.click(screen.getByRole("button", { name: "news" }))
    expect(onChange).toHaveBeenCalledWith(["weather"])
  })
})
