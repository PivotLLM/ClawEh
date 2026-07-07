import { IconPlus, IconTrash } from "@tabler/icons-react"
import type { ReactNode } from "react"
import { useTranslation } from "react-i18next"

import {
  type MCPHostForm,
  type MCPServerForm,
  blankServer,
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Textarea } from "@/components/ui/textarea"

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
  title: string
  description: string
  note: string
  patterns: string[]
  onChange: (next: string[]) => void
}

export function ToolsSection({
  title,
  description,
  note,
  patterns,
  onChange,
}: ToolsSectionProps) {
  const { t } = useTranslation()

  const setPatternAt = (index: number, value: string) => {
    const next = [...patterns]
    next[index] = value
    onChange(next)
  }

  const addPattern = () => {
    onChange([...patterns, ""])
  }

  const removePatternAt = (index: number) => {
    onChange(patterns.filter((_, i) => i !== index))
  }

  return (
    <SectionCard title={title} description={description}>
      <div className="space-y-3 py-4">
        <div className="text-muted-foreground text-xs">{note}</div>
        <div className="text-muted-foreground text-xs">
          {t("pages.mcp.tools_pattern_hint")}
        </div>

        <div className="space-y-2">
          {patterns.length === 0 ? (
            <div className="text-muted-foreground text-xs italic">
              {t("pages.mcp.tools_empty_warning")}
            </div>
          ) : (
            patterns.map((pattern, idx) => (
              <div key={idx} className="flex items-center gap-2">
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
          <Button type="button" variant="outline" size="sm" onClick={addPattern}>
            <IconPlus className="size-4" />
            {t("pages.mcp.tools_add_pattern")}
          </Button>
        </div>
      </div>
    </SectionCard>
  )
}

interface DiscoverySectionProps {
  discoveryEnabled: boolean
  alwaysShownNamespaces: string[]
  onFieldChange: UpdateMCPField
  onNamespacesChange: (next: string[]) => void
}

// DiscoverySection edits the global progressive-tool-discovery switch
// (tools.discovery.enabled) and the extra always-shown namespaces
// (mcp_host.always_shown_namespaces).
export function DiscoverySection({
  discoveryEnabled,
  alwaysShownNamespaces,
  onFieldChange,
  onNamespacesChange,
}: DiscoverySectionProps) {
  const { t } = useTranslation()

  const setAt = (index: number, value: string) => {
    const next = [...alwaysShownNamespaces]
    next[index] = value
    onNamespacesChange(next)
  }
  const add = () => onNamespacesChange([...alwaysShownNamespaces, ""])
  const removeAt = (index: number) =>
    onNamespacesChange(alwaysShownNamespaces.filter((_, i) => i !== index))

  return (
    <SectionCard
      title="Progressive Tool Discovery"
      description="Hide most tools behind a search so agents and MCP clients start with a small set and load tools on demand. Global switch, off by default."
    >
      <SwitchCardField
        label="Enable progressive discovery"
        hint="When on, the fusion/maestro suites and upstream MCP tools are hidden and found via search_tools / get_tool_details. Native tools stay in each agent's own context; the MCP host's tools/list shows only the always-shown namespaces below (plus search + cogmem)."
        checked={discoveryEnabled}
        onCheckedChange={(checked) => onFieldChange("discoveryEnabled", checked)}
      />
      {discoveryEnabled && (
        <div className="space-y-3 py-4">
          <div className="text-muted-foreground text-xs">
            Extra namespaces always shown in the MCP host&apos;s tools/list.
            search_tools, get_tool_details, and cogmem are always shown by rule;
            add more here (e.g. &quot;file&quot;, &quot;session&quot;). Everything
            else is discovered on demand.
          </div>
          <div className="space-y-2">
            {alwaysShownNamespaces.length === 0 ? (
              <div className="text-muted-foreground text-xs italic">
                Only search_tools, get_tool_details, and cogmem are shown up front.
              </div>
            ) : (
              alwaysShownNamespaces.map((ns, idx) => (
                <div key={idx} className="flex items-center gap-2">
                  <Input
                    value={ns}
                    placeholder="namespace (e.g. file)"
                    onChange={(e) => setAt(idx, e.target.value)}
                  />
                  <Button
                    type="button"
                    variant="outline"
                    size="icon"
                    onClick={() => removeAt(idx)}
                    aria-label={t("common.remove")}
                  >
                    <IconTrash className="size-4" />
                  </Button>
                </div>
              ))
            )}
            <Button type="button" variant="outline" size="sm" onClick={add}>
              <IconPlus className="size-4" />
              Add namespace
            </Button>
          </div>
        </div>
      )}
    </SectionCard>
  )
}

// ClientServersSection edits the external (upstream) MCP servers claw connects
// out to (tools.mcp.servers) via form fields — add / edit / delete, no raw JSON.
export function ClientServersSection({
  servers,
  error,
  onChange,
}: {
  servers: MCPServerForm[]
  error: string | null
  onChange: (next: MCPServerForm[]) => void
}) {
  const { t } = useTranslation()

  const update = (i: number, patch: Partial<MCPServerForm>) => {
    onChange(servers.map((s, idx) => (idx === i ? { ...s, ...patch } : s)))
  }
  const remove = (i: number) => onChange(servers.filter((_, idx) => idx !== i))
  const add = () => onChange([...servers, blankServer()])

  return (
    <SectionCard
      title={t("pages.mcp.sections.client")}
      description={t("pages.mcp.client_hint")}
    >
      <div className="space-y-4 pt-2">
        {servers.length === 0 && (
          <div className="text-muted-foreground text-sm">
            {t("pages.mcp.client_empty")}
          </div>
        )}
        {servers.map((s, i) => (
          <div
            key={i}
            className="border-border/60 space-y-3 rounded-lg border p-3"
          >
            <div className="flex justify-end">
              <Button
                type="button"
                variant="outline"
                size="icon"
                aria-label={t("common.remove")}
                onClick={() => remove(i)}
              >
                <IconTrash className="size-4" />
              </Button>
            </div>

            <Field label={t("pages.mcp.server_name")} layout="setting-row">
              <Input
                value={s.name}
                onChange={(e) => update(i, { name: e.target.value })}
                placeholder={t("pages.mcp.server_name_ph")}
                className="font-mono"
              />
            </Field>

            <Field label={t("pages.mcp.server_type")} layout="setting-row">
              <Select
                value={s.type}
                onValueChange={(v) => update(i, { type: v as "stdio" | "http" })}
              >
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="http">http</SelectItem>
                  <SelectItem value="stdio">stdio</SelectItem>
                </SelectContent>
              </Select>
            </Field>

            <SwitchCardField
              label={t("pages.mcp.server_enabled")}
              checked={s.enabled}
              onCheckedChange={(c) => update(i, { enabled: c })}
              layout="setting-row"
            />

            {s.type === "http" ? (
              <>
                <Field label={t("pages.mcp.server_url")} layout="setting-row">
                  <Input
                    value={s.url}
                    onChange={(e) => update(i, { url: e.target.value })}
                    placeholder="http://127.0.0.1:9999/mcp"
                    className="font-mono"
                  />
                </Field>
                <Field
                  label={t("pages.mcp.server_headers")}
                  hint={t("pages.mcp.server_headers_hint")}
                  layout="setting-row"
                >
                  <Textarea
                    value={s.headers}
                    onChange={(e) => update(i, { headers: e.target.value })}
                    placeholder={"Authorization: Bearer xyz"}
                    className="min-h-[60px] font-mono text-xs"
                    spellCheck={false}
                  />
                </Field>
              </>
            ) : (
              <>
                <Field
                  label={t("pages.mcp.server_command")}
                  layout="setting-row"
                >
                  <Input
                    value={s.command}
                    onChange={(e) => update(i, { command: e.target.value })}
                    placeholder="npx"
                    className="font-mono"
                  />
                </Field>
                <Field
                  label={t("pages.mcp.server_args")}
                  hint={t("pages.mcp.server_args_hint")}
                  layout="setting-row"
                >
                  <Textarea
                    value={s.args}
                    onChange={(e) => update(i, { args: e.target.value })}
                    placeholder={"-y\n@modelcontextprotocol/server-foo"}
                    className="min-h-[60px] font-mono text-xs"
                    spellCheck={false}
                  />
                </Field>
                <Field
                  label={t("pages.mcp.server_env")}
                  hint={t("pages.mcp.server_env_hint")}
                  layout="setting-row"
                >
                  <Textarea
                    value={s.env}
                    onChange={(e) => update(i, { env: e.target.value })}
                    placeholder={"API_KEY=..."}
                    className="min-h-[60px] font-mono text-xs"
                    spellCheck={false}
                  />
                </Field>
                <Field
                  label={t("pages.mcp.server_env_file")}
                  layout="setting-row"
                >
                  <Input
                    value={s.envFile}
                    onChange={(e) => update(i, { envFile: e.target.value })}
                    placeholder="/path/to/.env"
                    className="font-mono"
                  />
                </Field>
              </>
            )}
          </div>
        ))}

        {error && <div className="text-destructive text-xs">{error}</div>}

        <Button type="button" variant="outline" size="sm" onClick={add}>
          <IconPlus className="size-4" />
          {t("pages.mcp.server_add")}
        </Button>
      </div>
    </SectionCard>
  )
}
