import { act, renderHook } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import type { AgentEntry } from "@/components/agents/agent-model"

import {
  AUTOSAVE_MS,
  editsFromAgent,
  useAgentAutosave,
} from "./use-agent-autosave"

// This hook replaced nine parallel arrays indexed by agent position, a
// nine-field ref mirroring them for the debounce, and an eleven-parameter save.
// The properties below are the ones that were previously kept true by hand.
//
// Note every test holds its `agents` array in a variable rather than inlining it
// into the renderHook callback. The hook reseeds when that array's IDENTITY
// changes, so a fresh array literal on each render reseeds forever — React
// stops it with "Too many re-renders". That is a real constraint on callers,
// documented on the hook; writing these tests is what surfaced it.

function agent(id: string, extra: Partial<AgentEntry> = {}): AgentEntry {
  return { id, tools: [], ...extra } as AgentEntry
}

beforeEach(() => {
  vi.useFakeTimers()
})

afterEach(() => {
  vi.useRealTimers()
})

describe("editsFromAgent", () => {
  it("applies the documented defaults for an unconfigured agent", () => {
    expect(editsFromAgent(agent("a"))).toEqual({
      models: [],
      skills: [],
      tools: [],
      message: { mins: 0, count: 2 },
      temperature: undefined,
      summarizationModels: [],
      shareCommon: true,
      mounts: [],
      mcpTools: [],
    })
  })

  // share_common is on unless explicitly false — `undefined` must not read as
  // "off", or opening an agent would silently propose turning it off.
  it("treats a missing share_common as on and only false as off", () => {
    expect(editsFromAgent(agent("a")).shareCommon).toBe(true)
    expect(
      editsFromAgent(agent("a", { share_common: false })).shareCommon,
    ).toBe(false)
    expect(editsFromAgent(agent("a", { share_common: true })).shareCommon).toBe(
      true,
    )
  })
})

