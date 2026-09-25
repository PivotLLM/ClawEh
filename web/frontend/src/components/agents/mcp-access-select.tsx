import { useState } from "react"

import {
  mcpAccessEntries,
  mcpAccessView,
} from "@/components/agents/agent-model"
import { Checkbox } from "@/components/ui/checkbox"

interface MCPAccessSelectProps {
  serverNames: string[]
  value: string[]
  onChange: (entries: string[]) => void
}

// MCPAccessSelect edits an agent's mcp_tools as one checkbox per configured
// MCP server: checked grants every tool the server publishes. An entry that
// names no configured server stays visible, checked and flagged, so it can be
// removed.
export function MCPAccessSelect({
  serverNames,
  value,
  onChange,
}: MCPAccessSelectProps) {
  // Local copy so a toggle shows immediately; the parent's value catches up
  // after the debounced save. Resets per agent because the card is keyed.
  const [entries, setEntries] = useState(value)
  const rows = mcpAccessView(entries, serverNames)

  const toggle = (name: string) => {
    const next = mcpAccessEntries(
      rows.map((s) => (s.name === name ? { ...s, checked: !s.checked } : s)),
    )
    setEntries(next)
    onChange(next)
  }

  if (rows.length === 0) {
    return (
      <span className="text-muted-foreground text-xs">
        No MCP servers configured
      </span>
    )
  }

  return (
    <div className="space-y-1.5">
      <div className="grid grid-cols-2 gap-x-4 gap-y-0.5 md:grid-cols-3">
        {rows.map((s) => (
          <label
            key={s.name}
            className="flex cursor-pointer items-center gap-2 select-none"
          >
            <Checkbox
              checked={s.checked}
              onCheckedChange={() => toggle(s.name)}
            />
            <span className="font-mono text-xs">{s.name}</span>
            {!s.configured && (
              <span className="text-muted-foreground text-xs">
                (not configured)
              </span>
            )}
          </label>
        ))}
      </div>
      <p className="text-muted-foreground text-xs">
        A checked server grants all of its tools. Nothing checked = no MCP
        tools.
      </p>
    </div>
  )
}
