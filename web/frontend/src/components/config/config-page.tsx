import { IconCode, IconDeviceFloppy } from "@tabler/icons-react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { useEffect, useMemo, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import { patchAppConfig } from "@/api/channels"
import {
  getAutoStartStatus,
  getLauncherConfig,
  setAutoStartEnabled as updateAutoStartEnabled,
  setLauncherConfig as updateLauncherConfig,
} from "@/api/system"
import {
  AgentDefaultsSection,
  AgentModelDefaultsSection,
  ContextManagementSection,
  DevicesSection,
  LauncherSection,
  RuntimeSection,
} from "@/components/config/config-sections"
import {
  type CoreConfigForm,
  EMPTY_FORM,
  EMPTY_LAUNCHER_FORM,
  type LauncherForm,
  buildFormFromConfig,
  parseCIDRText,
  parseIntField,
} from "@/components/config/form-model"
import { PageHeader } from "@/components/page-header"
import { Button } from "@/components/ui/button"

export function ConfigPage() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [form, setForm] = useState<CoreConfigForm>(EMPTY_FORM)
  const [baseline, setBaseline] = useState<CoreConfigForm>(EMPTY_FORM)
  const [launcherForm, setLauncherForm] =
    useState<LauncherForm>(EMPTY_LAUNCHER_FORM)
  const [launcherBaseline, setLauncherBaseline] =
    useState<LauncherForm>(EMPTY_LAUNCHER_FORM)
  const [autoStartEnabled, setAutoStartEnabled] = useState(false)
  const [autoStartBaseline, setAutoStartBaseline] = useState(false)
  const [saving, setSaving] = useState(false)

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

  const { data: launcherConfig, isLoading: isLauncherLoading } = useQuery({
    queryKey: ["system", "launcher-config"],
    queryFn: getLauncherConfig,
  })

  const {
    data: autoStartStatus,
    isLoading: isAutoStartLoading,
    error: autoStartError,
  } = useQuery({
    queryKey: ["system", "autostart"],
    queryFn: getAutoStartStatus,
  })

  useEffect(() => {
    if (!data) return
    const parsed = buildFormFromConfig(data)
    setForm(parsed)
    setBaseline(parsed)
  }, [data])

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

  useEffect(() => {
    if (!launcherConfig) return
    const parsed: LauncherForm = {
      port: String(launcherConfig.port),
      publicAccess: launcherConfig.public,
      allowedCIDRsText: (launcherConfig.allowed_cidrs ?? []).join("\n"),
    }
    setLauncherForm(parsed)
    setLauncherBaseline(parsed)
  }, [launcherConfig])

  useEffect(() => {
    if (!autoStartStatus) return
    setAutoStartEnabled(autoStartStatus.enabled)
    setAutoStartBaseline(autoStartStatus.enabled)
  }, [autoStartStatus])

  const configDirty = JSON.stringify(form) !== JSON.stringify(baseline)
  const launcherDirty =
    JSON.stringify(launcherForm) !== JSON.stringify(launcherBaseline)
  const autoStartDirty = autoStartEnabled !== autoStartBaseline
  const isDirty = configDirty || launcherDirty || autoStartDirty

  const autoStartSupported = autoStartStatus?.supported !== false
  const autoStartHint = autoStartError
    ? t("pages.config.autostart_load_error")
    : !autoStartSupported
      ? t("pages.config.autostart_unsupported")
      : t("pages.config.autostart_hint")

  const updateField = <K extends keyof CoreConfigForm>(
    key: K,
    value: CoreConfigForm[K],
  ) => {
    setForm((prev) => ({ ...prev, [key]: value }))
  }

  const updateLauncherField = <K extends keyof LauncherForm>(
    key: K,
    value: LauncherForm[K],
  ) => {
    setLauncherForm((prev) => ({ ...prev, [key]: value }))
  }

  const handleReset = () => {
    setForm(baseline)
    setLauncherForm(launcherBaseline)
    setAutoStartEnabled(autoStartBaseline)
    toast.info(t("pages.config.reset_success"))
  }

  const handleSave = async () => {
    try {
      setSaving(true)

      if (configDirty) {
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
        const summarizationModels = form.summarizationModels
          .map((m) => m.trim())
          .filter((m) => m.length > 0)

        // Default model (agents.defaults.model): bare string when no fallbacks,
        // { primary, fallbacks } with them, null when cleared.
        const defaultModelName = form.defaultModel.trim()
        const defaultFallbacks = form.defaultModelFallbacks
          .map((m) => m.trim())
          .filter((m) => m.length > 0)
        const defaultModelPayload = !defaultModelName
          ? null
          : defaultFallbacks.length > 0
            ? { primary: defaultModelName, fallbacks: defaultFallbacks }
            : defaultModelName

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
        // Re-send the full agents.list with default flags flipped only when the
        // default agent actually changed (the patch replaces the array wholesale).
        const defaultAgentChanged = form.defaultAgentId !== baseline.defaultAgentId
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

        await patchAppConfig({
          agents: {
            base_dir: baseDir,
            defaults: {
              restrict_to_workspace: form.restrictToWorkspace,
              stream_tool_activity: form.streamToolActivity,
              max_tokens: maxTokens,
              max_tool_iterations: maxToolIterations,
              model: defaultModelPayload,
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
        })

        setBaseline(form)
        queryClient.invalidateQueries({ queryKey: ["config"] })
      }

      if (launcherDirty) {
        const port = parseIntField(launcherForm.port, "Service port", {
          min: 1,
          max: 65535,
        })
        const allowedCIDRs = parseCIDRText(launcherForm.allowedCIDRsText)
        const savedLauncherConfig = await updateLauncherConfig({
          port,
          public: launcherForm.publicAccess,
          allowed_cidrs: allowedCIDRs,
        })
        const parsedLauncher: LauncherForm = {
          port: String(savedLauncherConfig.port),
          publicAccess: savedLauncherConfig.public,
          allowedCIDRsText: (savedLauncherConfig.allowed_cidrs ?? []).join(
            "\n",
          ),
        }
        setLauncherForm(parsedLauncher)
        setLauncherBaseline(parsedLauncher)
        queryClient.setQueryData(
          ["system", "launcher-config"],
          savedLauncherConfig,
        )
      }

      if (autoStartDirty) {
        if (!autoStartSupported) {
          throw new Error(t("pages.config.autostart_unsupported"))
        }
        const status = await updateAutoStartEnabled(autoStartEnabled)
        setAutoStartEnabled(status.enabled)
        setAutoStartBaseline(status.enabled)
        queryClient.setQueryData(["system", "autostart"], status)
      }

      toast.success(t("pages.config.save_success"))
    } catch (err) {
      toast.error(
        err instanceof Error ? err.message : t("pages.config.save_error"),
      )
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="flex h-full flex-col">
      <PageHeader
        title={t("navigation.config")}
        children={
          <Button variant="outline" asChild>
            <Link to="/config/raw">
              <IconCode className="size-4" />
              {t("pages.config.open_raw")}
            </Link>
          </Button>
        }
      />
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
              {isDirty && (
                <div className="bg-yellow-50 px-3 py-2 text-sm text-yellow-700">
                  {t("pages.config.unsaved_changes")}
                </div>
              )}

              <AgentDefaultsSection form={form} onFieldChange={updateField} />

              <AgentModelDefaultsSection
                form={form}
                onFieldChange={updateField}
                agentOptions={agentOptions}
              />

              <ContextManagementSection form={form} onFieldChange={updateField} />

              <RuntimeSection form={form} onFieldChange={updateField} />

              <LauncherSection
                launcherForm={launcherForm}
                onFieldChange={updateLauncherField}
                disabled={saving || isLauncherLoading}
              />

              <DevicesSection
                form={form}
                onFieldChange={updateField}
                autoStartEnabled={autoStartEnabled}
                autoStartHint={autoStartHint}
                autoStartDisabled={
                  isAutoStartLoading ||
                  Boolean(autoStartError) ||
                  !autoStartSupported ||
                  saving
                }
                onAutoStartChange={setAutoStartEnabled}
              />

              <div className="flex justify-end gap-2">
                <Button
                  variant="outline"
                  onClick={handleReset}
                  disabled={!isDirty || saving}
                >
                  {t("common.reset")}
                </Button>
                <Button onClick={handleSave} disabled={!isDirty || saving}>
                  <IconDeviceFloppy className="size-4" />
                  {saving ? t("common.saving") : t("common.save")}
                </Button>
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
