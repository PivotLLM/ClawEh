import { IconPlus, IconTrash } from "@tabler/icons-react"
import type { ReactNode } from "react"
import { useTranslation } from "react-i18next"

import {
  ALWAYS_EXCLUDED_TOOLS,
  type MCPHostForm,
  matchToolPattern,
  validateEndpointPath,
  validateListen,
} from "@/components/mcp/form-model"
import { Field, SwitchCardField } from "@/components/shared-form"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Input } from "@/components/ui/input"

type UpdateMCPField = <K extends keyof MCPHostForm>(
  key: K,
  value: MCPHostForm[K],
) => void

interface SectionCardProps {
  title: string
  description?: string
  children: ReactNode
}

function SectionCard({ title, description, children }: SectionCardProps) {
  return (
    <Card size="sm">
      <CardHeader className="border-border border-b">
        <CardTitle>{title}</CardTitle>
        {description && <CardDescription>{description}</CardDescription>}
      </CardHeader>
      <CardContent className="pt-0">
        <div className="divide-border/70 divide-y">{children}</div>
      </CardContent>
    </Card>
  )
}

interface EnableSectionProps {
  form: MCPHostForm
  onFieldChange: UpdateMCPField
}

export function EnableSection({ form, onFieldChange }: EnableSectionProps) {
  const { t } = useTranslation()
  return (
    <SectionCard
      title={t("pages.mcp.sections.enable")}
      description={t("pages.mcp.enable_desc")}
    >
      <SwitchCardField
        label={t("pages.mcp.enabled")}
        hint={t("pages.mcp.enabled_hint")}
        layout="setting-row"
        checked={form.enabled}
        onCheckedChange={(checked) => onFieldChange("enabled", checked)}
      />
      <SwitchCardField
        label={t("pages.mcp.auto_enable")}
        hint={t("pages.mcp.auto_enable_hint")}
        layout="setting-row"
        checked={form.autoEnable}
        onCheckedChange={(checked) => onFieldChange("autoEnable", checked)}
      />
    </SectionCard>
  )
}

interface TransportSectionProps {
  form: MCPHostForm
  onFieldChange: UpdateMCPField
}

export function TransportSection({
  form,
  onFieldChange,
}: TransportSectionProps) {
  const { t } = useTranslation()
  const listenError = validateListen(form.listen) ?? undefined
  const pathError = validateEndpointPath(form.endpointPath) ?? undefined
  return (
    <SectionCard
      title={t("pages.mcp.sections.transport")}
      description={t("pages.mcp.transport_desc")}
    >
      <Field
        label={t("pages.mcp.listen")}
        hint={t("pages.mcp.listen_hint")}
        error={listenError}
        layout="setting-row"
      >
        <Input
          value={form.listen}
          onChange={(e) => onFieldChange("listen", e.target.value)}
          placeholder="127.0.0.1:5911"
        />
      </Field>
      <Field
        label={t("pages.mcp.endpoint_path")}
        hint={t("pages.mcp.endpoint_path_hint")}
        error={pathError}
        layout="setting-row"
      >
        <Input
          value={form.endpointPath}
          onChange={(e) => onFieldChange("endpointPath", e.target.value)}
          placeholder="/mcp"
        />
      </Field>
    </SectionCard>
  )
}

interface ToolsSectionProps {
  form: MCPHostForm
  onFieldChange: UpdateMCPField
  registeredTools: string[]
  toolsLoading: boolean
}

export function ToolsSection({
  form,
  onFieldChange,
  registeredTools,
  toolsLoading,
}: ToolsSectionProps) {
  const { t } = useTranslation()

  const setPatternAt = (index: number, value: string) => {
    const next = [...form.toolPatterns]
    next[index] = value
    onFieldChange("toolPatterns", next)
  }

  const addPattern = () => {
    onFieldChange("toolPatterns", [...form.toolPatterns, ""])
  }

  const removePatternAt = (index: number) => {
    const next = form.toolPatterns.filter((_, i) => i !== index)
    onFieldChange("toolPatterns", next)
  }

  const filteredCandidates = registeredTools.filter(
    (name) => !ALWAYS_EXCLUDED_TOOLS.includes(name),
  )

  const matched = filteredCandidates.filter((name) =>
    matchToolPattern(form.toolPatterns, name),
  )
  const excluded = filteredCandidates.filter(
    (name) => !matchToolPattern(form.toolPatterns, name),
  )

  return (
    <SectionCard
      title={t("pages.mcp.sections.tools")}
      description={t("pages.mcp.tools_desc")}
    >
      <div className="space-y-3 py-4">
        <div className="text-muted-foreground text-xs">
          {t("pages.mcp.tools_pattern_hint")}
        </div>

        <div className="space-y-2">
          {form.toolPatterns.length === 0 ? (
            <div className="text-muted-foreground text-xs italic">
              {t("pages.mcp.tools_empty_warning")}
            </div>
          ) : (
            form.toolPatterns.map((pattern, idx) => (
              <div
                key={idx}
                className="flex items-center gap-2"
              >
                <Input
                  value={pattern}
                  placeholder={t("pages.mcp.tools_pattern_placeholder")}
                  onChange={(e) => setPatternAt(idx, e.target.value)}
                />
                <Button
                  type="button"
                  variant="outline"
                  size="icon"
                  onClick={() => removePatternAt(idx)}
                  aria-label={t("common.remove")}
                >
                  <IconTrash className="size-4" />
                </Button>
              </div>
            ))
          )}
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={addPattern}
          >
            <IconPlus className="size-4" />
            {t("pages.mcp.tools_add_pattern")}
          </Button>
        </div>

        <div className="border-border/70 border-t pt-3">
          <div className="text-sm font-medium">
            {t("pages.mcp.tools_preview_title", {
              matched: matched.length,
              total: filteredCandidates.length,
            })}
          </div>
          <div className="text-muted-foreground mb-2 text-xs">
            {t("pages.mcp.tools_always_excluded", {
              tools: ALWAYS_EXCLUDED_TOOLS.join(", "),
            })}
          </div>

          {toolsLoading ? (
            <div className="text-muted-foreground text-xs">
              {t("labels.loading")}
            </div>
          ) : filteredCandidates.length === 0 ? (
            <div className="text-muted-foreground text-xs italic">
              {t("pages.mcp.tools_none_registered")}
            </div>
          ) : (
            <div className="grid grid-cols-1 gap-x-4 gap-y-1 sm:grid-cols-2 md:grid-cols-3">
              {matched.map((name) => (
                <div
                  key={`m-${name}`}
                  className="font-mono text-xs text-green-600"
                >
                  ✓ {name}
                </div>
              ))}
              {excluded.map((name) => (
                <div
                  key={`e-${name}`}
                  className="text-muted-foreground/70 font-mono text-xs"
                >
                  ✗ {name}
                </div>
              ))}
            </div>
          )}
        </div>
      </div>
    </SectionCard>
  )
}
