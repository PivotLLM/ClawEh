import type { ModelInfo } from "@/api/models"
import { Button } from "@/components/ui/button"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"

// Shared model pickers used by the Agents page (per-agent cards) and the Config
// page (agent-defaults card). Pure presentational components — no data loading.

interface ModelSelectProps {
  value: string
  models: ModelInfo[]
  onChange: (v: string) => void
  placeholder?: string
}

export function ModelSelect({ value, models, onChange, placeholder }: ModelSelectProps) {
  const configured = models
    .filter((m) => m.configured && m.enabled)
    .sort((a, b) => a.model_name.localeCompare(b.model_name))
  const selectedModel = models.find((m) => m.model_name === value)
  const noToolsWarning = selectedModel?.no_tools === true
  return (
    <div className="space-y-1.5">
      <Select value={value || "__none__"} onValueChange={(v) => onChange(v === "__none__" ? "" : v)}>
        <SelectTrigger className="w-full">
          <SelectValue placeholder={placeholder ?? "Select model"} />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="__none__">{placeholder ?? "No model override"}</SelectItem>
          {configured.map((m) => (
            <SelectItem key={m.index} value={m.model_name}>
              {m.model_name}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      {noToolsWarning && (
        <p className="text-amber-600 dark:text-amber-400 text-xs flex items-center gap-1">
          <span>&#9888;</span>
          Tools are disabled for {selectedModel.model_name}
        </p>
      )}
    </div>
  )
}

interface FallbacksSelectProps {
  fallbacks: string[]
  primary: string
  models: ModelInfo[]
  onChange: (fallbacks: string[]) => void
  addPlaceholder?: string
}

export function FallbacksSelect({ fallbacks, primary, models, onChange, addPlaceholder }: FallbacksSelectProps) {
  const available = models
    .filter((m) => m.configured && m.enabled && m.model_name !== primary)
    .sort((a, b) => a.model_name.localeCompare(b.model_name))

  const moveUp = (i: number) => {
    if (i === 0) return
    const next = [...fallbacks]
    ;[next[i - 1], next[i]] = [next[i], next[i - 1]]
    onChange(next)
  }

  const remove = (i: number) => {
    onChange(fallbacks.filter((_, idx) => idx !== i))
  }

  const add = (name: string) => {
    if (!name || fallbacks.includes(name)) return
    onChange([...fallbacks, name])
  }

  const remaining = available.filter((m) => !fallbacks.includes(m.model_name))

  return (
    <div className="space-y-1.5">
      {fallbacks.map((fb, i) => (
        <div key={fb} className="flex items-center gap-1.5">
          <span className="text-muted-foreground w-4 text-center text-xs">{i + 1}</span>
          <span className="border-border/50 bg-muted/40 flex-1 rounded px-2 py-1 font-mono text-xs">
            {fb}
          </span>
          <Button
            variant="ghost"
            size="icon-sm"
            onClick={() => moveUp(i)}
            disabled={i === 0}
            className="text-muted-foreground size-6"
            title="Move up"
          >
            ↑
          </Button>
          <Button
            variant="ghost"
            size="icon-sm"
            onClick={() => remove(i)}
            className="text-muted-foreground hover:text-destructive size-6"
            title="Remove"
          >
            ×
          </Button>
        </div>
      ))}
      {remaining.length > 0 && (
        <Select value="" onValueChange={add}>
          <SelectTrigger className="h-7 text-xs">
            <SelectValue placeholder={addPlaceholder ?? "Add model…"} />
          </SelectTrigger>
          <SelectContent>
            {remaining.map((m) => (
              <SelectItem key={m.index} value={m.model_name}>
                {m.model_name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      )}
      {fallbacks.length === 0 && remaining.length === 0 && (
        <p className="text-muted-foreground text-xs">No other configured models available.</p>
      )}
    </div>
  )
}
