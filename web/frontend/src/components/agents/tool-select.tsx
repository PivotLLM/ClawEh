import type { AgentToolCatalogResponse } from "@/api/channels"
import { Checkbox } from "@/components/ui/checkbox"
import { Button } from "@/components/ui/button"

export interface ToolSelectProps {
  selected: string[]
  catalog: AgentToolCatalogResponse
  onChange: (tools: string[]) => void
  // suiteStates maps a suite name (e.g. "maestro", "cogmem") to whether it is
  // enabled for this agent, so the greyed suite rows reflect the live toggle.
  suiteStates?: Record<string, boolean>
}

export function ToolSelect({ selected, catalog, onChange, suiteStates }: ToolSelectProps) {
  // Suite entries (cogmem, maestro) are all-or-nothing and controlled by the
  // agent's per-suite toggle — they are not part of the per-tool allowlist.
  const perToolTools = catalog.tools.filter((t) => !t.suite)
  const suiteTools = catalog.tools.filter((t) => t.suite)

  // MCP-client tools are no longer part of this per-tool allowlist; they have
  // their own per-tool mcp_tools field (see the MCP access box).
  const allTools = perToolTools.map((t) => t.name)

  // When "*" is present, expand to full explicit list before applying any change.
  const effectiveSelected = (): string[] => {
    if (selected.includes("*")) return allTools
    return selected
  }

  const isChecked = (name: string) =>
    selected.includes("*") || selected.includes(name)

  const handleToggle = (name: string) => {
    const current = effectiveSelected()
    if (current.includes(name)) {
      onChange(current.filter((s) => s !== name))
    } else {
      onChange([...current, name])
    }
  }

  const handleDefault = () => onChange([...catalog.default_tools])
  const handleClear = () => onChange([])

  const noneSelected = selected.length === 0

  return (
    <div className="space-y-2">
      <div className="flex items-center gap-1.5">
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={handleDefault}
          className="h-6 text-xs px-2"
        >
          Default
        </Button>
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={handleClear}
          className="h-6 text-xs px-2"
        >
          Clear
        </Button>
      </div>

      {noneSelected && (
        <p className="text-amber-400 text-xs font-medium">
          No tools selected — this agent has no tool access.
        </p>
      )}

      <div className="grid grid-cols-2 gap-x-4 gap-y-0.5 md:grid-cols-3">
        {[...perToolTools].sort((a, b) => a.name.localeCompare(b.name)).map((tool) => (
          <label
            key={tool.name}
            className="flex items-center gap-2 cursor-pointer select-none"
            title={tool.description}
          >
            <Checkbox
              checked={isChecked(tool.name)}
              onCheckedChange={() => handleToggle(tool.name)}
            />
            <span className="font-mono text-xs">{tool.name}</span>
          </label>
        ))}
      </div>

      {suiteTools.length > 0 && (
        <div className="space-y-0.5">
          {[...suiteTools].sort((a, b) => a.name.localeCompare(b.name)).map((tool) => {
            const on = !!suiteStates?.[tool.suite ?? ""]
            return (
              <label
                key={tool.name}
                className="flex items-center gap-2 select-none opacity-50 cursor-not-allowed"
                title={`${tool.description} — ${on ? "enabled" : "disabled"} via the agent's ${tool.suite} toggle, not this list.`}
              >
                <Checkbox checked={on} disabled />
                <span className="font-mono text-xs">{tool.name}</span>
                <span className="text-muted-foreground text-xs">
                  (suite toggle — {on ? "on" : "off"})
                </span>
              </label>
            )
          })}
        </div>
      )}

      {perToolTools.length === 0 && suiteTools.length === 0 && (
        <span className="text-muted-foreground text-xs">No tools available</span>
      )}
    </div>
  )
}
