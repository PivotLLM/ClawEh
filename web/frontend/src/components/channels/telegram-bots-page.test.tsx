import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import { SidebarProvider } from "@/components/ui/sidebar"

import { TelegramBotsPage } from "./telegram-bots-page"

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}))

afterEach(() => {
  vi.unstubAllGlobals()
})

describe("TelegramBotsPage", () => {
  it("names each bot, not its internal channel", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => ({
        ok: true,
        json: async () => ({
          channels: {
            telegram: [
              { id: "default", enabled: true, token: "x" },
              { id: "alice", enabled: true, token: "y" },
            ],
          },
        }),
      })),
    )
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    render(
      <QueryClientProvider client={qc}>
        <SidebarProvider>
          <TelegramBotsPage />
        </SidebarProvider>
      </QueryClientProvider>,
    )

    expect(await screen.findByText("Default bot")).toBeTruthy()
    expect(screen.getByText("alice")).toBeTruthy()
    expect(screen.queryByText(/telegram-/)).toBeNull()
    expect(
      screen.getByRole("button", { name: "Delete Default bot" }),
    ).toBeTruthy()
    expect(screen.getByRole("button", { name: "Delete alice" })).toBeTruthy()
  })
})
