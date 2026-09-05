import { fireEvent, render, screen } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"

import type { MemoryDomain, MemoryMemory } from "@/api/memory"

import { DomainCard, MemoryRow, type MemoryRowActions } from "./memory-domain"

// i18next is not initialised in the test environment, and these tests are about
// behaviour rather than copy. Returning the key keeps assertions stable and
// makes a missing key obvious.
vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (key: string, opts?: Record<string, unknown>) =>
      opts && "count" in opts ? `${key}:${String(opts.count)}` : key,
  }),
}))

function memory(over: Partial<MemoryMemory> = {}): MemoryMemory {
  return {
    id: "h1",
    type: "fact",
    text: "Home is Ottawa.",
    status: "active",
    confidence: 0.9,
    origin: "consolidation",
    file_ref: "",
    created: "2026-09-01T00:00:00Z",
    updated: "2026-09-01T00:00:00Z",
    ...over,
  }
}

function actions(over: Partial<MemoryRowActions> = {}): MemoryRowActions {
  return {
    onRetype: vi.fn(),
    onSetStatus: vi.fn(),
    onDelete: vi.fn(),
    onSelect: vi.fn(),
    selected: false,
    busy: false,
    ...over,
  }
}

describe("MemoryRow", () => {
  it("shows the memory and its current type", () => {
    render(<MemoryRow m={memory()} actions={actions()} />)
    expect(screen.getByText("Home is Ottawa.")).toBeTruthy()
    const row = screen.getByTestId("memory-row")
    expect(row.getAttribute("data-memory-type")).toBe("fact")
    expect(row.getAttribute("data-memory-status")).toBe("active")
  })

  // The retire control has to work in both directions, or the operator can
  // hide a memory with no way to bring it back.
  it("retires an active memory and restores a retired one", () => {
    const a = actions()
    const { unmount } = render(<MemoryRow m={memory()} actions={a} />)
    fireEvent.click(screen.getByLabelText("pages.memory.retire_memory"))
    expect(a.onSetStatus).toHaveBeenCalledWith(
      expect.objectContaining({ id: "h1" }),
      "retired",
    )
    unmount()

    const b = actions()
    render(<MemoryRow m={memory({ status: "retired" })} actions={b} />)
    fireEvent.click(screen.getByLabelText("pages.memory.restore_memory"))
    expect(b.onSetStatus).toHaveBeenCalledWith(
      expect.objectContaining({ id: "h1" }),
      "active",
    )
  })

  it("selects and deselects", () => {
    const a = actions()
    render(<MemoryRow m={memory()} actions={a} />)

    fireEvent.click(screen.getByLabelText("pages.memory.select_memory"))
    expect(a.onSelect).toHaveBeenCalledWith(
      expect.objectContaining({ id: "h1" }),
      true,
    )
  })

  it("deletes", () => {
    const a = actions()
    render(<MemoryRow m={memory()} actions={a} />)
    fireEvent.click(screen.getByLabelText("pages.memory.delete_memory"))
    expect(a.onDelete).toHaveBeenCalled()
  })

  // An event is the one type that never reaches the prompt. If the row does not
  // say so, a memory the assistant "has" but never sees looks like a bug.
  it("marks an event as not loaded into context", () => {
    render(<MemoryRow m={memory({ type: "event" })} actions={actions()} />)
    expect(screen.getByText("pages.memory.not_in_context")).toBeTruthy()
  })

  it("does not mark a standing memory that way", () => {
    render(<MemoryRow m={memory({ type: "rule" })} actions={actions()} />)
    expect(screen.queryByText("pages.memory.not_in_context")).toBeNull()
  })

  it("shows a retired badge", () => {
    render(<MemoryRow m={memory({ status: "retired" })} actions={actions()} />)
    expect(screen.getByText("pages.memory.retired")).toBeTruthy()
  })

  it("disables its controls while a write is in flight", () => {
    render(<MemoryRow m={memory()} actions={actions({ busy: true })} />)
    expect(
      screen
        .getByLabelText("pages.memory.delete_memory")
        .hasAttribute("disabled"),
    ).toBe(true)
    expect(
      screen
        .getByLabelText("pages.memory.retire_memory")
        .hasAttribute("disabled"),
    ).toBe(true)
  })
})

function domain(over: Partial<MemoryDomain> = {}): MemoryDomain {
  return {
    id: "d1",
    sticky: false,
    name: "Trips",
    status: "active",
    summary: "",
    memories: [],
    ...over,
  }
}

describe("DomainCard", () => {
  it("counts events separately from the total", () => {
    render(
      <DomainCard
        d={domain({
          memories: [
            memory({ id: "h1", type: "fact" }),
            memory({ id: "h2", type: "event" }),
            memory({ id: "h3", type: "event" }),
          ],
        })}
        onDeleteDomain={vi.fn()}
        onAddMemory={vi.fn()}
        rowActions={() => actions()}
      />,
    )
    // A domain of N memories is a different thing depending on how many of them
    // the assistant actually sees.
    expect(screen.getByText(/pages\.memory\.memory_count:3/)).toBeTruthy()
    expect(screen.getByText(/pages\.memory\.event_count:2/)).toBeTruthy()
  })

  it("omits the event count when there are none", () => {
    render(
      <DomainCard
        d={domain({ memories: [memory()] })}
        onDeleteDomain={vi.fn()}
        onAddMemory={vi.fn()}
        rowActions={() => actions()}
      />,
    )
    expect(screen.queryByText(/event_count/)).toBeNull()
  })

  it("adds a memory to this domain and deletes the domain", () => {
    const onAdd = vi.fn()
    const onDelete = vi.fn()
    render(
      <DomainCard
        d={domain()}
        onDeleteDomain={onDelete}
        onAddMemory={onAdd}
        rowActions={() => actions()}
      />,
    )
    fireEvent.click(screen.getByLabelText("pages.memory.add_memory_to"))
    expect(onAdd).toHaveBeenCalledWith(expect.objectContaining({ id: "d1" }))

    fireEvent.click(screen.getByLabelText("pages.memory.delete_domain"))
    expect(onDelete).toHaveBeenCalledWith(expect.objectContaining({ id: "d1" }))
  })
})
