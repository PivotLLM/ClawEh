import { render, screen } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"

import i18n from "@/i18n"

import { TurnTimeoutNote, formatDuration } from "./turn-timeout-note"

// The router is not mounted here; render its Link as a plain anchor.
vi.mock("@tanstack/react-router", () => ({
  Link: ({
    children,
    to,
    hash,
    ...rest
  }: {
    children: React.ReactNode
    to: string
    hash?: string
  }) => (
    <a href={hash ? `${to}#${hash}` : to} {...rest}>
      {children}
    </a>
  ),
}))

describe("formatDuration", () => {
  it.each([
    [900, "15 minutes"],
    [60, "1 minute"],
    [3600, "1 hour"],
    [5400, "1 hour 30 minutes"],
    [7322, "2 hours 2 minutes 2 seconds"],
    [45, "45 seconds"],
    [1, "1 second"],
  ])("formats %i seconds as %s", (seconds, want) => {
    expect(formatDuration(seconds, i18n.t)).toBe(want)
  })
})

describe("TurnTimeoutNote", () => {
  it("shows the limit and links to the Turn timeout when the request timeout is longer", () => {
    render(<TurnTimeoutNote requestTimeout="3600" turnTimeout={900} />)
    const note = screen.getByTestId("model-turn-timeout-note")
    expect(note.textContent).toBe("Limited to 15 minutes by the Turn timeout.")
    const link = screen.getByRole("link", { name: "Turn timeout" })
    expect(link.getAttribute("href")).toBe("/system#turn-timeout")
  })

  it.each([
    ["equal to the turn timeout", "900", 900],
    ["shorter than the turn timeout", "300", 900],
    ["blank (the default)", "", 900],
    ["not a number", "abc", 900],
    ["set while the turn timeout is unknown", "3600", 0],
  ])("is hidden when the request timeout is %s", (_, request, turn) => {
    render(<TurnTimeoutNote requestTimeout={request} turnTimeout={turn} />)
    expect(screen.queryByTestId("model-turn-timeout-note")).toBeNull()
  })
})
