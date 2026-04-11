import { IconLoader2, IconPlus, IconTrash } from "@tabler/icons-react"
import { useCallback, useEffect, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import { getAppConfig, patchAppConfig } from "@/api/channels"
import { TelegramForm } from "@/components/channels/channel-forms/telegram-form"
import { PageHeader } from "@/components/page-header"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"

type BotConfig = Record<string, unknown>

function asArray(value: unknown): unknown[] {
  return Array.isArray(value) ? value : []
}

function asRecord(value: unknown): Record<string, unknown> {
  if (value && typeof value === "object" && !Array.isArray(value)) {
    return value as Record<string, unknown>
  }
  return {}
}

function asString(value: unknown): string {
  return typeof value === "string" ? value : ""
}

function asBool(value: unknown): boolean {
  return value === true
}

function botToEditState(bot: unknown): BotConfig {
  const b = asRecord(bot)
  return { ...b, _token: "" }
}

function editStateToBot(state: BotConfig): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  for (const [key, value] of Object.entries(state)) {
    if (key === "_token") continue
    if (key === "token") {
      const incoming = asString(state["_token"])
      out["token"] = incoming !== "" ? incoming : value
      continue
    }
    out[key] = value
  }
  return out
}

function newEmptyBot(): BotConfig {
  return {
    id: "",
    enabled: true,
    token: "",
    _token: "",
    base_url: "",
    proxy: "",
    allow_from: [],
    typing: { enabled: true },
    placeholder: { enabled: true, text: "Thinking... 💭" },
  }
}

interface BotCardProps {
  bot: BotConfig
  isExpanded: boolean
  isNew: boolean
  saving: boolean
  onExpand: () => void
  onCollapse: () => void
  onChange: (key: string, value: unknown) => void
  onToggleEnabled: (enabled: boolean) => void
  onSave: () => void
  onDelete: () => void
}

function BotCard({
  bot,
  isExpanded,
  isNew,
  saving,
  onExpand,
  onCollapse,
  onChange,
  onToggleEnabled,
  onSave,
  onDelete,
}: BotCardProps) {
  const { t } = useTranslation()
  const configured = asString(bot.token) !== ""
  const botId = asString(bot.id)
  const displayId = botId === "" || botId === "default" ? "default" : botId

  return (
    <div className="border-border/60 bg-card rounded-xl border">
      <div className="flex items-center gap-3 px-4 py-3">
        <span
          className={[
            "h-2 w-2 shrink-0 rounded-full",
            configured ? "bg-green-500" : "bg-muted-foreground/25",
          ].join(" ")}
          title={configured ? t("models.status.configured") : t("models.status.unconfigured")}
        />
        <span className="min-w-0 flex-1 truncate font-mono text-sm font-semibold">
          {isNew ? (
            <Input
              value={botId}
              onChange={(e) => onChange("id", e.target.value)}
              placeholder="bot-id (leave blank for default)"
              className="h-7 text-sm"
            />
          ) : (
            <span>telegram-{displayId}</span>
          )}
        </span>

        {!isNew && (
          <Switch
            checked={asBool(bot.enabled)}
            onCheckedChange={onToggleEnabled}
            disabled={saving}
            aria-label={asBool(bot.enabled) ? "Disable bot" : "Enable bot"}
          />
        )}

        {!isExpanded && (
          <Button variant="outline" size="sm" onClick={onExpand}>
            {t("common.edit")}
          </Button>
        )}

        <Button
          variant="ghost"
          size="icon-sm"
          onClick={onDelete}
          className="text-muted-foreground hover:text-destructive hover:bg-destructive/10"
          title={t("models.action.delete")}
        >
          <IconTrash className="size-3.5" />
        </Button>
      </div>

      {isExpanded && (
        <div className="border-border/40 border-t px-4 py-4 space-y-4">
          {isNew && (
            <div className="flex items-center justify-between">
              <span className="text-sm font-medium">{t("channels.page.enableLabel")}</span>
              <Switch
                checked={asBool(bot.enabled)}
                onCheckedChange={(v) => onChange("enabled", v)}
              />
            </div>
          )}

          <TelegramForm
            config={bot}
            onChange={onChange}
            isEdit={configured && !isNew}
          />

          <div className="flex justify-end gap-2 border-t border-border/40 pt-4">
            <Button variant="outline" onClick={onCollapse} disabled={saving}>
              {t("common.cancel")}
            </Button>
            <Button onClick={onSave} disabled={saving}>
              {saving ? t("common.saving") : t("common.save")}
            </Button>
          </div>
        </div>
      )}
    </div>
  )
}

