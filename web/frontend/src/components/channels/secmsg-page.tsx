import { IconLoader2, IconPlus, IconTrash } from "@tabler/icons-react"
import { useCallback, useEffect, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import { getAppConfig, getSecMsgAccounts, patchAppConfig } from "@/api/channels"
import { SecMsgForm } from "@/components/channels/channel-forms/secmsg-form"
import { PageHeader } from "@/components/page-header"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"

type DaemonConfig = Record<string, unknown>

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

function newEmptyDaemon(): DaemonConfig {
  return {
    name: "",
    enabled: true,
    address: "",
    accounts: [],
  }
}

interface DaemonCardProps {
  daemon: DaemonConfig
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

function DaemonCard({
  daemon,
  isExpanded,
  isNew,
  saving,
  onExpand,
  onCollapse,
  onChange,
  onToggleEnabled,
  onSave,
  onDelete,
}: DaemonCardProps) {
  const { t } = useTranslation()
  const configured = asString(daemon.address) !== ""
  const name = asString(daemon.name)
  const displayName = name === "" ? "secmsg" : name

  // Account count comes from the daemon live (not config): claw discovers the
  // daemon's linked accounts, so the card reflects what the daemon actually
  // hosts. null = still loading; "error" = daemon unreachable.
  const [liveCount, setLiveCount] = useState<number | "error" | null>(null)
  useEffect(() => {
    if (isNew) return
    let cancelled = false
    getSecMsgAccounts(displayName)
      .then((r) => {
        if (!cancelled) setLiveCount(r.accounts.length)
      })
      .catch(() => {
        if (!cancelled) setLiveCount("error")
      })
    return () => {
      cancelled = true
    }
  }, [displayName, isNew])

  const accountLabel =
    liveCount === null
      ? "…"
      : liveCount === "error"
        ? "daemon unreachable"
        : liveCount === 1
          ? "1 account"
          : `${liveCount} accounts`

  return (
    <div className="border-border/60 bg-card rounded-xl border">
      <div className="flex items-center gap-3 px-4 py-3">
        <span
          className={[
            "h-2 w-2 shrink-0 rounded-full",
            configured ? "bg-green-500" : "bg-muted-foreground/25",
          ].join(" ")}
          title={
            configured
              ? t("models.status.configured")
              : t("models.status.unconfigured")
          }
        />
        <span className="min-w-0 flex-1 truncate font-mono text-sm font-semibold">
          {isNew ? (
            <Input
              value={name}
              onChange={(e) => onChange("name", e.target.value)}
              placeholder="name (e.g. signal)"
              className="h-7 text-sm"
            />
          ) : (
            <span>
              {displayName}
              <span className="text-muted-foreground ml-2 font-sans text-xs font-normal">
                {accountLabel}
              </span>
            </span>
          )}
        </span>

        {!isNew && (
          <Switch
            checked={asBool(daemon.enabled)}
            onCheckedChange={onToggleEnabled}
            disabled={saving}
            aria-label={asBool(daemon.enabled) ? "Disable" : "Enable"}
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
        <div className="border-border/40 space-y-4 border-t px-4 py-4">
          {isNew && (
            <div className="flex items-center justify-between">
              <span className="text-sm font-medium">
                {t("channels.page.enableLabel")}
              </span>
              <Switch
                checked={asBool(daemon.enabled)}
                onCheckedChange={(v) => onChange("enabled", v)}
              />
            </div>
          )}

          <SecMsgForm
            daemonName={displayName}
            config={daemon}
            onChange={onChange}
            isNew={isNew}
          />

          <div className="border-border/40 flex justify-end gap-2 border-t pt-4">
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

export function SecMsgPage() {
  const { t } = useTranslation()
  const [loading, setLoading] = useState(true)
  const [fetchError, setFetchError] = useState("")
  const [daemons, setDaemons] = useState<DaemonConfig[]>([])
  const [expandedIndex, setExpandedIndex] = useState<number | null>(null)
  const [saving, setSaving] = useState(false)
  const [isAdding, setIsAdding] = useState(false)
  const [newDaemon, setNewDaemon] = useState<DaemonConfig>(newEmptyDaemon())

  const loadData = useCallback(async () => {
    setLoading(true)
    try {
      const appConfig = await getAppConfig()
      const channelsConfig = asRecord(asRecord(appConfig).channels)
      setDaemons(asArray(channelsConfig["secmsg"]).map(asRecord))
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

  const persist = async (list: DaemonConfig[], successMsg?: string) => {
    setSaving(true)
    try {
      await patchAppConfig({ channels: { secmsg: list } })
      if (successMsg) toast.success(successMsg)
      await loadData()
      return true
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t("channels.page.saveError"))
      return false
    } finally {
      setSaving(false)
    }
  }

  const handleSave = async () => {
    if (await persist(daemons, t("channels.page.saveSuccess"))) {
      setExpandedIndex(null)
    }
  }

  const handleChange = (index: number, key: string, value: unknown) => {
    setDaemons((prev) => {
      const next = [...prev]
      next[index] = { ...next[index], [key]: value }
      return next
    })
  }

  const handleToggleEnabled = async (index: number, enabled: boolean) => {
    await persist(daemons.map((d, i) => (i === index ? { ...d, enabled } : d)))
  }

  const handleDelete = async (index: number) => {
    if (
      await persist(
        daemons.filter((_, i) => i !== index),
        t("channels.page.saveSuccess"),
      )
    ) {
      setExpandedIndex(null)
    }
  }

  const handleAdd = async () => {
    if (await persist([...daemons, newDaemon], t("channels.page.saveSuccess"))) {
      setIsAdding(false)
      setNewDaemon(newEmptyDaemon())
    }
  }

  return (
    <div className="flex h-full flex-col">
      <PageHeader title={t("channels.name.secmsg")}>
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
          Add Daemon
        </Button>
      </PageHeader>

      <div className="min-h-0 flex-1 overflow-y-auto px-4 pb-8 sm:px-6">
        <div className="w-full max-w-250 space-y-3 pt-4">
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

          {!loading && !fetchError && daemons.length === 0 && !isAdding && (
            <p className="text-muted-foreground text-sm">
              No secure-messaging daemons configured. Add one to get started.
            </p>
          )}

          {!loading &&
            !fetchError &&
            daemons.map((daemon, i) => (
              <DaemonCard
                key={i}
                daemon={daemon}
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
                onChange={(key, value) => handleChange(i, key, value)}
                onToggleEnabled={(enabled) => handleToggleEnabled(i, enabled)}
                onSave={() => handleSave()}
                onDelete={() => handleDelete(i)}
              />
            ))}

          {isAdding && (
            <DaemonCard
              daemon={newDaemon}
              isExpanded={true}
              isNew={true}
              saving={saving}
              onExpand={() => {}}
              onCollapse={() => {
                setIsAdding(false)
                setNewDaemon(newEmptyDaemon())
              }}
              onChange={(key, value) =>
                setNewDaemon((prev) => ({ ...prev, [key]: value }))
              }
              onToggleEnabled={(enabled) =>
                setNewDaemon((prev) => ({ ...prev, enabled }))
              }
              onSave={handleAdd}
              onDelete={() => {
                setIsAdding(false)
                setNewDaemon(newEmptyDaemon())
              }}
            />
          )}
        </div>
      </div>
    </div>
  )
}
