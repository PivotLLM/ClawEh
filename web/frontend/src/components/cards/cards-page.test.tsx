import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import type { CardsResponse } from "@/api/cards"

import { CardsPage } from "./cards-page"

// t resolves keys against the English strings, so the tests read what an
// operator sees.
vi.mock("react-i18next", async () => {
  const en = (await import("@/i18n/locales/en.json")).default as Record<
    string,
    unknown
  >
  const t = (key: string, opts?: Record<string, unknown>) => {
    const value = key
      .split(".")
      .reduce<unknown>(
        (o, k) => (o as Record<string, unknown> | undefined)?.[k],
        en,
      )
    if (typeof value !== "string") return key
    return value.replace(/\{\{(\w+)\}\}/g, (_, name: string) =>
      String(opts?.[name] ?? ""),
    )
  }
  return { useTranslation: () => ({ t }) }
})

vi.mock("@/components/page-header", () => ({
  PageHeader: ({
    title,
    children,
  }: {
    title: string
    children?: React.ReactNode
  }) => (
    <div>
      <h2>{title}</h2>
      {children}
    </div>
  ),
}))

function renderWith(response: CardsResponse) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => new Response(JSON.stringify(response), { status: 200 })),
  )
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <CardsPage />
    </QueryClientProvider>,
  )
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe("CardsPage", () => {
  it("shows the empty state when the library has no cards", async () => {
    renderWith({ cards: [], invalid: [] })
    await waitFor(() =>
      expect(screen.queryByText("No character cards yet.")).not.toBeNull(),
    )
    expect(
      screen.queryByText("A card's author instructions are used as written."),
    ).not.toBeNull()
  })

  it("lists each card with its agents, and each invalid file", async () => {
    renderWith({
      cards: [
        {
          id: "sage",
          name: "Sage",
          nickname: "S",
          tags: ["advice"],
          modification_date: 1_700_000_000,
          attached_to: ["Alice", "bob"],
        },
        {
          id: "quiet",
          name: "Quiet",
          tags: [],
          modification_date: 0,
          attached_to: [],
        },
      ],
      invalid: [{ file: "broken.json", reason: "not a card" }],
    })
    await waitFor(() =>
      expect(screen.getAllByTestId("card-row")).toHaveLength(2),
    )
    expect(screen.queryByText("Sage (S)")).not.toBeNull()
    expect(screen.queryByText("Alice, bob")).not.toBeNull()
    expect(screen.queryByText("advice")).not.toBeNull()
    expect(screen.getByTestId("invalid-card").textContent).toBe(
      "broken.json: not a card",
    )
    const downloads = screen.getAllByRole("link", { name: "Download" })
    expect(downloads[0].getAttribute("href")).toBe("/api/cards/sage")
    expect(screen.getAllByRole("button", { name: "Delete" })).toHaveLength(2)
  })
})
