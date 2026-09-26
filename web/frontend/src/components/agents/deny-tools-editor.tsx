import { IconPlus, IconTrash } from "@tabler/icons-react"
import { useTranslation } from "react-i18next"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"

interface DenyToolsEditorProps {
  value: string[]
  onChange: (value: string[]) => void
}

// DenyToolsEditor edits an agent's deny_tools as one free-text row per entry.
// Entries are tool names or patterns under the same matching rules as the
// allow lists (tools / mcp_tools); the page trims and drops blank rows on save.
export function DenyToolsEditor({ value, onChange }: DenyToolsEditorProps) {
  const { t } = useTranslation()
  return (
    <div className="space-y-1.5">
      {value.map((entry, i) => (
        <div key={i} className="flex items-center gap-1.5">
          <Input
            value={entry}
            onChange={(e) =>
              onChange(value.map((x, j) => (j === i ? e.target.value : x)))
            }
            placeholder={t("agents.denyToolsPlaceholder")}
            className="h-7 flex-1 font-mono text-xs"
          />
          <Button
            type="button"
            variant="outline"
            size="icon"
            className="h-7 w-7"
            aria-label={t("agents.denyToolsRemove")}
            onClick={() => onChange(value.filter((_, j) => j !== i))}
          >
            <IconTrash className="size-3.5" />
          </Button>
        </div>
      ))}
      <Button
        type="button"
        variant="outline"
        size="sm"
        className="h-6 px-2 text-xs"
        onClick={() => onChange([...value, ""])}
      >
        <IconPlus className="size-3.5" />
        {t("agents.denyToolsAdd")}
      </Button>
    </div>
  )
}
