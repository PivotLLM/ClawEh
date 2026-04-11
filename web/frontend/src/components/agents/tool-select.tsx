import type { AgentToolCatalogResponse } from "@/api/channels"

export interface ToolSelectProps {
  selected: string[]
  catalog: AgentToolCatalogResponse
  onChange: (tools: string[]) => void
}

export function ToolSelect({ selected, catalog, onChange }: ToolSelectProps) {
  const allActive = selected.includes("*")

  const handleAllToggle = () => {
    onChange(allActive ? [] : ["*"])
  }

  const handleToolToggle = (name: string) => {
    if (selected.includes(name)) {
      onChange(selected.filter((s) => s !== name))
    } else {
      onChange([...selected, name])
    }
  }

  const handleServerToggle = (pattern: string) => {
    if (selected.includes(pattern)) {
      onChange(selected.filter((s) => s !== pattern))
    } else {
      onChange([...selected, pattern])
    }
  }

  const isToolChecked = (name: string) => allActive || selected.includes(name)
  const isServerChecked = (pattern: string) => allActive || selected.includes(pattern)

  const noneSelected = selected.length === 0
  const hasMCPServers = (catalog.mcp_servers?.length ?? 0) > 0

  return (
    <div className="space-y-3">
      {/* Deny-by-default notice */}
      {noneSelected && (
        <p className="text-amber-400 text-xs font-medium">
          No tools selected — this agent has no tool access. Select specific tools or enable "All Tools."
        </p>
      )}

      {/* All Tools toggle */}
      <label className="flex items-center gap-2 cursor-pointer select-none">
        <input
          type="checkbox"
          checked={allActive}
          onChange={handleAllToggle}
          className="accent-primary"
        />
        <span className="text-xs font-semibold">All Tools</span>
        <span className="text-muted-foreground text-xs">(grants access to every tool via *)</span>
      </label>

      {/* Built-in tools — flat list */}
      <div className={["space-y-0.5", allActive ? "opacity-50 pointer-events-none" : ""].join(" ").trim()}>
        {catalog.tools.map((tool) => (
          <label
            key={tool.name}
            className="flex items-center gap-2 cursor-pointer select-none"
            title={tool.description}
          >
            <input
              type="checkbox"
              checked={isToolChecked(tool.name)}
              onChange={() => handleToolToggle(tool.name)}
              className="accent-primary"
            />
            <span className="font-mono text-xs">{tool.name}</span>
          </label>
        ))}
      </div>

      {/* MCP servers — one checkbox per server, grants mcp_name_* access */}
      {hasMCPServers && (
        <div className={allActive ? "opacity-50 pointer-events-none" : ""}>
          <p className="text-xs font-semibold text-foreground mb-1">MCP Servers</p>
          <div className="ml-0 space-y-0.5">
            {catalog.mcp_servers!.map((server) => (
              <label
                key={server.name}
                className="flex items-center gap-2 cursor-pointer select-none"
                title={`Allow all tools from the "${server.name}" MCP server (${server.pattern})`}
              >
                <input
                  type="checkbox"
                  checked={isServerChecked(server.pattern)}
                  onChange={() => handleServerToggle(server.pattern)}
                  className="accent-primary"
                />
                <span className="font-mono text-xs">{server.name}</span>
                <span className="text-muted-foreground text-xs">({server.pattern})</span>
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
