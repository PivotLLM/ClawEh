import { useEffect, useRef, useState } from "react"

import type { AgentEntry, MountEntry } from "@/components/agents/agent-model"

// AgentEdits is one agent's in-progress edit buffer.
//
// This was nine parallel arrays on the page — agentModelsEdits, agentSkillsEdits,
// agentToolsEdits, agentMessageEdits, agentTemperatureEdits,
// agentSummarizationEdits, agentShareCommonEdits, agentMountsEdits,
// agentMCPToolsEdits — each indexed by the agent's position, each with its own
// setState, each mirrored into a nine-field ref for the debounce to read, and
// all nine threaded through an eleven-parameter save function. Every one of them
// had to be kept the same length and in the same order as the others; nothing
// enforced that but care.
export interface AgentEdits {
  models: string[]
  skills: string[]
  tools: string[]
  message: { mins: number; count: number }
  temperature: number | undefined
  summarizationModels: string[]
  shareCommon: boolean
  mounts: MountEntry[]
  mcpTools: string[]
}

// editsFromAgent seeds an edit buffer from the saved agent. The defaults here
// are the ones the old per-array seeding used, kept exactly: an absent message
// window means 0/2, and share_common is on unless explicitly false.
export function editsFromAgent(a: AgentEntry): AgentEdits {
  return {
    models: a.models ?? [],
    skills: a.skills ?? [],
    tools: a.tools ?? [],
    message: {
      mins: a.message?.window_minutes ?? 0,
      count: a.message?.window_count ?? 2,
    },
    temperature: a.temperature,
    summarizationModels: a.summarization_models ?? [],
    shareCommon: a.share_common !== false,
    mounts: a.mounts ?? [],
    mcpTools: a.mcp_tools ?? [],
  }
}

export type AutoStatus = Record<string, "saving" | "saved" | "error">

export const AUTOSAVE_MS = 600
const SAVED_HINT_MS = 2000

export interface UseAgentAutosave {
  /** Edit buffers, index-aligned with the agents passed in. */
  edits: AgentEdits[]
  /** Apply a partial edit to one agent and schedule its debounced save. */
  editAgent: (index: number, patch: Partial<AgentEdits>) => void
  /** Per-agent "Saving…/Saved/error" hint, keyed `agent-<index>`. */
  status: AutoStatus
}

/**
 * useAgentAutosave owns the per-agent edit buffers, their debounced save, and
 * the per-card save status.
 *
 * onSave is called with the LATEST buffer for that agent at the moment the
 * debounce fires, not the one captured when the timer was set — the handler
 * that schedules a save runs before React has applied the state update, so
 * reading the render value there would persist the previous keystroke. It must
 * REJECT on failure; that is how the error state is reached.
 *
 * Status and reseed suppression are owned here rather than exposed, so onSave
 * needs nothing back from this hook. That is what keeps the two from referring
 * to each other in a cycle the caller has to break with hoisting or a ref.
 *
 * `agents` MUST BE A STABLE REFERENCE across renders. The reseed below triggers
 * on the array's identity, so a fresh literal each render — `list ?? []` where
 * `list` is undefined, say — reseeds on every render forever, and React stops it
 * with "Too many re-renders". Hold it in state or memoise it. (AgentsPage passes
 * `agentsCfg.list`, which is state and therefore stable.)
 */
export function useAgentAutosave(
  agents: AgentEntry[],
  onSave: (index: number, edits: AgentEdits) => Promise<void>,
): UseAgentAutosave {
  const [edits, setEdits] = useState<AgentEdits[]>(() =>
    agents.map(editsFromAgent),
  )
  const [status, setStatus] = useState<AutoStatus>({})

  // Reseeding is decided during render rather than in an effect: an effect
  // renders once with buffers belonging to the previous agent list, which is
  // visible when an agent is added or removed.
  const [skipNext, setSkipNext] = useState(false)
  const [syncedAgents, setSyncedAgents] = useState(agents)
  if (agents !== syncedAgents) {
    setSyncedAgents(agents)
    if (skipNext) {
      setSkipNext(false)
    } else {
      setEdits(agents.map(editsFromAgent))
    }
  }

  // The debounce fires long after the handler that scheduled it, so it reaches
  // the buffers through a ref. Written in an effect, not during render: a
  // render-phase ref write is a side effect in render.
  const latest = useRef(edits)
  useEffect(() => {
    latest.current = edits
  })

  const saveTimers = useRef<Record<string, ReturnType<typeof setTimeout>>>({})
  const savedTimers = useRef<Record<string, ReturnType<typeof setTimeout>>>({})

  // Timers outlive the component if it unmounts mid-debounce, firing a save
  // against a page that is gone.
  useEffect(() => {
    const save = saveTimers.current
    const saved = savedTimers.current
    return () => {
      for (const id of Object.values(save)) clearTimeout(id)
      for (const id of Object.values(saved)) clearTimeout(id)
    }
  }, [])

  const key = (index: number) => `agent-${index}`

  const markSaving = (index: number) =>
    setStatus((s) => ({ ...s, [key(index)]: "saving" }))

  const markError = (index: number) =>
    setStatus((s) => ({ ...s, [key(index)]: "error" }))

  const markSaved = (index: number) => {
    const k = key(index)
    setStatus((s) => ({ ...s, [k]: "saved" }))
    clearTimeout(savedTimers.current[k])
    savedTimers.current[k] = setTimeout(() => {
      setStatus((s) => {
        const next = { ...s }
        delete next[k]
        return next
      })
    }, SAVED_HINT_MS)
  }

  const editAgent = (index: number, patch: Partial<AgentEdits>) => {
    setEdits((prev) => {
      const next = [...prev]
      const base = next[index] ?? editsFromAgent(agents[index])
      next[index] = { ...base, ...patch }
      return next
    })

    const k = key(index)
    clearTimeout(saveTimers.current[k])
    saveTimers.current[k] = setTimeout(() => {
      const current = latest.current[index]
      if (!current) return
      markSaving(index)
      // A successful save writes the persisted config back into the caller's
      // state, which would otherwise reseed these buffers and discard whatever
      // has been typed since. Suppress that reseed BEFORE awaiting, so it can
      // never lose the race — and lift the suppression again if the save
      // fails, or the next genuine reseed would be swallowed.
      setSkipNext(true)
      onSave(index, current)
        .then(() => markSaved(index))
        .catch(() => {
          setSkipNext(false)
          markError(index)
        })
    }, AUTOSAVE_MS)
  }

  return { edits, editAgent, status }
}
