import { useCallback, useEffect, useRef, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import { getAppConfig, patchAppConfig } from "@/api/channels"
import {
  EMPTY_MCP_FORM,
  type MCPHostForm,
  buildMCPFormFromConfig,
  validateEndpointPath,
  validateListen,
} from "@/components/mcp/form-model"
import {
  DiscoverySection,
  EnableSection,
  ResilienceSection,
  ToolsSection,
  TransportSection,
} from "@/components/mcp/mcp-sections"
import { PageHeader } from "@/components/page-header"

type SaveStatus = "saving" | "saved" | "error" | null

// MCPConfigPage edits the global MCP settings — host transport, tool visibility,
// discovery, and client resilience. The external server list lives on its own
// page (MCPServersPage); this page never touches tools.mcp.servers.
export function MCPConfigPage() {
  const { t } = useTranslation()
  const [form, setForm] = useState<MCPHostForm>(EMPTY_MCP_FORM)
  const [status, setStatus] = useState<SaveStatus>(null)

  // formRef mirrors the latest form so the debounced save reads current values.
  const formRef = useRef<MCPHostForm>(form)
  useEffect(() => {
    formRef.current = form
  }, [form])
  const saveTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const savedTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)

  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState("")

  // Seed the editable form from the config via an async callback (not a
  // synchronous setState in an effect) — the repo's pattern for query→form state.
  const loadData = useCallback(async () => {
    setLoading(true)
    try {
      const cfg = await getAppConfig()
      setForm(buildMCPFormFromConfig(cfg))
      setLoadError("")
    } catch (e) {
      setLoadError(e instanceof Error ? e.message : "Failed to load")
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void loadData()
  }, [loadData])

  // Clear timers on unmount.
  useEffect(
    () => () => {
      clearTimeout(saveTimer.current)
      clearTimeout(savedTimer.current)
    },
    [],
  )

  const clean = (ps: string[]) => ps.map((p) => p.trim()).filter((p) => p !== "")

  // doSave persists whatever is currently valid. Validation-gated per block so an
  // invalid listen/endpoint never blocks a visibility/discovery/resilience change.
  // Does NOT touch tools.mcp.servers (owned by the Servers page) and does NOT
  // refetch config, so in-progress edits survive.
  const doSave = async () => {
    const f = formRef.current
    const listenErr = validateListen(f.listen)
    const pathErr = validateEndpointPath(f.endpointPath)

    const patch: Record<string, unknown> = {}
    if (!listenErr && !pathErr) {
      patch.mcp_host = {
        enabled: f.enabled,
        auto_enable: f.autoEnable,
        listen: f.listen.trim(),
        endpoint_path: f.endpointPath.trim(),
        internal_tools: clean(f.internalToolPatterns),
        external_tools: clean(f.externalToolPatterns),
      }
    }
    // tools.discovery + tools.mcp resilience knobs. A JSON merge patch leaves
    // tools.mcp.servers untouched because we never include that key here.
    const toolsPatch: Record<string, unknown> = {}
    if (!listenErr && !pathErr) {
      toolsPatch.discovery = {
        enabled: f.discoveryEnabled,
        ttl_max: f.ttlMax,
        visible_budget: f.visibleBudget,
        always_shown_namespaces: clean(f.alwaysShownNamespaces),
      }
    }
    toolsPatch.mcp = {
      reconnect_cooldown_seconds: f.reconnectCooldownSeconds,
      call_timeout_seconds: f.callTimeoutSeconds,
      liveness_probe_seconds: f.livenessProbeSeconds,
    }
    patch.tools = toolsPatch

    if (Object.keys(patch).length === 0) {
      setStatus("error")
      return
    }

    setStatus("saving")
    try {
      await patchAppConfig(patch)
      if (listenErr || pathErr) {
        setStatus("error")
      } else {
        setStatus("saved")
        clearTimeout(savedTimer.current)
        savedTimer.current = setTimeout(() => setStatus(null), 2000)
      }
    } catch (e) {
      setStatus("error")
      toast.error(e instanceof Error ? e.message : t("pages.mcp.save_error"))
    }
  }

  const scheduleSave = () => {
    clearTimeout(saveTimer.current)
    saveTimer.current = setTimeout(() => void doSave(), 600)
  }

  const updateField = <K extends keyof MCPHostForm>(
    key: K,
    value: MCPHostForm[K],
  ) => {
    setForm((prev) => ({ ...prev, [key]: value }))
    scheduleSave()
  }

  return (
    <div className="flex h-full flex-col">
      <PageHeader title={t("navigation.mcp_config")}>
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
      </PageHeader>
      <div className="flex-1 overflow-auto p-3 lg:p-6">
        <div className="w-full max-w-[1000px] space-y-6">
          {loading ? (
            <div className="text-muted-foreground py-6 text-sm">
              {t("labels.loading")}
            </div>
          ) : loadError ? (
            <div className="text-destructive py-6 text-sm">
              {t("pages.mcp.load_error")}
            </div>
          ) : (
            <div className="space-y-6">
              <EnableSection form={form} onFieldChange={updateField} />

              <TransportSection form={form} onFieldChange={updateField} />

              <ToolsSection
                title={t("pages.mcp.sections.internal_tools")}
                description={t("pages.mcp.internal_tools_desc")}
                note={t("pages.mcp.internal_tools_note")}
                patterns={form.internalToolPatterns}
                onChange={(next) => updateField("internalToolPatterns", next)}
              />

              <ToolsSection
                title={t("pages.mcp.sections.external_tools")}
                description={t("pages.mcp.external_tools_desc")}
                note={t("pages.mcp.external_tools_note")}
                patterns={form.externalToolPatterns}
                onChange={(next) => updateField("externalToolPatterns", next)}
              />

              <DiscoverySection
                discoveryEnabled={form.discoveryEnabled}
                ttlMax={form.ttlMax}
                visibleBudget={form.visibleBudget}
                alwaysShownNamespaces={form.alwaysShownNamespaces}
                onFieldChange={updateField}
                onNamespacesChange={(next) =>
                  updateField("alwaysShownNamespaces", next)
                }
              />

              <ResilienceSection
                reconnectCooldownSeconds={form.reconnectCooldownSeconds}
                callTimeoutSeconds={form.callTimeoutSeconds}
                livenessProbeSeconds={form.livenessProbeSeconds}
                onFieldChange={updateField}
              />
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
