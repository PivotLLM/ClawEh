import { IconEdit, IconKey, IconTrash } from "@tabler/icons-react"
import { useTranslation } from "react-i18next"

import type { ProviderInfo } from "@/api/providers"
import { isCliProtocol } from "@/components/providers/provider-config-fields"
import { Button } from "@/components/ui/button"

interface ProviderCardProps {
  provider: ProviderInfo
  onEdit: (provider: ProviderInfo) => void
  onDelete: (provider: ProviderInfo) => void
}

export function ProviderCard({
  provider,
  onEdit,
  onDelete,
}: ProviderCardProps) {
  const { t } = useTranslation()
  const cli = isCliProtocol(provider.protocol)
  // `ready` is the backend's answer, not a guess from the config: for a CLI
  // provider it means the binary actually resolves, which a blank command does
  // and a stale path does not.
  const detail = cli
    ? (provider.command ?? provider.resolved_command)
    : provider.base_url

  return (
    <div
      data-testid="provider-card"
      className="group/card hover:bg-muted/30 border-border/60 bg-card relative flex w-full max-w-[36rem] flex-col gap-3 justify-self-start rounded-xl border p-4 transition-colors hover:shadow-xs"
    >
      <div className="flex items-start justify-between gap-2">
        <div className="flex min-w-0 items-center gap-2">
          <span
            className={[
              "mt-0.5 h-2 w-2 shrink-0 rounded-full",
              provider.ready ? "bg-green-500" : "bg-muted-foreground/25",
            ].join(" ")}
          />
          <span className="text-foreground truncate text-sm font-semibold">
            {provider.name}
          </span>
          <span className="bg-muted text-muted-foreground shrink-0 rounded px-1.5 py-0.5 text-[10px] leading-none font-medium">
            {provider.protocol}
          </span>
        </div>

        <div className="flex shrink-0 items-center gap-0.5">
          <Button
            variant="ghost"
            size="icon-sm"
            onClick={() => onEdit(provider)}
            title={t("providers.action.edit")}
          >
            <IconEdit className="size-3.5" />
          </Button>

          <Button
            variant="ghost"
            size="icon-sm"
            onClick={() => onDelete(provider)}
            title={t("providers.action.delete")}
            className="text-muted-foreground hover:text-destructive hover:bg-destructive/10"
          >
            <IconTrash className="size-3.5" />
          </Button>
        </div>
      </div>

      {detail && (
        <p className="text-muted-foreground truncate font-mono text-xs leading-snug">
          {detail}
        </p>
      )}

      <div className="flex items-center justify-between gap-2">
        {provider.ready ? (
          <span className="text-muted-foreground/70 flex items-center gap-1 text-[11px]">
            {/* The key icon says how an HTTP provider is configured. A CLI
                provider has no key, so it gets the same label without it. */}
            {!cli && <IconKey className="size-3" />}
            {t("providers.status.configured")}
          </span>
        ) : (
          <span className="text-muted-foreground/50 text-[11px]">
            {t("providers.status.unconfigured")}
          </span>
        )}
        <span className="text-muted-foreground/70 text-[11px]">
          {t("providers.modelCount", { count: provider.model_count })}
        </span>
      </div>
    </div>
  )
}
