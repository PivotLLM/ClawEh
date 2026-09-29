import { useState } from "react"

import {
  fusionAccessView,
  mcpAccessEntries,
  mcpAccessView,
  type MCPAccessServer,
} from "@/components/agents/agent-model"
import { Checkbox } from "@/components/ui/checkbox"

interface MCPAccessSelectProps {
  serverNames: string[]
  fusionServices: string[]
  value: string[]
  onChange: (entries: string[]) => void
}

// MCPAccessSelect edits an agent's mcp_tools as two checkbox lists over the one
// entry list: MCP access, one box per configured MCP server, and Fusion
// services, one box per Fusion service (applied when the agent's Fusion switch
// is on). Checked grants every tool of the server or service. An entry that
// names neither stays visible under MCP access, checked and flagged, so it can
// be removed.
export function MCPAccessSelect({
  serverNames,
  fusionServices,
  value,
  onChange,
}: MCPAccessSelectProps) {
  // Local copy so a toggle shows immediately; the parent's value catches up
  // after the debounced save. Resets per agent because the card is keyed.
  const [entries, setEntries] = useState(value)
  const mcpRows = mcpAccessView(entries, serverNames, fusionServices)
  const fusionRows = fusionAccessView(entries, fusionServices)

  const flip = (rows: MCPAccessServer[], name: string) =>
    rows.map((s) => (s.name === name ? { ...s, checked: !s.checked } : s))
  const toggleMCP = (name: string) => {
    const next = mcpAccessEntries([...flip(mcpRows, name), ...fusionRows])
    setEntries(next)
    onChange(next)
  }
  const toggleFusion = (name: string) => {
    const next = mcpAccessEntries([...mcpRows, ...flip(fusionRows, name)])
    setEntries(next)
    onChange(next)
  }

  return (
    <div className="space-y-3">
      <div className="space-y-1.5">
        <p className="text-foreground text-xs font-semibold">MCP access</p>
        {mcpRows.length === 0 ? (
          <span className="text-muted-foreground text-xs">
            No MCP servers configured
          </span>
        ) : (
          <>
            <AccessGrid rows={mcpRows} onToggle={toggleMCP} />
            <p className="text-muted-foreground text-xs">
              A checked server grants all of its tools. Nothing checked = no
              MCP tools.
            </p>
          </>
        )}
      </div>
      {fusionRows.length > 0 && (
        <div className="space-y-1.5">
          <p className="text-foreground text-xs font-semibold">
            Fusion services
          </p>
          <AccessGrid rows={fusionRows} onToggle={toggleFusion} />
          <p className="text-muted-foreground text-xs">
            A checked service grants all of its tools when Fusion is on.
            Nothing checked = no Fusion tools.
          </p>
        </div>
      )}
    </div>
  )
}

function AccessGrid({
  rows,
  onToggle,
}: {
  rows: MCPAccessServer[]
  onToggle: (name: string) => void
}) {
  return (
    <div className="grid grid-cols-2 gap-x-4 gap-y-0.5 md:grid-cols-3">
      {rows.map((s) => (
        <label
          key={s.name}
          className="flex cursor-pointer items-center gap-2 select-none"
        >
          <Checkbox
            checked={s.checked}
            onCheckedChange={() => onToggle(s.name)}
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
  )
}
