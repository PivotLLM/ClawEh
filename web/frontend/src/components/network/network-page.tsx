import { IconAlertTriangle, IconX } from "@tabler/icons-react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { type ReactNode, useEffect, useRef, useState } from "react"
import { useTranslation } from "react-i18next"

import { getAppConfig, patchAppConfig } from "@/api/channels"
import { getTLS } from "@/api/tls"
import { ConfigSectionCard } from "@/components/config/sections/section-card"
import { CertificateCard } from "@/components/network/certificate-card"
import {
  EMPTY_NETWORK_FORM,
  type HttpScope,
  type HttpsMode,
  type NetworkForm,
  buildNetworkFormFromConfig,
  buildNetworkPatch,
  listenerChanged,
} from "@/components/network/network-form"
import { PageHeader } from "@/components/page-header"
import { Field, SwitchCardField } from "@/components/shared-form"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group"
import { Textarea } from "@/components/ui/textarea"

type SaveStatus = "saving" | "saved" | "error" | null

interface Choice<V extends string> {
  value: V
  label: string
  hint?: string
  /** Shown in amber under the option: a consequence worth reading first. */
  warning?: string
}

// ChoiceField is a labelled radio group laid out like the other setting rows.
function ChoiceField<V extends string>({
  name,
  label,
  hint,
  value,
  onChange,
  options,
}: {
  name: string
  label: string
  hint?: string
  value: V
  onChange: (v: V) => void
  options: Choice<V>[]
}) {
  return (
    <div className="flex flex-col gap-4 py-4 md:grid md:grid-cols-[minmax(0,1fr)_minmax(240px,320px)] md:gap-6">
      <div className="max-w-full space-y-1 md:max-w-[clamp(18rem,42vw,28rem)]">
        <p className="text-sm font-medium">{label}</p>
        {hint && (
          <p className="text-muted-foreground text-xs leading-normal">{hint}</p>
        )}
      </div>
      <RadioGroup
        value={value}
        onValueChange={(v) => onChange(v as V)}
        className="gap-2"
        data-testid={name}
      >
        {options.map((o) => (
          <div key={o.value} className="flex items-start gap-2">
            <RadioGroupItem
              value={o.value}
              id={`${name}-${o.value}`}
              className="mt-0.5"
            />
            <div className="min-w-0">
              <Label htmlFor={`${name}-${o.value}`} className="font-normal">
                {o.label}
              </Label>
              {o.hint && (
                <p className="text-muted-foreground text-xs leading-normal">
                  {o.hint}
                </p>
              )}
              {o.warning && (
                <p className="text-xs leading-normal text-amber-600 dark:text-amber-400">
                  {o.warning}
                </p>
              )}
            </div>
          </div>
        ))}
      </RadioGroup>
    </div>
  )
}

// TagInput edits a short list of names: Enter or comma adds, × removes.
function TagInput({
  value,
  onChange,
  placeholder,
  testId,
}: {
  value: string[]
  onChange: (v: string[]) => void
  placeholder?: string
  testId: string
}) {
  const [draft, setDraft] = useState("")
  const commit = () => {
    const name = draft.trim().replace(/,$/, "").trim()
    if (name && !value.includes(name)) onChange([...value, name])
    setDraft("")
  }
  return (
    <div className="flex flex-col gap-2">
      {value.length > 0 && (
        <ul className="flex flex-wrap gap-1.5" data-testid={`${testId}-tags`}>
          {value.map((name) => (
            <li
              key={name}
              className="bg-muted flex items-center gap-1 rounded-md px-2 py-0.5 text-xs"
            >
              <span className="font-mono">{name}</span>
              <button
                type="button"
                className="text-muted-foreground hover:text-foreground"
                aria-label={`Remove ${name}`}
                onClick={() => onChange(value.filter((n) => n !== name))}
              >
                <IconX className="size-3" />
              </button>
            </li>
          ))}
        </ul>
      )}
      <Input
        data-testid={testId}
        value={draft}
        placeholder={placeholder}
        onChange={(e) => {
          if (e.target.value.endsWith(",")) {
            setDraft(e.target.value)
            commit()
          } else {
            setDraft(e.target.value)
          }
        }}
        onKeyDown={(e) => {
          if (e.key === "Enter") {
            e.preventDefault()
            commit()
          }
        }}
        onBlur={commit}
      />
    </div>
  )
}

