import { IconCode } from "@tabler/icons-react"
import { useQuery } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { useEffect, useMemo, useRef, useState } from "react"
import { useTranslation } from "react-i18next"

import { patchAppConfig } from "@/api/channels"
import {
  AgentDefaultsSection,
  AgentModelDefaultsSection,
  BackupSection,
  ContextManagementSection,
  DevicesSection,
  RuntimeSection,
  ServiceSection,
} from "@/components/config/config-sections"
import {
  type CoreConfigForm,
  EMPTY_FORM,
  buildFormFromConfig,
  parseCIDRText,
  parseIntField,
} from "@/components/config/form-model"
import { PageHeader } from "@/components/page-header"
import { Button } from "@/components/ui/button"

type SaveStatus = "saving" | "saved" | "error" | null

export function ConfigPage() {
  const { t } = useTranslation()
  const [form, setForm] = useState<CoreConfigForm>(EMPTY_FORM)
  const [status, setStatus] = useState<SaveStatus>(null)
  // Message for a validation failure (e.g. a bad number), shown inline instead of
  // toast-spamming on every debounced attempt.
  const [saveError, setSaveError] = useState<string | null>(null)

  // Refs so the debounced save reads current values and diffs against the last
  // persisted snapshot (baselineRef) — used to detect a default-agent change,
  // which requires re-sending the whole agents.list (arrays are replaced
  // wholesale by the merge patch, not deep-merged).
  const formRef = useRef<CoreConfigForm>(form)
  formRef.current = form
  const baselineRef = useRef<CoreConfigForm>(EMPTY_FORM)
  const saveTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const savedTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)

  // The address the user is currently reaching the WebUI on. Inherently
  // reachable from their own machine (where claw-auth runs), so it is the
  // external-URL default/placeholder for the Service card.
  const externalUrlPlaceholder = `${window.location.protocol}//${window.location.host}`

  const { data, isLoading, error } = useQuery({
    queryKey: ["config"],
    queryFn: async () => {
      const res = await fetch("/api/config")
      if (!res.ok) {
        throw new Error("Failed to load config")
      }
      return res.json()
    },
  })

  useEffect(() => {
    if (!data) return
    const parsed = buildFormFromConfig(data)
    // Seed the external URL with how the user is currently reaching the WebUI so
    // an unset value persists a reachable default on save (rather than leaving it
    // to server-side host:port derivation).
    if (!parsed.gatewayExternalUrl) {
      parsed.gatewayExternalUrl = externalUrlPlaceholder
    }
    setForm(parsed)
    baselineRef.current = parsed
  }, [data, externalUrlPlaceholder])

  // Clear pending timers on unmount so a debounced save can't fire after teardown.
  useEffect(
    () => () => {
      clearTimeout(saveTimer.current)
      clearTimeout(savedTimer.current)
    },
    [],
  )

  // Raw agents.list straight from the loaded config — re-sent verbatim (with the
  // default flag flipped) when the default agent changes, so no agent fields are
  // lost. The backend patch replaces the list array wholesale, so it must be
  // complete.
  const rawAgentList = useMemo<Array<Record<string, unknown>>>(() => {
    const agents = (data as { agents?: { list?: unknown } } | undefined)?.agents
    return Array.isArray(agents?.list)
      ? (agents.list as Array<Record<string, unknown>>)
      : []
  }, [data])

  const agentOptions = useMemo(
    () =>
      rawAgentList
        .filter((a) => a.enabled !== false)
        .map((a) => ({ id: String(a.id ?? ""), name: a.name ? String(a.name) : undefined }))
        .filter((a) => a.id)
        .sort((a, b) => (a.name || a.id).localeCompare(b.name || b.id)),
    [rawAgentList],
  )

  const updateField = <K extends keyof CoreConfigForm>(
    key: K,
    value: CoreConfigForm[K],
  ) => {
    setForm((prev) => ({ ...prev, [key]: value }))
    scheduleSave()
  }

  const scheduleSave = () => {
    clearTimeout(saveTimer.current)
    saveTimer.current = setTimeout(() => void doSave(), 600)
  }

  // doSave validates the current form and, if valid, sends one JSON merge patch.
  // A validation failure (e.g. a non-numeric field) surfaces inline via saveError
  // and skips the patch — no toast spam while the user is mid-edit. It does not
  // refetch config, so edits made during the save are preserved.
  const doSave = async () => {
    const form = formRef.current
    let patch: Record<string, unknown>
    try {
      {
        // base_dir may be blank — the backend then defaults to <data_dir>/agents.
        const baseDir = form.baseDir.trim()
        const sessionMode = form.sessionMode.trim()

        if (!sessionMode) {
          throw new Error("Session mode is required.")
        }

        const maxTokens = parseIntField(form.maxTokens, "Max tokens", {
          min: 1,
        })
        const maxToolIterations = parseIntField(
          form.maxToolIterations,
          "Max tool iterations",
          { min: 1 },
        )
        const requestTimeout = parseIntField(
          form.requestTimeout,
          "Request timeout (s)",
          { min: 0 },
        )
        const turnTimeout = parseIntField(
          form.turnTimeout,
          "Turn timeout (s)",
          { min: 0 },
        )
        const maxSubagentDepth = parseIntField(
          form.maxSubagentDepth,
          "Max sub-agent depth",
          { min: 1 },
        )
        const summarizationModels = form.summarizationModels
          .map((m) => m.trim())
          .filter((m) => m.length > 0)

        // Default models (agents.defaults.models): ordered list tried in order.
        const defaultModels = form.defaultModels
          .map((s) => s.trim())
          .filter((s) => s.length > 0)

        // Default temperature: number in [0,2], or null to clear when blank.
        const tempRaw = form.defaultTemperature.trim()
        let defaultTemperaturePayload: number | null = null
        if (tempRaw !== "") {
          const tp = Number(tempRaw)
          if (Number.isNaN(tp) || tp < 0 || tp > 2) {
            throw new Error("Default temperature must be a number between 0 and 2.")
          }
          defaultTemperaturePayload = tp
        }
        const compressNormalPercent = parseIntField(
          form.compressNormalPercent,
          "Normal compression threshold",
          { min: 0, max: 100 },
        )
        const compressSafetyPercent = parseIntField(
          form.compressSafetyPercent,
          "Emergency compression threshold",
          { min: 0, max: 100 },
        )
        const compressMinPercent = parseIntField(
          form.compressMinPercent,
          "Minimum context threshold",
          { min: 0, max: 100 },
        )
        const compressMessageThreshold = parseIntField(
          form.compressMessageThreshold,
          "Message count threshold",
          { min: 0 },
        )
        const compressRetainTokenPercent = parseIntField(
          form.compressRetainTokenPercent,
          "Tail window size",
          { min: 0, max: 100 },
        )
        const compressRetainMinMessages = parseIntField(
          form.compressRetainMinMessages,
          "Minimum tail messages",
          { min: 0 },
        )
        const archiveMessageCount = parseIntField(
          form.archiveMessageCount,
          "Archive message count",
          { min: 0 },
        )
        const archiveDays = parseIntField(form.archiveDays, "Archive days", {
          min: 0,
        })
        const summaryMaxCount = parseIntField(
          form.summaryMaxCount,
          "Max summaries kept",
          { min: 0 },
        )
        const summaryRetentionDays = parseIntField(
          form.summaryRetentionDays,
          "Summary retention days",
          { min: 0 },
        )
        const logRetentionDays = parseIntField(
          form.logRetentionDays,
          "Log retention days",
          { min: 0 },
        )
        const evictionProtectTurns = parseIntField(
          form.evictionProtectTurns,
          "Protected age",
          { min: 0 },
        )
        const evictionEvictTurns = parseIntField(
          form.evictionEvictTurns,
          "Evict everything after (turns)",
          { min: 0 },
        )
        const evictionBudgetBytes = parseIntField(
          form.evictionBudgetBytes,
          "Read byte budget",
          { min: 0 },
        )
        // Re-send the full agents.list with default flags flipped only when the
        // default agent actually changed (the patch replaces the array wholesale).
        const defaultAgentChanged =
          form.defaultAgentId !== baselineRef.current.defaultAgentId
        const agentListPayload = defaultAgentChanged
          ? rawAgentList.map((a) => {
              const next = { ...a }
              if (String(a.id ?? "") === form.defaultAgentId) {
                next.default = true
              } else {
                delete next.default
              }
              return next
            })
          : undefined

        const gatewayPort = parseIntField(form.gatewayPort, "Web port", {
          min: 1,
          max: 65535,
        })

        patch = {
          gateway: {
            host: form.gatewayHost,
            port: gatewayPort,
            external_url: form.gatewayExternalUrl.trim(),
            allowed_cidrs: parseCIDRText(form.allowedCIDRsText),
          },
          agents: {
            base_dir: baseDir,
            common_dir: form.commonDir.trim(),
            defaults: {
              restrict_to_workspace: form.restrictToWorkspace,
              stream_tool_activity: form.streamToolActivity,
              max_tokens: maxTokens,
              max_tool_iterations: maxToolIterations,
              max_subagent_depth: maxSubagentDepth,
              request_timeout: requestTimeout,
              turn_timeout: turnTimeout,
              models: defaultModels,
              vision_model: form.visionModels[0] ?? "",
              vision_model_fallbacks: form.visionModels.slice(1),
              temperature: defaultTemperaturePayload,
              compress_normal_percent: compressNormalPercent,
              compress_safety_percent: compressSafetyPercent,
              compress_min_percent: compressMinPercent,
              compress_message_threshold: compressMessageThreshold,
              compress_retain_token_percent: compressRetainTokenPercent,
              compress_retain_min_messages: compressRetainMinMessages,
              archive_message_count: archiveMessageCount,
              archive_days: archiveDays,
              summary_max_count: summaryMaxCount,
              summary_retention_days: summaryRetentionDays,
              context_eviction: {
                enabled: form.evictionEnabled,
                notify_user: form.evictionNotifyUser,
                protect_turns: evictionProtectTurns,
                evict_turns: evictionEvictTurns,
                budget_bytes: evictionBudgetBytes,
              },
            },
            ...(agentListPayload ? { list: agentListPayload } : {}),
          },
          summarization: {
            models: summarizationModels,
            debug_capture: form.summarizationDebugCapture,
          },
          session: {
            mode: sessionMode,
          },
          tools: {
            exec: {
              allow_remote: form.allowRemote,
            },
          },
          devices: {
            enabled: form.devicesEnabled,
            monitor_usb: form.monitorUSB,
          },
          logging: {
            retention_days: logRetentionDays,
          },
          backup: {
            enabled: form.backupEnabled,
            at: form.backupAt.trim() || "03:00",
            retain_days: parseIntField(form.backupRetainDays, "Backup retention days", { min: 1 }),
          },
        }
      }
    } catch (err) {
      // Validation error (parseIntField etc.) — show it, don't patch.
      setStatus("error")
      setSaveError(err instanceof Error ? err.message : String(err))
      return
    }

    setSaveError(null)
    setStatus("saving")
    try {
      await patchAppConfig(patch)
      baselineRef.current = form
      setStatus("saved")
      clearTimeout(savedTimer.current)
      savedTimer.current = setTimeout(() => setStatus(null), 2000)
    } catch (err) {
      setStatus("error")
      setSaveError(err instanceof Error ? err.message : t("pages.config.save_error"))
    }
  }

  return (
    <div className="flex h-full flex-col">
      <PageHeader title={t("navigation.config")}>
        <div className="flex items-center gap-3">
          {status && (
            <span
              className={`text-xs ${status === "error" ? "text-destructive" : status === "saved" ? "text-emerald-500" : "text-muted-foreground"}`}
            >
              {status === "saving"
                ? "Saving…"
                : status === "saved"
                  ? "Saved ✓"
                  : "Save failed"}
            </span>
          )}
          <Button variant="outline" asChild>
            <Link to="/config/raw">
              <IconCode className="size-4" />
              {t("pages.config.open_raw")}
            </Link>
          </Button>
        </div>
      </PageHeader>
      <div className="flex-1 overflow-auto p-3 lg:p-6">
        <div className="w-full max-w-[1000px] space-y-6">
          {isLoading ? (
            <div className="text-muted-foreground py-6 text-sm">
              {t("labels.loading")}
            </div>
          ) : error ? (
            <div className="text-destructive py-6 text-sm">
              {t("pages.config.load_error")}
            </div>
          ) : (
            <div className="space-y-6">
              {saveError && (
                <div className="bg-destructive/10 text-destructive px-3 py-2 text-sm">
                  {saveError}
                </div>
              )}

              <ServiceSection
                form={form}
                onFieldChange={updateField}
                externalUrlPlaceholder={externalUrlPlaceholder}
              />

              <AgentDefaultsSection form={form} onFieldChange={updateField} />

              <AgentModelDefaultsSection
                form={form}
                onFieldChange={updateField}
                agentOptions={agentOptions}
              />

              <ContextManagementSection form={form} onFieldChange={updateField} />

              <RuntimeSection form={form} onFieldChange={updateField} />

            <BackupSection form={form} onFieldChange={updateField} />

              <DevicesSection form={form} onFieldChange={updateField} />
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
