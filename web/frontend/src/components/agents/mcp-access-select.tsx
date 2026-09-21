import { useState } from "react"

import {
  mcpAccessEntries,
  mcpAccessView,
  splitCsv,
} from "@/components/agents/agent-model"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"

interface MCPAccessSelectProps {
  serverNames: string[]
  value: string[]
  onChange: (entries: string[]) => void
}

// MCPAccessSelect edits an agent's mcp_tools as one checkbox per configured
// MCP server, plus a text field for prefixes that grant only part of a server
// (e.g. fusion_trello). An entry naming a server that is no longer configured
// stays visible, checked and flagged, so it can be removed.
export function MCPAccessSelect({
  serverNames,
  value,
  onChange,
}: MCPAccessSelectProps) {
  // Local copy so a toggle shows immediately; the parent's value catches up
  // after the debounced save. Resets per agent because the card is keyed.
  const [entries, setEntries] = useState(value)
  const view = mcpAccessView(entries, serverNames)
  // Raw text for the prefixes field, kept locally so typing commas and spaces
  // is not fought by a parse-on-every-keystroke round trip.
  const [extrasRaw, setExtrasRaw] = useState(view.extras.join(", "))

  const commit = (next: string[]) => {
    setEntries(next)
    onChange(next)
  }
  const toggle = (name: string) =>
    commit(
      mcpAccessEntries({
        ...view,
        servers: view.servers.map((s) =>
          s.name === name ? { ...s, checked: !s.checked } : s,
        ),
      }),
    )

  return (
    <div className="space-y-2">
      {view.servers.length > 0 ? (
        <div className="grid grid-cols-2 gap-x-4 gap-y-0.5 md:grid-cols-3">
          {view.servers.map((s) => (
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
      ) : (
        <span className="text-muted-foreground text-xs">
          No MCP servers configured
        </span>
      )}
      <div className="space-y-1">
        <label className="flex items-center gap-2">
          <span className="text-muted-foreground shrink-0 text-xs">
            Prefixes
          </span>
          <Input
            value={extrasRaw}
            onChange={(e) => {
              setExtrasRaw(e.target.value)
              commit(
                mcpAccessEntries({ ...view, extras: splitCsv(e.target.value) }),
              )
            }}
            placeholder="e.g. fusion_trello"
            className="h-7 flex-1 font-mono text-xs"
          />
        </label>
        <p className="text-muted-foreground text-xs">
          A checked server grants all of its tools. A prefix (comma-separated,
          case-insensitive) grants only the tools whose name starts with it.
          Nothing checked and no prefixes = no MCP tools.
        </p>
      </div>
    </div>
  )
}
