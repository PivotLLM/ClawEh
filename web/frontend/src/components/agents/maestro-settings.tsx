import { useTranslation } from "react-i18next"

import { type MaestroRunnerEdits } from "@/components/agents/agent-model"
import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"

// parseOptionalPositive maps an input value onto the block's convention: empty
// (or anything not a positive integer) means "use Maestro's default".
function parseOptionalPositive(raw: string): number | undefined {
  const n = parseInt(raw, 10)
  return Number.isInteger(n) && n > 0 ? n : undefined
}

export interface MaestroSettingsSectionProps {
  value: MaestroRunnerEdits
  onChange: (v: MaestroRunnerEdits) => void
}

/** MaestroSettingsSection edits the runner settings of an enabled Maestro
 *  block: concurrency cap, dispatch rate limit and whether the LLM may request
 *  parallel runs. Empty fields mean Maestro's defaults. */
export function MaestroSettingsSection({
  value,
  onChange,
}: MaestroSettingsSectionProps) {
  const { t } = useTranslation()
  return (
    <div
      className="mt-2 space-y-2 border-l-2 pl-3"
      data-testid="maestro-settings"
    >
      <div className="flex items-center gap-2">
        <Input
          type="number"
          min={1}
          placeholder="5"
          value={value.maxConcurrent ?? ""}
          aria-label={t("agents.maestroMaxConcurrent")}
          onChange={(e) =>
            onChange({
              ...value,
              maxConcurrent: parseOptionalPositive(e.target.value),
            })
          }
          className="h-7 w-20 text-xs"
        />
        <span className="text-muted-foreground text-xs">
          {t("agents.maestroMaxConcurrentHint")}
        </span>
      </div>
      <div className="flex items-center gap-2">
        <Input
          type="number"
          min={1}
          placeholder="10"
          value={value.rateLimitRequests ?? ""}
          aria-label={t("agents.maestroRateLimitRequests")}
          onChange={(e) =>
            onChange({
              ...value,
              rateLimitRequests: parseOptionalPositive(e.target.value),
            })
          }
          className="h-7 w-20 text-xs"
        />
        <span className="text-muted-foreground text-xs">
          {t("agents.maestroRateLimitRequestsHint")}
        </span>
        <Input
          type="number"
          min={1}
          placeholder="60"
          value={value.rateLimitPeriod ?? ""}
          aria-label={t("agents.maestroRateLimitPeriod")}
          onChange={(e) =>
            onChange({
              ...value,
              rateLimitPeriod: parseOptionalPositive(e.target.value),
            })
          }
          className="h-7 w-20 text-xs"
        />
        <span className="text-muted-foreground text-xs">
          {t("agents.maestroRateLimitPeriodHint")}
        </span>
      </div>
      <div className="flex items-center justify-between gap-2">
        <p className="text-foreground text-xs">
          {t("agents.maestroAllowParallel")}
        </p>
        <Switch
          checked={value.allowParallel}
          onCheckedChange={(v) => onChange({ ...value, allowParallel: v })}
          aria-label={t("agents.maestroAllowParallel")}
        />
      </div>
      <p className="text-muted-foreground text-xs">
        {t("agents.maestroAllowParallelHint")}
      </p>
    </div>
  )
}
