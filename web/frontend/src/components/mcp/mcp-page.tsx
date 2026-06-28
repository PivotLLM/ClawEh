import { useQuery } from "@tanstack/react-query"
import { useEffect, useRef, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import { patchAppConfig } from "@/api/channels"
import {
  EMPTY_MCP_FORM,
  type MCPHostForm,
  buildMCPFormFromConfig,
  serversToPatch,
  validateEndpointPath,
  validateListen,
  validateServers,
} from "@/components/mcp/form-model"
import {
  ClientServersSection,
  EnableSection,
  ToolsSection,
  TransportSection,
} from "@/components/mcp/mcp-sections"
import { PageHeader } from "@/components/page-header"

type SaveStatus = "saving" | "saved" | "error" | null

export function MCPPage() {
  const { t } = useTranslation()
  const [form, setForm] = useState<MCPHostForm>(EMPTY_MCP_FORM)
  const [status, setStatus] = useState<SaveStatus>(null)

  // baselineRef tracks the last-saved form so server diffs (serversToPatch) are
  // computed against what's actually persisted. formRef mirrors the latest form
  // so the debounced save reads current values.
  const baselineRef = useRef<MCPHostForm>(EMPTY_MCP_FORM)
  const formRef = useRef<MCPHostForm>(form)
  formRef.current = form
  const saveTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const savedTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)

  const { data, isLoading, error } = useQuery({
    queryKey: ["config"],
    queryFn: async () => {
      const res = await fetch("/api/config")
      if (!res.ok) throw new Error("Failed to load config")
      return res.json()
    },
  })

  useEffect(() => {
    if (!data) return
    const parsed = buildMCPFormFromConfig(data)
    setForm(parsed)
    baselineRef.current = parsed
  }, [data])

  // Clear timers on unmount.
  useEffect(
    () => () => {
      clearTimeout(saveTimer.current)
      clearTimeout(savedTimer.current)
    },
    [],
  )

  const clean = (ps: string[]) => ps.map((p) => p.trim()).filter((p) => p !== "")

  // doSave persists whatever is currently valid. Validation-gated per block so a
  // half-typed server never blocks saving a visibility/toggle change, and an
  // invalid listen/endpoint never blocks the servers. Does NOT refetch config, so
  // in-progress edits (e.g. an empty pattern row) survive.
  const doSave = async () => {
    const f = formRef.current
    const listenErr = validateListen(f.listen)
    const pathErr = validateEndpointPath(f.endpointPath)
    const serversErr = validateServers(f.servers)

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
    if (!serversErr) {
      patch.tools = {
        mcp: { servers: serversToPatch(f.servers, baselineRef.current.servers) },
      }
    }
    if (Object.keys(patch).length === 0) {
      setStatus("error")
      return
    }

    setStatus("saving")
    try {
      await patchAppConfig(patch)
      // Advance the saved baseline; keep the prior servers baseline if we didn't
      // persist servers this round (so the next diff is still correct).
      baselineRef.current = {
        ...f,
        servers: serversErr ? baselineRef.current.servers : f.servers,
      }
      if (listenErr || pathErr || serversErr) {
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

  const serversError = validateServers(form.servers)

  return (
    <div className="flex h-full flex-col">
      <PageHeader title={t("navigation.mcp")}>
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
          {isLoading ? (
            <div className="text-muted-foreground py-6 text-sm">
              {t("labels.loading")}
            </div>
          ) : error ? (
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

              <ClientServersSection
                servers={form.servers}
                error={serversError}
                onChange={(next) => updateField("servers", next)}
              />
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
