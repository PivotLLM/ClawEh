import { IconAlertTriangle } from "@tabler/icons-react"
import { useTranslation } from "react-i18next"

import type { AgentToolCatalogResponse } from "@/api/channels"
import {
  SHELL_TOOL,
  shellAllowed,
  toolDenied,
  withCodingTools,
  withShellAllowed,
} from "@/components/agents/agent-model"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"

export interface ToolSelectProps {
  /** The agent's tools list; [] = none. */
  selected: string[]
  catalog: AgentToolCatalogResponse
  onChange: (tools: string[]) => void
  /** The agent's deny_tools: a ticked tool it blocks is marked on its row. */
  denyTools?: string[]
}

export function ToolSelect({
  selected,
  catalog,
  onChange,
  denyTools = [],
}: ToolSelectProps) {
  const { t } = useTranslation()
  // Only per-tool (native) tools are listed here — these are the tools always in
  // the agent's context. Suites (cogmem, maestro, fusion, forum) are
  // all-or-nothing and controlled by their own switches on the agent card, so
  // they are intentionally not shown.
  const perToolTools = catalog.tools.filter((tool) => !tool.suite)
  const shell = shellAllowed(selected)

  // MCP-client tools are not part of this per-tool allowlist; they have their
  // own per-tool mcp_tools field (see the MCP access box).
  // "*" stands for every listed tool but shell_exec, which only its own entry
  // grants.
  const starTools = perToolTools
    .map((tool) => tool.name)
    .filter((n) => n !== SHELL_TOOL)

  // When "*" is present, expand to full explicit list before applying any change.
  const effectiveSelected = (): string[] => {
    if (selected.includes("*"))
      return shell ? [...starTools, SHELL_TOOL] : starTools
    return selected
  }

  const isChecked = (name: string) =>
    name === SHELL_TOOL
      ? shell
      : selected.includes("*") || selected.includes(name)

  const handleToggle = (name: string) => {
    if (name === SHELL_TOOL) {
      onChange(withShellAllowed(selected, !shell))
      return
    }
    const current = effectiveSelected()
    if (current.includes(name)) {
      onChange(current.filter((s) => s !== name))
    } else {
      onChange([...current, name])
    }
  }

  // "Default tools" writes the standard set into the list, which stays
  // editable; it never adds shell_exec and leaves its box as it was.
  const handleDefault = () =>
    onChange(withShellAllowed(catalog.default_tools, shell))
  // "Coding tools" only adds: the coding set joins whatever is granted.
  const handleCoding = () => onChange(withCodingTools(selected))
  const handleClear = () => onChange([])

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
          {t("agents.defaultTools")}
        </Button>
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={handleCoding}
          className="h-6 px-2 text-xs"
        >
          {t("agents.codingTools")}
        </Button>
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={handleClear}
          className="h-6 px-2 text-xs"
        >
          {t("agents.clearTools")}
        </Button>
      </div>

      <div className="grid grid-cols-2 gap-x-4 gap-y-0.5 md:grid-cols-3">
        {[...perToolTools]
          .sort((a, b) => a.name.localeCompare(b.name))
          .map((tool) => {
            const checked = isChecked(tool.name)
            return (
              <div key={tool.name} data-testid={`tool-row-${tool.name}`}>
                <label
                  className="flex cursor-pointer items-center gap-2 select-none"
                  title={tool.description}
                >
                  <Checkbox
                    checked={checked}
                    onCheckedChange={() => handleToggle(tool.name)}
                    aria-label={tool.name}
                  />
                  <span className="font-mono text-xs">{tool.name}</span>
                  {tool.name === SHELL_TOOL && (
                    <span
                      title={t("agents.shellWarning")}
                      aria-label={t("agents.shellWarning")}
                      data-testid="shell-warning"
                    >
                      <IconAlertTriangle className="size-3.5 text-amber-500" />
                    </span>
                  )}
                </label>
                {checked && toolDenied(denyTools, tool.name) && (
                  <p className="text-xs font-medium text-amber-400">
                    {t("agents.toolDenied")}
                  </p>
                )}
              </div>
            )
          })}
      </div>

      {perToolTools.length === 0 && (
        <span className="text-muted-foreground text-xs">
          {t("agents.noToolsAvailable")}
        </span>
      )}
    </div>
  )
}