describe("useAgentAutosave", () => {
  it("seeds one buffer per agent, in order", () => {
    const agents = [agent("a", { temperature: 0.5 }), agent("b")]
    const { result } = renderHook(() =>
      useAgentAutosave(agents, async () => {}),
    )

    expect(result.current.edits).toHaveLength(2)
    expect(result.current.edits[0].temperature).toBe(0.5)
    expect(result.current.edits[1].temperature).toBeUndefined()
  })

  it("applies a partial edit without disturbing the other fields", () => {
    const agents = [agent("a", { models: ["m1"] })]
    const { result } = renderHook(() =>
      useAgentAutosave(agents, async () => {}),
    )

    act(() => result.current.editAgent(0, { temperature: 0.9 }))

    expect(result.current.edits[0].temperature).toBe(0.9)
    expect(result.current.edits[0].models).toEqual(["m1"])
  })

  it("edits one agent without touching its neighbour", () => {
    const agents = [agent("a"), agent("b")]
    const { result } = renderHook(() =>
      useAgentAutosave(agents, async () => {}),
    )

    act(() => result.current.editAgent(1, { temperature: 0.3 }))

    expect(result.current.edits[0].temperature).toBeUndefined()
    expect(result.current.edits[1].temperature).toBe(0.3)
  })

  it("debounces: no save before the delay, one save after", () => {
    const onSave = vi.fn().mockResolvedValue(undefined)
    const agents = [agent("a")]
    const { result } = renderHook(() => useAgentAutosave(agents, onSave))

    act(() => result.current.editAgent(0, { temperature: 0.1 }))
    act(() => void vi.advanceTimersByTime(AUTOSAVE_MS - 1))
    expect(onSave).not.toHaveBeenCalled()

    act(() => void vi.advanceTimersByTime(1))
    expect(onSave).toHaveBeenCalledTimes(1)
  })

  // The whole reason the hook reads its buffers through a ref at fire time.
  // Typing three characters quickly must save once, with the LAST value — not
  // three times, and not the first keystroke.
  it("coalesces rapid edits into one save carrying the latest value", () => {
    const onSave = vi.fn().mockResolvedValue(undefined)
    const agents = [agent("a")]
    const { result } = renderHook(() => useAgentAutosave(agents, onSave))

    act(() => result.current.editAgent(0, { temperature: 0.1 }))
    act(() => void vi.advanceTimersByTime(100))
    act(() => result.current.editAgent(0, { temperature: 0.2 }))
    act(() => void vi.advanceTimersByTime(100))
    act(() => result.current.editAgent(0, { temperature: 0.3 }))
    act(() => void vi.advanceTimersByTime(AUTOSAVE_MS))

    expect(onSave).toHaveBeenCalledTimes(1)
    expect(onSave.mock.calls[0][0]).toBe(0)
    expect(onSave.mock.calls[0][1].temperature).toBe(0.3)
  })

  it("debounces each agent independently", () => {
    const onSave = vi.fn().mockResolvedValue(undefined)
    const agents = [agent("a"), agent("b")]
    const { result } = renderHook(() => useAgentAutosave(agents, onSave))

    act(() => result.current.editAgent(0, { temperature: 0.1 }))
    act(() => result.current.editAgent(1, { temperature: 0.2 }))
    act(() => void vi.advanceTimersByTime(AUTOSAVE_MS))

    expect(onSave).toHaveBeenCalledTimes(2)
    expect(onSave.mock.calls.map((c) => c[0]).sort()).toEqual([0, 1])
  })

  it("reports saving then saved for the agent it saved", async () => {
    const onSave = vi.fn().mockResolvedValue(undefined)
    const agents = [agent("a")]
    const { result } = renderHook(() => useAgentAutosave(agents, onSave))

    act(() => result.current.editAgent(0, { temperature: 0.1 }))
    act(() => void vi.advanceTimersByTime(AUTOSAVE_MS))
    expect(result.current.status["agent-0"]).toBe("saving")

    // Not waitFor(): it polls on real timers, which the fake ones freeze, so it
    // would sit here until the 5s test timeout. Flushing the microtask queue is
    // what actually lets the resolved onSave promise settle.
    await act(async () => {
      await Promise.resolve()
      await Promise.resolve()
    })
    expect(result.current.status["agent-0"]).toBe("saved")
  })

  it("reports an error when the save rejects", async () => {
    const onSave = vi.fn().mockRejectedValue(new Error("nope"))
    const agents = [agent("a")]
    const { result } = renderHook(() => useAgentAutosave(agents, onSave))

    act(() => result.current.editAgent(0, { temperature: 0.1 }))
    act(() => void vi.advanceTimersByTime(AUTOSAVE_MS))
    await act(async () => {
      await Promise.resolve()
      await Promise.resolve()
    })
    expect(result.current.status["agent-0"]).toBe("error")
  })

  // Adding an agent re-sorts the list, so buffers must follow the NEW order.
  // With the nine parallel arrays this was the fragile case: index 0 silently
  // became a different agent.
  it("reseeds the buffers when the agent list changes", () => {
    const first = [agent("b", { temperature: 0.7 })]
    const { result, rerender } = renderHook(
      ({ agents }) => useAgentAutosave(agents, async () => {}),
      { initialProps: { agents: first } },
    )

    expect(result.current.edits[0].temperature).toBe(0.7)

    // "a" sorts ahead of "b", so every index shifts.
    const second = [agent("a"), agent("b", { temperature: 0.7 })]
    rerender({ agents: second })

    expect(result.current.edits).toHaveLength(2)
    expect(result.current.edits[0].temperature).toBeUndefined()
    expect(result.current.edits[1].temperature).toBe(0.7)
  })

  // After a save the page writes the persisted config back, which arrives as a
  // new agents array. That must NOT discard what the user has typed since.
  it("does not clobber edits made while a save was in flight", async () => {
    let resolveSave: () => void = () => {}
    const onSave = vi.fn(
      () =>
        new Promise<void>((res) => {
          resolveSave = res
        }),
    )
    const agents = [agent("a")]
    const { result, rerender } = renderHook(
      ({ agents }) => useAgentAutosave(agents, onSave),
      { initialProps: { agents } },
    )

    act(() => result.current.editAgent(0, { temperature: 0.1 }))
    act(() => void vi.advanceTimersByTime(AUTOSAVE_MS))
    expect(onSave).toHaveBeenCalledTimes(1)

    // The user keeps typing while the request is outstanding.
    act(() => result.current.editAgent(0, { temperature: 0.4 }))

    // The save lands and the page hands back the persisted agent.
    await act(async () => {
      resolveSave()
      await Promise.resolve()
    })
    rerender({ agents: [agent("a", { temperature: 0.1 })] })

    expect(result.current.edits[0].temperature).toBe(0.4)
  })
})
