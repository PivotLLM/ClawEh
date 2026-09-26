import { IconRefresh } from "@tabler/icons-react"
import { useInfiniteQuery } from "@tanstack/react-query"
import { useState } from "react"
import { useTranslation } from "react-i18next"

import { AUDIT_KINDS, type AuditEvent, listAudit } from "@/api/audit"
import { PageHeader } from "@/components/page-header"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"

const PAGE_SIZE = 100

// The kind filter's "everything" value. The Select cannot hold an empty string
// as a value, so a sentinel stands in for "no filter".
const ALL_KINDS = "all"

const COLUMNS = [
  "time",
  "kind",
  "who",
  "channel",
  "what",
  "outcome",
  "duration",
] as const

/** The row's "who": the operator for HTTP-originated events, the agent (and
 *  the chat sender, when known) for tool calls. */
function whoLabel(e: AuditEvent): string {
  if (e.actor) return e.actor
  if (e.agent && e.sender) return `${e.agent} · ${e.sender}`
  return e.agent ?? e.sender ?? ""
}

/** The row's "what": the tool name for a tool call, otherwise the summary
 *  (changed config keys, or the auth action). */
function whatLabel(e: AuditEvent): string {
  if (e.tool) return e.tool
  return e.summary ?? ""
}

function Dash() {
  return <span className="opacity-40">—</span>
}

export function AuditPage() {
  const { t } = useTranslation()
  const [kind, setKind] = useState<string>(ALL_KINDS)
  const [agentInput, setAgentInput] = useState("")
  const [agent, setAgent] = useState("")
  const kindParam = kind === ALL_KINDS ? "" : kind

  // Pages are keyed by before_id: each page's next_before_id is the cursor for
  // the one after it. Fetched on mount and on explicit refresh only, so reading
  // history is never interrupted by a background update.
  const query = useInfiniteQuery({
    queryKey: ["audit", kindParam, agent],
    queryFn: ({ pageParam }) =>
      listAudit({
        kind: kindParam,
        agent,
        limit: PAGE_SIZE,
        before_id: pageParam,
      }),
    initialPageParam: 0,
    getNextPageParam: (last) =>
      last.events.length < PAGE_SIZE || last.next_before_id === 0
        ? undefined
        : last.next_before_id,
    staleTime: Infinity,
    refetchOnWindowFocus: false,
  })

  const rows = query.data?.pages.flatMap((p) => p.events) ?? []
  const dropped = query.data?.pages.at(-1)?.dropped ?? 0
  const loading = query.isFetching
  const error = query.error instanceof Error ? query.error.message : ""

  const applyAgent = () => setAgent(agentInput.trim())

  return (
    <div className="flex h-full flex-col">
      <PageHeader title={t("navigation.audit")}>
        <div className="flex items-center gap-2">
          <Select value={kind} onValueChange={setKind}>
            <SelectTrigger
              className="w-40"
              aria-label={t("pages.audit.filter_kind")}
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL_KINDS}>
                {t("pages.audit.all_kinds")}
              </SelectItem>
              {AUDIT_KINDS.map((k) => (
                <SelectItem key={k} value={k}>
                  {t(`pages.audit.kind.${k}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Input
            value={agentInput}
            onChange={(e) => setAgentInput(e.target.value)}
            onBlur={applyAgent}
            onKeyDown={(e) => {
              if (e.key === "Enter") applyAgent()
            }}
            placeholder={t("pages.audit.filter_agent")}
            aria-label={t("pages.audit.filter_agent")}
            className="w-40"
          />
          <Button
            variant="outline"
            size="sm"
            onClick={() => void query.refetch()}
            disabled={loading}
          >
            <IconRefresh
              className={`size-4 ${loading ? "animate-spin" : ""}`}
            />
            {t("pages.audit.refresh")}
          </Button>
        </div>
      </PageHeader>

      <div className="flex flex-1 flex-col gap-4 overflow-auto p-4 sm:p-8">
        {error && (
          <div className="text-destructive bg-destructive/10 rounded-lg px-4 py-3 text-sm">
            {error}
          </div>
        )}
        {dropped > 0 && (
          <div className="text-muted-foreground bg-muted/40 rounded-lg px-4 py-2 text-xs">
            {t("pages.audit.dropped", { count: dropped })}
          </div>
        )}
        <div className="border-border/60 bg-card overflow-hidden rounded-xl border">
          {rows.length === 0 && !loading ? (
            <p className="text-muted-foreground px-4 py-6 text-center text-sm">
              {t("pages.audit.empty")}
            </p>
          ) : (
            <table className="w-full text-sm">
              <thead>
                <tr className="border-border/40 border-b">
                  {COLUMNS.map((col) => (
                    <th
                      key={col}
                      className="text-muted-foreground px-4 py-2.5 text-left text-xs font-medium"
                    >
                      {t(`pages.audit.col.${col}`)}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {rows.map((e) => (
                  <tr
                    key={e.id}
                    className="border-border/30 hover:bg-muted/20 border-b transition-colors last:border-0"
                    title={e.details || undefined}
                  >
                    <td className="px-4 py-2.5 font-mono text-xs whitespace-nowrap">
                      {new Date(e.ts).toLocaleString()}
                    </td>
                    <td className="px-4 py-2.5 text-xs">
                      {t(`pages.audit.kind.${e.kind}`, {
                        defaultValue: e.kind,
                      })}
                    </td>
                    <td className="px-4 py-2.5 font-mono text-xs">
                      {whoLabel(e) || <Dash />}
                    </td>
                    <td className="text-muted-foreground px-4 py-2.5 font-mono text-xs">
                      {e.channel || <Dash />}
                    </td>
                    <td className="px-4 py-2.5 font-mono text-xs break-all">
                      {whatLabel(e) || <Dash />}
                    </td>
                    <td
                      className={`px-4 py-2.5 text-xs ${e.outcome === "error" ? "text-destructive" : ""}`}
                    >
                      {e.outcome || <Dash />}
                    </td>
                    <td className="text-muted-foreground px-4 py-2.5 text-right font-mono text-xs">
                      {e.kind === "tool_call" ? `${e.duration_ms} ms` : ""}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
        {query.hasNextPage && (
          <div className="flex justify-center">
            <Button
              variant="outline"
              size="sm"
              onClick={() => void query.fetchNextPage()}
              disabled={loading}
            >
              {loading ? t("pages.audit.loading") : t("pages.audit.load_more")}
            </Button>
          </div>
        )}
      </div>
    </div>
  )
}
