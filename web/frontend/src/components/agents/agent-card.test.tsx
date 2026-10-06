import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"

import { AgentCard } from "./agent-card"

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}))

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to }: { children: React.ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))

vi.mock("./message-tokens-section", () => ({
  MessageTokensSection: () => null,
}))

function renderCard(ignoredMounts: string[]) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <AgentCard
        label="alice"
        enabled
        selectedModels={[]}
        skills={[]}
        tools={[]}
        availableSkills={[]}
        availableTools={{ tools: [], default_tools: [] }}
        models={[]}
        mounts={[
          { name: "files", path: "/srv/a", notify: false, writable: false },
          { name: "notes", path: "/srv/b", notify: false, writable: false },
        ]}
        onMountsChange={() => {}}
        ignoredMounts={ignoredMounts}
        onModelsChange={() => {}}
        onSkillsChange={() => {}}
        onToolsChange={() => {}}
      />
    </QueryClientProvider>,
  )
}

describe("AgentCard mounts", () => {
  it("marks a mount ignored for a reserved name in its row", () => {
    renderCard(["files"])
    const notes = screen.getAllByTestId("ignored-mount")
    expect(notes).toHaveLength(1)
    expect(notes[0].textContent).toBe('Ignored: "files" is a reserved name.')
  })

  it("shows no note when no mount is ignored", () => {
    renderCard([])
    expect(screen.queryAllByTestId("ignored-mount")).toHaveLength(0)
  })
})
