import { useState } from "react"
import { useTranslation } from "react-i18next"

import {
  ALL_AGENTS,
  mayMessageRows,
  toggleAllowAgent,
  withAllAgents,
} from "@/components/agents/agent-model"
import { Checkbox } from "@/components/ui/checkbox"

interface MayMessageSelectProps {
  /** The agent's display name (name, else id), for the notes. */
  agentName: string
  /** The agent's id, left out of the list. */
  agentId: string
  /** Every configured agent. */
  agents: { id: string; name?: string }[]
  /** subagents.allow_agents as saved. */
  value: string[]
  /** The agent has the agent_message tool. */
  messageGranted: boolean
  /** Something reads the list: agent_message, agent_spawn or the forum. */
  listUsed: boolean
  onChange: (allow: string[]) => void
}

// MayMessageSelect edits subagents.allow_agents: one box per other configured
// agent, plus "All agents" ("*"). An entry naming no other configured agent
// stays visible, checked and flagged, so it can be removed. A note says when
// agent_message is granted but the list is empty, and when nothing reads the
// list (no agent_message, no agent_spawn, no forum).
export function MayMessageSelect({
  agentName,
  agentId,
  agents,
  value,
  messageGranted,
  listUsed,
  onChange,
}: MayMessageSelectProps) {
  const { t } = useTranslation()
  // Local copy so a toggle shows immediately; the parent's value catches up
  // after the debounced save. Resets per agent because the card is keyed.
  const [allow, setAllow] = useState(value)
  const all = allow.includes(ALL_AGENTS)
  const rows = mayMessageRows(allow, agentId, agents)

  const set = (next: string[]) => {
    setAllow(next)
    onChange(next)
  }

  return (
    <div className="space-y-1.5">
      <p className="text-foreground text-xs font-semibold">
        {t("agents.mayMessage")}
      </p>
      <p className="text-muted-foreground text-xs">
        {t("agents.mayMessageHint")}
      </p>
      <label className="flex cursor-pointer items-center gap-2 select-none">
        <Checkbox
          checked={all}
          onCheckedChange={(v) => set(withAllAgents(allow, v === true))}
          aria-label={t("agents.mayMessageAll")}
        />
        <span className="text-xs">{t("agents.mayMessageAll")}</span>
      </label>
      {rows.length === 0 ? (
        <span className="text-muted-foreground text-xs">
          {t("agents.mayMessageNoOthers")}
        </span>
      ) : (
        <div className="grid grid-cols-2 gap-x-4 gap-y-0.5 md:grid-cols-3">
          {rows.map((r) => (
            <label
              key={`${r.configured ? "a" : "x"}:${r.id}`}
              className="flex cursor-pointer items-center gap-2 select-none"
            >
              <Checkbox
                checked={r.checked}
                disabled={all && r.configured}
                onCheckedChange={() => set(toggleAllowAgent(allow, r.id))}
                aria-label={r.label}
              />
              <span className="text-xs">{r.label}</span>
              {!r.configured && (
                <span className="text-muted-foreground text-xs">
                  {t("agents.mayMessageUnknown")}
                </span>
              )}
            </label>
          ))}
        </div>
      )}
      {messageGranted && allow.length === 0 && (
        <p
          data-testid="may-message-note"
          className="text-xs font-medium text-amber-400"
        >
          {t("agents.mayMessageNone", { name: agentName })}
        </p>
      )}
      {!listUsed && allow.length > 0 && (
        <p
          data-testid="may-message-note"
          className="text-xs font-medium text-amber-400"
        >
          {t("agents.mayMessageNoTool")}
        </p>
      )}
    </div>
  )
}
