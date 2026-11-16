import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"

import "@/i18n"

import { LoginPage } from "./login-page"

// The page and the document title name the product only as the server does:
// /api/auth/status answers before login and carries the names.
function renderWithStatus(status: Record<string, unknown>) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => ({ ok: true, json: async () => status })),
  )
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <LoginPage />
    </QueryClientProvider>,
  )
}

describe("LoginPage", () => {
  it("tells the operator to run the admin command under the binary's name", async () => {
    renderWithStatus({
      configured: false,
      authenticated: false,
      username: "",
      name: "Alice Suite",
      short_name: "Alice",
      binary_name: "alice-cli",
    })

    const command = await screen.findByTestId("login-admin-command")
    expect(command.textContent).toBe("alice-cli admin")
    expect(screen.getByRole("heading").textContent).toBe("Alice Suite")
    expect(
      screen.getByText(
        "Then check again. Alice picks the account up within a minute.",
      ),
    ).toBeTruthy()
  })

  it("sets the document title from the auth status", async () => {
    renderWithStatus({
      configured: true,
      authenticated: false,
      username: "",
      name: "Bob Suite",
      short_name: "Bob",
      binary_name: "bob",
    })

    await screen.findByTestId("login-form")
    expect(document.title).toBe("Bob Suite")
  })
})
