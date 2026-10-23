import type { AgentToolCatalogResponse } from "@/api/channels"
import {
  SHELL_TOOL,
  shellAllowed,
  withShellAllowed,
} from "@/components/agents/agent-model"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"

export interface ToolSelectProps {
  /** The agent's tools list; [] = none. */
  selected: string[]
  catalog: AgentToolCatalogResponse
  onChange: (tools: string[]) => void
}

export function ToolSelect({ selected, catalog, onChange }: ToolSelectProps) {
  // Only per-tool (native) tools are listed here — these are the tools always in
  // the agent's context. Suites (cogmem, maestro, fusion, forum) are
  // all-or-nothing and controlled by their own switches on the agent card, so
  // they are intentionally not shown.
  // shell_exec has its own "Allow shell commands" switch and is granted only by
  // name, so it is not listed here, and every change below keeps its entry.
  const perToolTools = catalog.tools.filter(
    (t) => !t.suite && t.name !== SHELL_TOOL,
  )
  const shell = shellAllowed(selected)
  const emit = (next: string[]) => onChange(withShellAllowed(next, shell))

  // MCP-client tools are not part of this per-tool allowlist; they have their
  // own per-tool mcp_tools field (see the MCP access box).
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
      emit(current.filter((s) => s !== name))
    } else {
      emit([...current, name])
    }
  }

  // "Default tools" writes the standard set into the list, which stays
  // editable; shell_exec keeps its own switch's setting.
  const handleDefault = () => emit(catalog.default_tools)
  const handleClear = () => emit([])

  return (
    <div className="space-y-2">
      <div className="flex items-center gap-1.5">
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={handleDefault}
          className="h-6 px-2 text-xs"
        >
          Default tools
        </Button>
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={handleClear}
          className="h-6 px-2 text-xs"
        >
          Clear
        </Button>
      </div>

      <div className="grid grid-cols-2 gap-x-4 gap-y-0.5 md:grid-cols-3">
        {[...perToolTools]
          .sort((a, b) => a.name.localeCompare(b.name))
          .map((tool) => (
            <label
              key={tool.name}
              className="flex cursor-pointer items-center gap-2 select-none"
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

      {perToolTools.length === 0 && (
        <span className="text-muted-foreground text-xs">
          No tools available
        </span>
      )}
    </div>
  )
}
