import { IconDeviceFloppy } from "@tabler/icons-react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { useEffect, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import { patchAppConfig } from "@/api/channels"
import { getTools } from "@/api/tools"
import {
  EMPTY_MCP_FORM,
  type MCPHostForm,
  buildMCPFormFromConfig,
  validateEndpointPath,
  validateListen,
} from "@/components/mcp/form-model"
import {
  EnableSection,
  ToolsSection,
  TransportSection,
} from "@/components/mcp/mcp-sections"
import { PageHeader } from "@/components/page-header"
import { Button } from "@/components/ui/button"

export function MCPPage() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [form, setForm] = useState<MCPHostForm>(EMPTY_MCP_FORM)
  const [baseline, setBaseline] = useState<MCPHostForm>(EMPTY_MCP_FORM)
  const [saving, setSaving] = useState(false)

  const { data, isLoading, error } = useQuery({
    queryKey: ["config"],
    queryFn: async () => {
      const res = await fetch("/api/config")
      if (!res.ok) throw new Error("Failed to load config")
      return res.json()
    },
  })

  const { data: toolsData, isLoading: toolsLoading } = useQuery({
    queryKey: ["tools"],
    queryFn: getTools,
  })

  useEffect(() => {
    if (!data) return
    const parsed = buildMCPFormFromConfig(data)
    setForm(parsed)
    setBaseline(parsed)
  }, [data])

  const isDirty = JSON.stringify(form) !== JSON.stringify(baseline)

  const updateField = <K extends keyof MCPHostForm>(
    key: K,
    value: MCPHostForm[K],
  ) => {
    setForm((prev) => ({ ...prev, [key]: value }))
  }

  const handleReset = () => {
    setForm(baseline)
    toast.info(t("pages.mcp.reset_success"))
  }

  const handleSave = async () => {
    try {
      setSaving(true)

      const listenErr = validateListen(form.listen)
      if (listenErr) throw new Error(listenErr)
      const pathErr = validateEndpointPath(form.endpointPath)
      if (pathErr) throw new Error(pathErr)

      const cleanedPatterns = form.toolPatterns
        .map((p) => p.trim())
        .filter((p) => p !== "")

      await patchAppConfig({
        mcp_host: {
          enabled: form.enabled,
          auto_enable: form.autoEnable,
          listen: form.listen.trim(),
          endpoint_path: form.endpointPath.trim(),
          tools: cleanedPatterns,
        },
      })

      const nextForm: MCPHostForm = { ...form, toolPatterns: cleanedPatterns }
      setForm(nextForm)
      setBaseline(nextForm)
      queryClient.invalidateQueries({ queryKey: ["config"] })
      toast.success(t("pages.mcp.save_success"))
    } catch (err) {
      toast.error(
        err instanceof Error ? err.message : t("pages.mcp.save_error"),
      )
    } finally {
      setSaving(false)
    }
  }

  const registeredTools = (toolsData?.tools ?? []).map((t) => t.name)

  return (
    <div className="flex h-full flex-col">
      <PageHeader title={t("navigation.mcp")} />
      <div className="flex-1 overflow-auto p-3 lg:p-6">
        <div className="mx-auto w-full max-w-[1000px] space-y-6">
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
              {isDirty && (
                <div className="bg-yellow-50 px-3 py-2 text-sm text-yellow-700">
                  {t("pages.mcp.unsaved_changes")}
                </div>
              )}

              <EnableSection form={form} onFieldChange={updateField} />

              <TransportSection form={form} onFieldChange={updateField} />

              <ToolsSection
                form={form}
                onFieldChange={updateField}
                registeredTools={registeredTools}
                toolsLoading={toolsLoading}
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
