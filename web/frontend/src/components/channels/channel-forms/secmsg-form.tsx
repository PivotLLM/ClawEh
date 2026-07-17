import { IconPlus, IconTrash } from "@tabler/icons-react"
import { useEffect, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import {
  type ChannelConfig,
  getSecMsgAccounts,
  getSecMsgLinkStatus,
  requestSecMsgLink,
  type SecMsgLinkStatus,
} from "@/api/channels"
import { Field, SwitchCardField } from "@/components/shared-form"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"

function asString(value: unknown): string {
  return typeof value === "string" ? value : ""
}

function asArray(value: unknown): unknown[] {
  return Array.isArray(value) ? value : []
}

function asStringArray(value: unknown): string[] {
  if (!Array.isArray(value)) return []
  return value.filter((item): item is string => typeof item === "string")
}

function asBool(value: unknown): boolean {
  return value === true
}

function asRecord(value: unknown): Record<string, unknown> {
  if (value && typeof value === "object" && !Array.isArray(value)) {
    return value as Record<string, unknown>
  }
  return {}
}

// accountChannelName mirrors config.SecMsgAccountConfig.ChannelName so the link
// panel targets the same channel name the backend registers.
function accountChannelName(daemonName: string, account: string): string {
  const base = daemonName === "" ? "secmsg" : daemonName
  return account === "" ? base : `${base}-${account}`
}

// SecMsgLinkPanel drives device pairing for one saved, running account channel.
function SecMsgLinkPanel({ channelName }: { channelName: string }) {
  const [status, setStatus] = useState<SecMsgLinkStatus | null>(null)
  const [busy, setBusy] = useState(false)

  const start = async () => {
    setBusy(true)
    try {
      setStatus(await requestSecMsgLink(channelName))
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Link request failed")
    } finally {
      setBusy(false)
    }
  }

  // Poll while a pairing is pending; the daemon flips status once the code is
  // scanned or the attempt fails.
  useEffect(() => {
    if (status?.status !== "pending") return
    const id = setInterval(() => {
      void getSecMsgLinkStatus(channelName)
        .then((s) => {
          setStatus(s)
          if (s.status === "complete") toast.success("Device linked")
        })
        .catch(() => {
          // transient daemon/socket hiccup — keep polling
        })
    }, 3000)
    return () => clearInterval(id)
  }, [status?.status, channelName])

  return (
    <div className="space-y-2">
      <Button
        variant="outline"
        size="sm"
        onClick={() => void start()}
        disabled={busy}
      >
        {status?.qr_png ? "Regenerate QR" : "Link device"}
      </Button>
      {status?.status === "complete" && (
        <p className="text-sm text-green-600">
          Linked{status.phone ? ` (${status.phone})` : ""}.
        </p>
      )}
      {status?.status === "error" && (
        <p className="text-destructive text-sm">
          {status.error || "Linking failed."}
        </p>
      )}
      {status?.status === "pending" && status.qr_png && (
        <div className="space-y-2">
          <img
            src={status.qr_png}
            alt="Pairing QR code"
            className="border-border h-56 w-56 rounded border bg-white p-2"
          />
          <p className="text-muted-foreground text-xs">
            Scan this from the app, then wait for confirmation.
          </p>
        </div>
      )}
    </div>
  )
}

interface AccountRowProps {
  daemonName: string
  account: Record<string, unknown>
  canLink: boolean
  onChange: (key: string, value: unknown) => void
  onRemove: () => void
}

function AccountRow({
  daemonName,
  account,
  canLink,
  onChange,
  onRemove,
}: AccountRowProps) {
  const { t } = useTranslation()
  const groupTrigger = asRecord(account.group_trigger)
  const accountId = asString(account.account)
  const derivedName = accountChannelName(daemonName, accountId)
  const channelName = asString(account.name) || derivedName

  return (
    <div className="border-border/60 bg-muted/20 space-y-4 rounded-lg border p-4">
      <div className="flex items-center justify-between gap-2">
        <span className="text-muted-foreground font-mono text-xs">
          {channelName}
        </span>
        <Button
          variant="ghost"
          size="icon-sm"
          onClick={onRemove}
          className="text-muted-foreground hover:text-destructive hover:bg-destructive/10"
          title={t("models.action.delete")}
        >
          <IconTrash className="size-3.5" />
        </Button>
      </div>

      <Field
        label={t("channels.field.secmsgAccount")}
        hint={t("channels.form.desc.secmsgAccount")}
      >
        <Input
          value={accountId}
          onChange={(e) => onChange("account", e.target.value)}
          placeholder="droid1"
        />
      </Field>

      <Field
        label={t("channels.field.secmsgName")}
        hint={t("channels.form.desc.secmsgName")}
      >
        <Input
          value={asString(account.name)}
          onChange={(e) => onChange("name", e.target.value)}
          placeholder={derivedName}
        />
      </Field>

      <Field
        label={t("channels.field.allowFrom")}
        hint={t("channels.form.desc.allowFrom")}
      >
        <Input
          value={asStringArray(account.allow_from).join(", ")}
          onChange={(e) =>
            onChange(
              "allow_from",
              e.target.value
                .split(",")
                .map((s: string) => s.trim())
                .filter(Boolean),
            )
          }
          placeholder={t("channels.field.allowFromPlaceholder")}
        />
      </Field>

      <SwitchCardField
        label={t("channels.field.mentionOnly")}
        hint={t("channels.form.desc.mentionOnly")}
        checked={asBool(groupTrigger.mention_only)}
        onCheckedChange={(checked) =>
          onChange("group_trigger", { ...groupTrigger, mention_only: checked })
        }
        ariaLabel={t("channels.field.mentionOnly")}
      />

      {canLink && (
        <div className="border-border/40 border-t pt-3">
          <p className="mb-2 text-sm font-medium">Device Linking</p>
          <SecMsgLinkPanel channelName={channelName} />
        </div>
      )}
    </div>
  )
}

interface SecMsgFormProps {
  daemonName: string
  config: ChannelConfig
  onChange: (key: string, value: unknown) => void
  // isNew suppresses the per-account link panel: pairing needs a saved, running
  // channel the backend can resolve by name.
  isNew: boolean
}

// DiscoveredAccounts lists the accounts the daemon actually hosts (queried live),
// each with its own device-linking panel. This is the primary path: point claw
// at the daemon and it binds one channel per account automatically — no manual
// account entry required.
function DiscoveredAccounts({ daemonName }: { daemonName: string }) {
  const [accounts, setAccounts] = useState<string[] | "error" | null>(null)

  useEffect(() => {
    let cancelled = false
    getSecMsgAccounts(daemonName)
      .then((r) => {
        if (!cancelled) setAccounts(r.accounts)
      })
      .catch(() => {
        if (!cancelled) setAccounts("error")
      })
    return () => {
      cancelled = true
    }
  }, [daemonName])

  if (accounts === null) {
    return <p className="text-muted-foreground text-xs">Querying daemon…</p>
  }
  if (accounts === "error") {
    return (
      <p className="text-muted-foreground text-xs">
        Daemon unreachable — accounts will appear once it's running. Save the
        daemon first if you just added it.
      </p>
    )
  }
  if (accounts.length === 0) {
    return (
      <p className="text-muted-foreground text-xs">
        No linked accounts on this daemon yet. Link a device below to add one.
      </p>
    )
  }

  return (
    <div className="space-y-3">
      {accounts.map((id) => (
        <div
          key={id}
          className="border-border/60 bg-muted/20 space-y-3 rounded-lg border p-4"
        >
          <div className="flex items-center gap-2">
            <span className="font-mono text-sm font-semibold">{id}</span>
            <span className="text-muted-foreground font-mono text-xs">
              → {accountChannelName(daemonName, id)}
            </span>
          </div>
          <SecMsgLinkPanel channelName={accountChannelName(daemonName, id)} />
        </div>
      ))}
    </div>
  )
}

export function SecMsgForm({
  daemonName,
  config,
  onChange,
  isNew,
}: SecMsgFormProps) {
  const { t } = useTranslation()
  const accounts = asArray(config.accounts).map(asRecord)
  const groupTrigger = asRecord(config.group_trigger)
  const [showAdvanced, setShowAdvanced] = useState(accounts.length > 0)

  const setAccounts = (next: Record<string, unknown>[]) =>
    onChange("accounts", next)

  const updateAccount = (index: number, key: string, value: unknown) => {
    setAccounts(
      accounts.map((a, i) => (i === index ? { ...a, [key]: value } : a)),
    )
  }

  return (
    <div className="space-y-5">
      <Field
        label={t("channels.field.secmsgAddress")}
        required
        hint={t("channels.form.desc.secmsgAddress")}
      >
        <Input
          value={asString(config.address)}
          onChange={(e) => onChange("address", e.target.value)}
          placeholder="127.0.0.1:9600"
        />
      </Field>

      <Field
        label={t("channels.field.allowFrom")}
        hint="Who may message any account on this daemon. Applies to every discovered account; a pinned account below can override it."
      >
        <Input
          value={asStringArray(config.allow_from).join(", ")}
          onChange={(e) =>
            onChange(
              "allow_from",
              e.target.value
                .split(",")
                .map((s: string) => s.trim())
                .filter(Boolean),
            )
          }
          placeholder={t("channels.field.allowFromPlaceholder")}
        />
      </Field>

      <SwitchCardField
        label={t("channels.field.mentionOnly")}
        hint={t("channels.form.desc.mentionOnly")}
        checked={asBool(groupTrigger.mention_only)}
        onCheckedChange={(checked) =>
          onChange("group_trigger", { ...groupTrigger, mention_only: checked })
        }
        ariaLabel={t("channels.field.mentionOnly")}
      />

      <div className="space-y-3">
        <div>
          <p className="text-sm font-medium">Accounts</p>
          <p className="text-muted-foreground text-xs">
            claw discovers this daemon's linked accounts automatically — each
            becomes its own channel. Link devices below.
          </p>
        </div>

        {isNew ? (
          <p className="text-muted-foreground text-xs">
            Save the daemon to discover its accounts.
          </p>
        ) : (
          <DiscoveredAccounts daemonName={daemonName} />
        )}
      </div>

      <div className="border-border/40 space-y-3 border-t pt-4">
        <button
          type="button"
          onClick={() => setShowAdvanced((v) => !v)}
          className="text-muted-foreground hover:text-foreground text-xs font-medium"
        >
          {showAdvanced ? "Hide" : "Show"} advanced: pin specific accounts
        </button>

        {showAdvanced && (
          <div className="space-y-3">
            <p className="text-muted-foreground text-xs">
              Optional. Pin an account to give it a custom name, its own
              allowlist, or group-trigger settings. Leave empty to let
              discovery bind every account with the daemon defaults above.
            </p>
            {accounts.map((account, i) => (
              <AccountRow
                key={i}
                daemonName={daemonName}
                account={account}
                canLink={!isNew}
                onChange={(key, value) => updateAccount(i, key, value)}
                onRemove={() => setAccounts(accounts.filter((_, j) => j !== i))}
              />
            ))}
            <Button
              variant="outline"
              size="sm"
              onClick={() => setAccounts([...accounts, { account: "" }])}
            >
              <IconPlus className="size-4" />
              Pin account
            </Button>
          </div>
        )}
      </div>
    </div>
  )
}
