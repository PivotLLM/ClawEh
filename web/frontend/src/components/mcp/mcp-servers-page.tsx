import { IconPlus, IconRefresh, IconTrash } from "@tabler/icons-react"
import { useQuery } from "@tanstack/react-query"
import { useCallback, useEffect, useRef, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import {
  type MCPServerStatus,
  getAppConfig,
  getMCPStatus,
  patchAppConfig,
} from "@/api/channels"
import { reloadGateway } from "@/api/system"
import {
  type MCPServerForm,
  blankServer,
  serversFromConfig,
  serversToPatch,
  validateServers,
} from "@/components/mcp/form-model"
import { ServerFields, ServerStatusBadge } from "@/components/mcp/mcp-sections"
import { PageHeader } from "@/components/page-header"
import { Button } from "@/components/ui/button"

type SaveStatus = "saving" | "saved" | "error" | null

// serverDotClass maps a server's resolved state to a solid dot colour for the
// left-rail list (mirrors the ServerStatusBadge pill, without the chrome).
function serverDotClass(
  live: MCPServerStatus | undefined,
  enabled: boolean,
): string {
  const state = live?.state ?? (enabled ? "disconnected" : "disabled")
  switch (state) {
    case "connected":
      return "bg-emerald-500"
    case "reconnecting":
      return "bg-amber-500"
    case "cooldown":
      return "bg-destructive"
    default:
      return "bg-muted-foreground/40"
  }
}

// MCPServersPage edits the external (upstream) MCP servers claw connects out to
// (tools.mcp.servers). Two-column list/detail — a rail of servers on the left,
// the selected server's fields on the right — so many servers stay manageable,
// mirroring the Agents page. It patches only tools.mcp.servers.
export function MCPServersPage() {
  const { t } = useTranslation()
  const [servers, setServers] = useState<MCPServerForm[]>([])
  const [selectedIdx, setSelectedIdx] = useState(-1)
  const [status, setStatus] = useState<SaveStatus>(null)
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState("")
  const [applying, setApplying] = useState(false)

  // serversRef mirrors the latest list so debounced saves and handlers compute
  // against current values. baselineRef tracks the last-saved list so the diff
  // (serversToPatch) is against what's actually persisted.
  const serversRef = useRef<MCPServerForm[]>(servers)
  useEffect(() => {
    serversRef.current = servers
  }, [servers])
  const baselineRef = useRef<MCPServerForm[]>([])
  const initedRef = useRef(false)
  const saveTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const savedTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)

  // Live connection state, polled every 5s. Keyed by server name; a configured
  // server absent here is treated as disconnected.
  const { data: statusData } = useQuery({
    queryKey: ["mcp-status"],
    queryFn: getMCPStatus,
    refetchInterval: 5000,
  })
  const statusByName = new Map(
    (statusData?.servers ?? []).map((s) => [s.name, s]),
  )

  // Seed the editable list from config via an async callback (not a synchronous
  // setState in an effect) — the repo's pattern for query→form state.
  const loadData = useCallback(async () => {
    setLoading(true)
    try {
      const next = serversFromConfig(await getAppConfig())
      setServers(next)
      baselineRef.current = next
      if (!initedRef.current) {
        initedRef.current = true
        setSelectedIdx(next.length > 0 ? 0 : -1)
      }
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

  const doSave = async () => {
    const cur = serversRef.current
    if (validateServers(cur)) {
      setStatus("error")
      return
    }
    setStatus("saving")
    try {
      await patchAppConfig({
        tools: { mcp: { servers: serversToPatch(cur, baselineRef.current) } },
      })
      baselineRef.current = cur
      setStatus("saved")
      clearTimeout(savedTimer.current)
      savedTimer.current = setTimeout(() => setStatus(null), 2000)
    } catch (e) {
      setStatus("error")
      toast.error(e instanceof Error ? e.message : t("pages.mcp.save_error"))
    }
  }

  const scheduleSave = () => {
    clearTimeout(saveTimer.current)
    saveTimer.current = setTimeout(() => void doSave(), 600)
  }

  // applyNow flushes any pending edit to disk, then forces an immediate config
  // reload so a newly added/changed server connects right away instead of waiting
  // out the ~10-15s mtime-watcher debounce.
  const applyNow = async () => {
    clearTimeout(saveTimer.current)
    setApplying(true)
    try {
      await doSave()
      await reloadGateway()
      toast.success(t("pages.mcp.apply_done"))
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t("pages.mcp.save_error"))
    } finally {
      setApplying(false)
    }
  }

  const addServer = () => {
    // Select the new (last) row; a blank server has no name, so serversToPatch
    // skips it — nothing is saved until the user names it (no premature write).
    setSelectedIdx(servers.length)
    setServers((prev) => [...prev, blankServer()])
  }

  const updateSelected = (patch: Partial<MCPServerForm>) => {
    setServers((prev) =>
      prev.map((s, i) => (i === selectedIdx ? { ...s, ...patch } : s)),
    )
    scheduleSave()
  }

  const removeSelected = () => {
    if (selectedIdx < 0) return
    const nextLen = servers.length - 1
    setSelectedIdx(nextLen === 0 ? -1 : Math.min(selectedIdx, nextLen - 1))
    setServers((prev) => prev.filter((_, i) => i !== selectedIdx))
    scheduleSave()
  }

  const selected = selectedIdx >= 0 ? servers[selectedIdx] : undefined
  const listError = validateServers(servers)

  return (
    <div className="flex h-full flex-col">
      <PageHeader title={t("navigation.mcp_servers")}>
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
          <Button
            size="sm"
            variant="outline"
            onClick={() => void applyNow()}
            disabled={applying}
            title={t("pages.mcp.apply_hint")}
          >
            <IconRefresh className={`size-4 ${applying ? "animate-spin" : ""}`} />
            {t("pages.mcp.apply_now")}
          </Button>
          <Button size="sm" variant="outline" onClick={addServer}>
            <IconPlus className="size-4" />
            {t("pages.mcp.server_add")}
          </Button>
        </div>
      </PageHeader>

      <div className="min-h-0 flex flex-1">
        {/* Left rail: one entry per server. Selecting one shows just its fields. */}
        {!loading && !loadError && servers.length > 0 && (
          <nav className="border-border/60 w-52 shrink-0 space-y-0.5 overflow-y-auto border-r px-2 py-4">
            {servers.map((s, i) => {
              const active = i === selectedIdx
              return (
                <button
                  key={i}
                  type="button"
                  onClick={() => setSelectedIdx(i)}
                  className={`flex w-full items-center gap-2 rounded-lg px-3 py-2 text-left text-sm transition-colors ${
                    active
                      ? "bg-accent text-accent-foreground font-medium"
                      : "text-muted-foreground hover:bg-accent/50"
                  }`}
                >
                  <span
                    className={`size-1.5 shrink-0 rounded-full ${serverDotClass(statusByName.get(s.name.trim()), s.enabled)}`}
                  />
                  <span className="truncate">
                    {s.name.trim() || t("pages.mcp.server_unnamed")}
                  </span>
                </button>
              )
            })}
          </nav>
        )}

        <div className="min-h-0 flex-1 overflow-y-auto px-4 pb-8 sm:px-6">
          <div className="w-full max-w-[800px] pt-4">
            {loading ? (
              <div className="text-muted-foreground py-6 text-sm">
                {t("labels.loading")}
              </div>
            ) : loadError ? (
              <div className="text-destructive py-6 text-sm">
                {t("pages.mcp.load_error")}
              </div>
            ) : servers.length === 0 ? (
              <p className="text-muted-foreground py-20 text-center text-sm">
                {t("pages.mcp.client_empty")}
              </p>
            ) : selected ? (
              <div className="space-y-3">
                <div className="flex items-center justify-between">
                  <ServerStatusBadge
                    live={statusByName.get(selected.name.trim())}
                    enabled={selected.enabled}
                  />
                  <Button
                    type="button"
                    variant="outline"
                    size="icon"
                    aria-label={t("common.remove")}
                    onClick={removeSelected}
                  >
                    <IconTrash className="size-4" />
                  </Button>
                </div>

                <ServerFields server={selected} onChange={updateSelected} />

                {listError && (
                  <div className="text-destructive text-xs">{listError}</div>
                )}
              </div>
            ) : null}
          </div>
        </div>
      </div>
    </div>
  )
}