export function TelegramBotsPage() {
  const { t } = useTranslation()
  const [loading, setLoading] = useState(true)
  const [fetchError, setFetchError] = useState("")
  const [bots, setBots] = useState<BotConfig[]>([])
  const [expandedIndex, setExpandedIndex] = useState<number | null>(null)
  const [saving, setSaving] = useState(false)
  const [isAdding, setIsAdding] = useState(false)
  const [newBot, setNewBot] = useState<BotConfig>(newEmptyBot())

  const loadData = useCallback(async () => {
    setLoading(true)
    try {
      const appConfig = await getAppConfig()
      const channelsConfig = asRecord(asRecord(appConfig).channels)
      const rawBots = asArray(channelsConfig["telegram"])
      setBots(rawBots.map(botToEditState))
      setFetchError("")
    } catch (e) {
      setFetchError(e instanceof Error ? e.message : t("channels.loadError"))
    } finally {
      setLoading(false)
    }
  }, [t])

  useEffect(() => {
    void loadData()
  }, [loadData])

  const buildBotsPayload = (editedBots: BotConfig[]) =>
    editedBots.map(editStateToBot)

  const handleSaveBot = async () => {
    const updated = [...bots]
    setSaving(true)
    try {
      await patchAppConfig({
        channels: { telegram: buildBotsPayload(updated) },
      })
      toast.success(t("channels.page.saveSuccess"))
      setExpandedIndex(null)
      await loadData()
    } catch (e) {
      const message = e instanceof Error ? e.message : t("channels.page.saveError")
      toast.error(message)
    } finally {
      setSaving(false)
    }
  }

  const handleBotChange = (index: number, key: string, value: unknown) => {
    setBots((prev) => {
      const next = [...prev]
      next[index] = { ...next[index], [key]: value }
      return next
    })
  }

  const handleToggleBotEnabled = async (index: number, enabled: boolean) => {
    const updated = bots.map((b, i) =>
      i === index ? { ...b, enabled } : b
    )
    setSaving(true)
    try {
      await patchAppConfig({
        channels: { telegram: buildBotsPayload(updated) },
      })
      await loadData()
    } catch (e) {
      const message = e instanceof Error ? e.message : t("channels.page.saveError")
      toast.error(message)
    } finally {
      setSaving(false)
    }
  }

  const handleDeleteBot = async (index: number) => {
    const updated = bots.filter((_, i) => i !== index)
    setSaving(true)
    try {
      await patchAppConfig({
        channels: { telegram: buildBotsPayload(updated) },
      })
      toast.success(t("channels.page.saveSuccess"))
      setExpandedIndex(null)
      await loadData()
    } catch (e) {
      const message = e instanceof Error ? e.message : t("channels.page.saveError")
      toast.error(message)
    } finally {
      setSaving(false)
    }
  }

  const handleAddBot = async () => {
    const allBots = [...bots.map(editStateToBot), editStateToBot(newBot)]
    setSaving(true)
    try {
      await patchAppConfig({
        channels: { telegram: allBots },
      })
      toast.success(t("channels.page.saveSuccess"))
      setIsAdding(false)
      setNewBot(newEmptyBot())
      await loadData()
    } catch (e) {
      const message = e instanceof Error ? e.message : t("channels.page.saveError")
      toast.error(message)
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="flex h-full flex-col">
      <PageHeader title={t("channels.name.telegram")}>
        <Button
          size="sm"
          variant="outline"
          onClick={() => {
            setIsAdding(true)
            setExpandedIndex(null)
          }}
          disabled={isAdding}
        >
          <IconPlus className="size-4" />
          Add Bot
        </Button>
      </PageHeader>

      <div className="min-h-0 flex-1 overflow-y-auto px-4 pb-8 sm:px-6">
        <div className="mx-auto w-full max-w-250 pt-4 space-y-3">
          {loading && (
            <div className="flex items-center justify-center py-20">
              <IconLoader2 className="text-muted-foreground size-6 animate-spin" />
            </div>
          )}

          {fetchError && (
            <div className="text-destructive bg-destructive/10 rounded-lg px-4 py-3 text-sm">
              {fetchError}
            </div>
          )}

          {!loading && !fetchError && bots.length === 0 && !isAdding && (
            <p className="text-muted-foreground text-sm">
              No Telegram bots configured. Add one to get started.
            </p>
          )}

          {!loading &&
            !fetchError &&
            bots.map((bot, i) => (
              <BotCard
                key={i}
                bot={bot}
                isExpanded={expandedIndex === i}
                isNew={false}
                saving={saving}
                onExpand={() => {
                  setExpandedIndex(i)
                  setIsAdding(false)
                }}
                onCollapse={() => {
                  setExpandedIndex(null)
                  void loadData()
                }}
                onChange={(key, value) => handleBotChange(i, key, value)}
                onToggleEnabled={(enabled) => handleToggleBotEnabled(i, enabled)}
                onSave={() => handleSaveBot()}
                onDelete={() => handleDeleteBot(i)}
              />
            ))}

          {isAdding && (
            <BotCard
              bot={newBot}
              isExpanded={true}
              isNew={true}
              saving={saving}
              onExpand={() => {}}
              onCollapse={() => {
                setIsAdding(false)
                setNewBot(newEmptyBot())
              }}
              onChange={(key, value) =>
                setNewBot((prev) => ({ ...prev, [key]: value }))
              }
              onToggleEnabled={(enabled) =>
                setNewBot((prev) => ({ ...prev, enabled }))
              }
              onSave={handleAddBot}
              onDelete={() => {
                setIsAdding(false)
                setNewBot(newEmptyBot())
              }}
            />
          )}
        </div>
      </div>
    </div>
  )
}
