import type { AgentToolCatalogResponse } from "@/api/channels"
import { Checkbox } from "@/components/ui/checkbox"
import { Button } from "@/components/ui/button"

export interface ToolSelectProps {
  selected: string[]
  catalog: AgentToolCatalogResponse
  onChange: (tools: string[]) => void
}

export function ToolSelect({ selected, catalog, onChange }: ToolSelectProps) {
  const allTools = [
    ...catalog.tools.map((t) => t.name),
    ...(catalog.mcp_servers ?? []).map((s) => s.pattern),
  ]

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
  const hasMCPServers = (catalog.mcp_servers?.length ?? 0) > 0

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

      <div className="space-y-0.5">
        {[...catalog.tools].sort((a, b) => a.name.localeCompare(b.name)).map((tool) => (
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

      {hasMCPServers && (
        <div>
          <p className="text-xs font-semibold text-foreground mb-1">MCP Servers</p>
          <div className="space-y-0.5">
            {[...catalog.mcp_servers!].sort((a, b) => a.name.localeCompare(b.name)).map((server) => (
              <label
                key={server.name}
                className="flex items-center gap-2 cursor-pointer select-none"
                title={`Allow all tools from the "${server.name}" MCP server (${server.pattern})`}
              >
                <Checkbox
                  checked={isChecked(server.pattern)}
                  onCheckedChange={() => handleToggle(server.pattern)}
                />
                <span className="font-mono text-xs">{server.name}</span>
                <span className="text-muted-foreground text-xs">
                  ({server.pattern})
                </span>
              </label>
            ))}
          </div>
        </div>
      )}

      {catalog.tools.length === 0 && !hasMCPServers && (
        <span className="text-muted-foreground text-xs">No tools available</span>
      )}
    </div>
  )
}