function ReadOnlyRow({
  label,
  hint,
  children,
}: {
  label: string
  hint?: string
  children: ReactNode
}) {
  return (
    <div className="flex flex-col gap-2 py-4 md:grid md:grid-cols-[minmax(0,1fr)_minmax(240px,320px)] md:items-center md:gap-6">
      <div className="max-w-full space-y-1 md:max-w-[clamp(18rem,42vw,28rem)]">
        <p className="text-sm font-medium">{label}</p>
        {hint && (
          <p className="text-muted-foreground text-xs leading-normal">{hint}</p>
        )}
      </div>
      <div className="text-sm">{children}</div>
    </div>
  )
}

// NetworkPage is everything about listeners: the WebUI's HTTP and HTTPS
// listeners and their certificate, the IP allowlist, the device gateway's
// listener, and the (loopback-only) MCP host address. The general fields save
// on the Save button through PATCH /api/config; the certificate has its own
// buttons because its files are validated by the server before they are kept.
export function NetworkPage() {
  const { t } = useTranslation()
  const qc = useQueryClient()

  const configQuery = useQuery({ queryKey: ["config"], queryFn: getAppConfig })
  const tlsQuery = useQuery({
    queryKey: ["tls"],
    queryFn: getTLS,
    retry: false,
  })

  const [form, setForm] = useState<NetworkForm>(EMPTY_NETWORK_FORM)
  const [baseline, setBaseline] = useState<NetworkForm>(EMPTY_NETWORK_FORM)
  // Seed the form when a fetch lands. Adjusted during render rather than in an
  // effect so the form is never painted empty for a frame, and only for a
  // genuinely new result — never clobbering edits since.
  const [synced, setSynced] = useState(configQuery.data)
  if (configQuery.data && configQuery.data !== synced) {
    setSynced(configQuery.data)
    const parsed = buildNetworkFormFromConfig(configQuery.data)
    setForm(parsed)
    setBaseline(parsed)
  }

  const [status, setStatus] = useState<SaveStatus>(null)
  const [saveError, setSaveError] = useState<string | null>(null)
  // Set after a save that changed a bound address or port; the server's own
  // restart_required flag covers it once GET /api/tls is refetched, but that
  // read can lag the write.
  const [restartPending, setRestartPending] = useState(false)
  const savedTimer = useRef<ReturnType<typeof setTimeout> | undefined>(
    undefined,
  )
  useEffect(() => () => clearTimeout(savedTimer.current), [])

  const update = <K extends keyof NetworkForm>(key: K, value: NetworkForm[K]) =>
    setForm((prev) => ({ ...prev, [key]: value }))

  const dirty = JSON.stringify(form) !== JSON.stringify(baseline)

  const refreshTLS = () => {
    void qc.invalidateQueries({ queryKey: ["tls"] })
    void qc.invalidateQueries({ queryKey: ["config"] })
  }

  const save = async () => {
    let patch: Record<string, unknown>
    try {
      patch = buildNetworkPatch(form)
    } catch (err) {
      setStatus("error")
      setSaveError(err instanceof Error ? err.message : String(err))
      return
    }
    setSaveError(null)
    setStatus("saving")
    try {
      await patchAppConfig(patch)
      if (listenerChanged(form, baseline)) setRestartPending(true)
      setBaseline(form)
      setStatus("saved")
      clearTimeout(savedTimer.current)
      savedTimer.current = setTimeout(() => setStatus(null), 2000)
      void qc.invalidateQueries({ queryKey: ["tls"] })
    } catch (err) {
      setStatus("error")
      setSaveError(
        err instanceof Error ? err.message : t("pages.network.save_failed"),
      )
    }
  }

  const tls = tlsQuery.data
  const tlsError = tlsQuery.error
    ? tlsQuery.error instanceof Error
      ? tlsQuery.error.message
      : String(tlsQuery.error)
    : null
  const restartRequired = restartPending || tls?.restart_required === true

  return (
    <div className="flex h-full flex-col">
      <PageHeader title={t("navigation.network")}>
        <div className="flex items-center gap-3">
          {status && (
            <span
              className={`text-xs ${status === "error" ? "text-destructive" : status === "saved" ? "text-emerald-500" : "text-muted-foreground"}`}
              data-testid="network-save-status"
            >
              {status === "saving"
                ? t("pages.network.saving")
                : status === "saved"
                  ? t("pages.network.saved")
                  : t("pages.network.save_failed")}
            </span>
          )}
          <Button
            onClick={() => void save()}
            disabled={status === "saving" || !dirty}
            data-testid="network-save"
          >
            {t("pages.network.save")}
          </Button>
        </div>
      </PageHeader>
      <div className="flex-1 overflow-auto p-3 lg:p-6">
        <div className="w-full max-w-[1000px] space-y-6">
          {configQuery.isLoading ? (
            <div className="text-muted-foreground py-6 text-sm">
              {t("labels.loading")}
            </div>
          ) : configQuery.error ? (
            <div className="text-destructive py-6 text-sm">
              {t("pages.network.load_error")}
            </div>
          ) : (
            <div className="space-y-6">
              {restartRequired && (
                <div
                  className="flex items-start gap-2 rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-sm text-amber-700 dark:text-amber-300"
                  data-testid="network-restart-banner"
                >
                  <IconAlertTriangle className="mt-0.5 size-4 shrink-0" />
                  <span>{t("pages.network.restart_required")}</span>
                </div>
              )}
              {saveError && (
                <div
                  className="bg-destructive/10 text-destructive px-3 py-2 text-sm"
                  role="alert"
                  data-testid="network-save-error"
                >
                  {saveError}
                </div>
              )}

              {/* Where to open ClawEh, from the running listeners. */}
              <ConfigSectionCard
                title={t("pages.network.urls.title")}
                description={t("pages.network.urls.desc")}
              >
                <div className="py-4" data-testid="network-urls">
                  {tlsError ? (
                    <p className="text-destructive text-sm">
                      {t("pages.network.urls.unavailable", {
                        message: tlsError,
                      })}
                    </p>
                  ) : tls ? (
                    <dl className="grid gap-x-6 gap-y-2 text-sm md:grid-cols-[max-content_minmax(0,1fr)]">
                      <dt className="text-muted-foreground">
                        {t("pages.network.urls.localhost")}
                      </dt>
                      <dd>
                        <a
                          className="font-mono text-xs underline"
                          href={tls.urls.localhost}
                        >
                          {tls.urls.localhost}
                        </a>
                      </dd>
                      {tls.urls.http.length > 0 && (
                        <>
                          <dt className="text-muted-foreground">
                            {t("pages.network.urls.http")}
                          </dt>
                          <dd>
                            <ul className="space-y-1">
                              {tls.urls.http.map((u) => (
                                <li key={u}>
                                  <a
                                    className="font-mono text-xs underline"
                                    href={u}
                                  >
                                    {u}
                                  </a>
                                </li>
                              ))}
                            </ul>
                            <p className="text-xs text-amber-600 dark:text-amber-400">
                              {t("pages.network.urls.http_warning")}
                            </p>
                          </dd>
                        </>
                      )}
                      <dt className="text-muted-foreground">
                        {t("pages.network.urls.https")}
                      </dt>
                      <dd>
                        {tls.urls.https.length === 0 ? (
                          <span className="text-muted-foreground">
                            {t("pages.network.urls.none")}
                          </span>
                        ) : (
                          <ul className="space-y-1">
                            {tls.urls.https.map((u) => (
                              <li key={u}>
                                <a
                                  className="font-mono text-xs underline"
                                  href={u}
                                >
                                  {u}
                                </a>
                              </li>
                            ))}
                          </ul>
                        )}
                      </dd>
                    </dl>
                  ) : (
                    <p className="text-muted-foreground text-sm">
                      {t("labels.loading")}
                    </p>
                  )}
                </div>
              </ConfigSectionCard>

              <ConfigSectionCard
                title={t("pages.network.http.title")}
                description={t("pages.network.http.desc")}
              >
                <Field
                  label={t("pages.network.http.port")}
                  hint={t("pages.network.http.port_hint")}
                  layout="setting-row"
                >
                  <Input
                    type="number"
                    min={1}
                    max={65535}
                    value={form.httpPort}
                    data-testid="network-http-port"
                    onChange={(e) => update("httpPort", e.target.value)}
                  />
                </Field>
                <ChoiceField<HttpScope>
                  name="network-http-scope"
                  label={t("pages.network.http.scope")}
                  hint={t("pages.network.http.scope_hint")}
                  value={form.httpScope}
                  onChange={(v) => update("httpScope", v)}
                  options={[
                    {
                      value: "localhost",
                      label: t("pages.network.http.scope_localhost"),
                      hint: t("pages.network.http.scope_localhost_hint"),
                    },
                    {
                      value: "network",
                      label: t("pages.network.http.scope_network"),
                      warning: t("pages.network.http.scope_network_warning"),
                    },
                  ]}
                />
              </ConfigSectionCard>

              <ConfigSectionCard
                title={t("pages.network.https.title")}
                description={t("pages.network.https.desc")}
              >
                <ChoiceField<HttpsMode>
                  name="network-https-mode"
                  label={t("pages.network.https.mode")}
                  hint={t("pages.network.https.mode_hint")}
                  value={form.httpsMode}
                  onChange={(v) => update("httpsMode", v)}
                  options={[
                    {
                      value: "all",
                      label: t("pages.network.https.mode_all"),
                      hint: t("pages.network.https.mode_all_hint"),
                    },
                    {
                      value: "localhost",
                      label: t("pages.network.https.mode_localhost"),
                      hint: t("pages.network.https.mode_localhost_hint"),
                    },
                    {
                      value: "off",
                      label: t("pages.network.https.mode_off"),
                      hint: t("pages.network.https.mode_off_hint"),
                    },
                  ]}
                />
                <Field
                  label={t("pages.network.https.port")}
                  hint={t("pages.network.https.port_hint")}
                  layout="setting-row"
                >
                  <Input
                    type="number"
                    min={1}
                    max={65535}
                    value={form.tlsPort}
                    data-testid="network-tls-port"
                    onChange={(e) => update("tlsPort", e.target.value)}
                  />
                </Field>
                <Field
                  label={t("pages.network.https.external_url")}
                  hint={t("pages.network.https.external_url_hint")}
                  layout="setting-row"
                >
                  <Input
                    type="text"
                    value={form.externalUrl}
                    placeholder="https://claw.example.com"
                    data-testid="network-external-url"
                    onChange={(e) => update("externalUrl", e.target.value)}
                  />
                </Field>
                <Field
                  label={t("pages.network.https.extra_names")}
                  hint={t("pages.network.https.extra_names_hint")}
                  layout="setting-row"
                >
                  <TagInput
                    value={form.tlsExtraNames}
                    onChange={(v) => update("tlsExtraNames", v)}
                    placeholder={t(
                      "pages.network.https.extra_names_placeholder",
                    )}
                    testId="network-extra-names"
                  />
                </Field>
              </ConfigSectionCard>

              <CertificateCard
                tls={tls}
                tlsError={tlsError}
                onChanged={refreshTLS}
              />

              <ConfigSectionCard
                title={t("pages.network.allowed.title")}
                description={t("pages.network.allowed.desc")}
              >
                <Field
                  label={t("pages.network.allowed.cidrs")}
                  hint={t("pages.network.allowed.cidrs_hint")}
                  layout="setting-row"
                  controlClassName="md:max-w-md"
                >
                  <Textarea
                    value={form.allowedCIDRsText}
                    placeholder={t("pages.network.allowed.cidrs_placeholder")}
                    className="min-h-[88px]"
                    data-testid="network-allowed-cidrs"
                    onChange={(e) => update("allowedCIDRsText", e.target.value)}
                  />
                </Field>
              </ConfigSectionCard>

              <ConfigSectionCard
                title={t("pages.network.device.title")}
                description={t("pages.network.device.desc")}
              >
                <ChoiceField<HttpScope>
                  name="network-device-scope"
                  label={t("pages.network.device.scope")}
                  hint={t("pages.network.device.scope_hint")}
                  value={form.deviceScope}
                  onChange={(v) => update("deviceScope", v)}
                  options={[
                    {
                      value: "localhost",
                      label: t("pages.network.device.scope_localhost"),
                    },
                    {
                      value: "network",
                      label: t("pages.network.device.scope_network"),
                      hint: t("pages.network.device.scope_network_hint"),
                    },
                  ]}
                />
                <Field
                  label={t("pages.network.device.port")}
                  hint={t("pages.network.device.port_hint")}
                  layout="setting-row"
                >
                  <Input
                    type="number"
                    min={1}
                    max={65535}
                    value={form.devicePort}
                    data-testid="network-device-port"
                    onChange={(e) => update("devicePort", e.target.value)}
                  />
                </Field>
                <Field
                  label={t("pages.network.device.external_url")}
                  hint={t("pages.network.device.external_url_hint")}
                  layout="setting-row"
                >
                  <Input
                    type="text"
                    value={form.deviceExternalUrl}
                    placeholder="wss://claw.example.com"
                    data-testid="network-device-external-url"
                    onChange={(e) =>
                      update("deviceExternalUrl", e.target.value)
                    }
                  />
                </Field>
                <Field
                  label={t("pages.network.device.cidrs")}
                  hint={t("pages.network.device.cidrs_hint")}
                  layout="setting-row"
                  controlClassName="md:max-w-md"
                >
                  <Textarea
                    value={form.deviceAllowedCIDRsText}
                    placeholder={t("pages.network.allowed.cidrs_placeholder")}
                    className="min-h-[66px]"
                    data-testid="network-device-cidrs"
                    onChange={(e) =>
                      update("deviceAllowedCIDRsText", e.target.value)
                    }
                  />
                </Field>
                <SwitchCardField
                  label={t("pages.network.device.auto_approve")}
                  hint={t("pages.network.device.auto_approve_hint")}
                  layout="setting-row"
                  checked={form.deviceAutoApprove}
                  onCheckedChange={(v) => update("deviceAutoApprove", v)}
                />
                <div className="py-3">
                  <Link
                    to="/devices"
                    className="text-muted-foreground text-sm underline"
                  >
                    {t("pages.network.device.pairing_link")}
                  </Link>
                </div>
              </ConfigSectionCard>

              <ConfigSectionCard
                title={t("pages.network.mcp.title")}
                description={t("pages.network.mcp.desc")}
              >
                <ReadOnlyRow
                  label={t("pages.network.mcp.listen")}
                  hint={t("pages.network.mcp.listen_hint")}
                >
                  <code
                    className="bg-muted rounded px-2 py-1 font-mono text-xs"
                    data-testid="network-mcp-listen"
                  >
                    {form.mcpListen}
                  </code>{" "}
                  <Link
                    to="/mcp/config"
                    className="text-muted-foreground text-xs underline"
                  >
                    {t("pages.network.mcp.link")}
                  </Link>
                </ReadOnlyRow>
              </ConfigSectionCard>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
