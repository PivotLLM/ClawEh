import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { fireEvent, render, screen } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"

import { SidebarProvider } from "@/components/ui/sidebar"
import "@/i18n"

import { SecMsgPage } from "./secmsg-page"

vi.mock("@/api/channels", () => ({
  getAppConfig: vi.fn().mockResolvedValue({
    channels: {
      secmsg: [
        {
          name: "signal",
          enabled: true,
          address: "/run/secmsg.sock",
          accounts: [
            { account: "alice" },
            { account: "bob", name: "bob-chat" },
          ],
        },
      ],
    },
  }),
  getSecMsgAccounts: vi.fn().mockResolvedValue({ accounts: [] }),
  getSecMsgLinkStatus: vi.fn(),
  requestSecMsgLink: vi.fn(),
  patchAppConfig: vi.fn().mockResolvedValue({ status: "ok" }),
}))

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <SidebarProvider>
        <SecMsgPage />
      </SidebarProvider>
    </QueryClientProvider>,
  )
}

describe("SecMsgPage delete buttons", () => {
  it("names the daemon on its delete button", async () => {
    renderPage()
    const del = await screen.findByRole("button", { name: "Delete signal" })
    expect(del.getAttribute("title")).toBe("Delete signal")
  })

  it("names each account on its remove button", async () => {
    renderPage()
    await screen.findByRole("button", { name: "Delete signal" })
    fireEvent.click(screen.getByRole("button", { name: "Edit" }))
    // An account without a name is its channel, <daemon>-<account>.
    const alice = await screen.findByRole("button", {
      name: "Remove account signal-alice",
    })
    expect(alice.getAttribute("title")).toBe("Remove account signal-alice")
    expect(
      screen.getByRole("button", { name: "Remove account bob-chat" }),
    ).toBeTruthy()
  })
})
