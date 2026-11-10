import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import type { ReactNode } from "react"
import { describe, expect, it, vi } from "vitest"

import { addModel } from "@/api/models"

import { AddModelSheet } from "./add-model-sheet"

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}))

vi.mock("@/api/models", () => ({
  addModel: vi.fn(async () => ({ status: "ok" })),
  setDefaultModel: vi.fn(async () => ({ status: "ok" })),
}))

vi.mock("@/api/providers", () => ({
  getProviders: vi.fn(async () => ({
    providers: [
      {
        index: 0,
        name: "Alice Provider",
        protocol: "openai-chat",
        api_key: "",
      },
    ],
    total: 1,
  })),
}))

// Radix Select does not run in jsdom; a native select keeps the same
// value/onValueChange contract.
vi.mock("@/components/ui/select", () => ({
  Select: ({
    value,
    onValueChange,
    children,
  }: {
    value?: string
    onValueChange?: (v: string) => void
    children: ReactNode
  }) => (
    <select
      data-testid="select"
      aria-label="select"
      value={value ?? ""}
      onChange={(e) => onValueChange?.(e.target.value)}
    >
      <option value="">none</option>
      {children}
    </select>
  ),
  SelectTrigger: () => null,
  SelectValue: () => null,
  SelectContent: ({ children }: { children: ReactNode }) => <>{children}</>,
  SelectItem: ({ value, children }: { value: string; children: ReactNode }) => (
    <option value={value}>{children}</option>
  ),
}))

describe("AddModelSheet", () => {
  it("adds the model enabled", async () => {
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    render(
      <QueryClientProvider client={qc}>
        <AddModelSheet
          open
          onClose={() => {}}
          onSaved={() => {}}
          existingModelNames={[]}
          turnTimeout={0}
        />
      </QueryClientProvider>,
    )

    fireEvent.change(
      screen.getByPlaceholderText("models.add.modelNamePlaceholder"),
      { target: { value: "alice-whisper" } },
    )
    fireEvent.change(
      screen.getByPlaceholderText("models.add.modelIdPlaceholder"),
      { target: { value: "openai/whisper-large-v3" } },
    )
    const providerSelect = await waitFor(() => {
      const s = screen
        .getAllByTestId("select")
        .find((el) => el.querySelector('option[value="Alice Provider"]'))
      if (!s) throw new Error("provider list not loaded")
      return s
    })
    fireEvent.change(providerSelect, { target: { value: "Alice Provider" } })
    fireEvent.click(screen.getByText("models.add.confirm"))

    await waitFor(() => expect(addModel).toHaveBeenCalled())
    expect(addModel).toHaveBeenCalledWith(
      expect.objectContaining({
        model_name: "alice-whisper",
        provider: "Alice Provider",
        enabled: true,
      }),
    )
  })
})
