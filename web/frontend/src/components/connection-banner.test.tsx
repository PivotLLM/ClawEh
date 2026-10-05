import { act, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import { setGatewayReachable } from "@/store/connection"

import { ConnectionBanner } from "./connection-banner"

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}))

afterEach(() => {
  setGatewayReachable(true)
})

describe("ConnectionBanner", () => {
  it("shows while the gateway is unreachable and hides when it answers again", () => {
    render(<ConnectionBanner />)
    expect(screen.queryByTestId("connection-banner")).toBe(null)

    act(() => setGatewayReachable(false))
    expect(screen.getByTestId("connection-banner").textContent).toBe(
      "connection.lost",
    )

    act(() => setGatewayReachable(true))
    expect(screen.queryByTestId("connection-banner")).toBe(null)
  })
})
