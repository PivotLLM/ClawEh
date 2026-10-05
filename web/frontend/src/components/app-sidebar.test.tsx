import { render, screen } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"

import { SidebarProvider } from "@/components/ui/sidebar"
import { TooltipProvider } from "@/components/ui/tooltip"

import { AppSidebar } from "./app-sidebar"

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}))

// The sidebar links everywhere; the router is not mounted here.
vi.mock("@tanstack/react-router", () => ({
  Link: ({
    children,
    to,
    ...rest
  }: {
    children: React.ReactNode
    to: string
  }) => (
    <a href={to} {...rest}>
      {children}
    </a>
  ),
  useRouterState: () => ({ location: { pathname: "/agents" } }),
}))

vi.mock("@/api/auth", () => ({ logout: vi.fn() }))
vi.mock("@/api/system", () => ({
  getVersion: vi.fn().mockResolvedValue("1.2.3"),
}))
vi.mock("@/hooks/use-sidebar-channels", () => ({
  useSidebarChannels: () => ({ channelItems: [] }),
}))
vi.mock("@/lib/claw-chat-controller", () => ({ teardownChatStore: vi.fn() }))

async function renderSidebar() {
  const view = render(
    <TooltipProvider>
      <SidebarProvider>
        <AppSidebar />
      </SidebarProvider>
    </TooltipProvider>,
  )
  // The footer version lands asynchronously; wait for it so the state
  // update happens inside the test.
  await screen.findByText("ClawEh v1.2.3")
  return view
}

describe("AppSidebar", () => {
  it("renders Chat as a direct link, not a disclosure group", async () => {
    await renderSidebar()

    const chat = screen.getByTestId("nav-chat")
    expect(chat.tagName).toBe("A")
    expect(chat.getAttribute("href")).toBe("/")
    expect(chat.textContent).toBe("navigation.chat")
    expect(screen.queryByRole("button", { name: "navigation.chat" })).toBeNull()

    // The groups with several children are still disclosure controls.
    const models = screen.getByRole("button", {
      name: "navigation.model_group",
    })
    expect(models.getAttribute("aria-expanded")).not.toBeNull()
  })
})
