import {
  IconEdit,
  IconLoader2,
  IconPlus,
  IconTrash,
  IconX,
} from "@tabler/icons-react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { useMemo, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import { getAppConfig, getSecMsgAccounts, patchAppConfig } from "@/api/channels"
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

interface Binding {
  agent_id: string
  match: {
    channel: string
    peer?: { kind: string; id: string }
  }
  agent_mentions?: string[]
  // raw is the original config object for this binding, preserved so a save
  // round-trips fields the UI does not model (default, deliver_to,
  // deliver_peer_kind, group_trigger, …). Absent for bindings added in the UI.
  raw?: Record<string, unknown>
}

// ── config parsers ──────────────────────────────────────────────────────────

function asRecord(v: unknown): Record<string, unknown> {
  if (v && typeof v === "object" && !Array.isArray(v))
    return v as Record<string, unknown>
  return {}
}
function asArray(v: unknown): unknown[] {
  return Array.isArray(v) ? v : []
}
function asString(v: unknown): string {
  return typeof v === "string" ? v : ""
}

function parseBindings(cfg: unknown): Binding[] {
  return asArray(asRecord(cfg).bindings).map((b) => {
    const r = asRecord(b)
    const match = asRecord(r.match)
    const peerRaw = match.peer ? asRecord(match.peer) : undefined
    const mentionsRaw = asArray(r.agent_mentions).map(asString).filter(Boolean)
    return {
      agent_id: asString(r.agent_id),
      match: {
        channel: asString(match.channel),
        peer: peerRaw
          ? { kind: asString(peerRaw.kind), id: asString(peerRaw.id) }
          : undefined,
      },
      agent_mentions: mentionsRaw.length > 0 ? mentionsRaw : undefined,
      raw: r,
    }
  })
}

// secmsgChannelName mirrors config.SecMsgAccountConfig.ChannelName so binding
// options match the channel names the backend registers per account.
function secmsgChannelName(daemonName: string, account: string): string {
  const base = daemonName === "" ? "secmsg" : daemonName
  return account === "" ? base : `${base}-${account}`
}

function parseChannelNames(cfg: unknown): string[] {
  const channels = asRecord(asRecord(cfg).channels)
  const names: string[] = []

  const telegramSeen = new Set<string>()
  for (const bot of asArray(channels.telegram)) {
    const b = asRecord(bot)
    if (b.enabled !== false) {
      const id = asString(b.id)
      const channelName =
        !id || id === "default" ? "telegram" : `telegram-${id}`
      if (!telegramSeen.has(channelName)) {
        telegramSeen.add(channelName)
        names.push(channelName)
      }
    }
  }

  for (const name of ["webui", "slack", "discord", "matrix", "line"]) {
    if (asRecord(channels[name]).enabled === true) names.push(name)
  }

  // secmsg: one channel per account on each enabled daemon. Pinned accounts come
  // from config here; discovered accounts are merged in later via a live query
  // (see loadData), since they are not enumerated in config.
  for (const d of asArray(channels.secmsg)) {
    const daemon = asRecord(d)
    if (daemon.enabled === false || asString(daemon.address) === "") continue
    const daemonName = asString(daemon.name)
    for (const a of asArray(daemon.accounts)) {
      const acct = asRecord(a)
      const name =
        asString(acct.name) ||
        secmsgChannelName(daemonName, asString(acct.account))
      if (name) names.push(name)
    }
  }

  return names
}

function parseAgentIds(cfg: unknown): string[] {
  return asArray(asRecord(asRecord(cfg).agents).list)
    .filter((a) => asRecord(a).enabled !== false)
    .map((a) => asString(asRecord(a).id))
    .filter(Boolean)
}

// ── helpers ─────────────────────────────────────────────────────────────────

function peerLabel(peer?: { kind: string; id: string }): string {
  if (!peer) return ""
  if (peer.kind === "direct") return "Direct messages"
  if (peer.id) return `#${peer.id}`
  return peer.kind
}

function parseMentionsInput(s: string): string[] {
  return s
    .split(/[\s,]+/)
    .map((x) => x.trim())
    .filter(Boolean)
}

// ── peer type for the add form ──────────────────────────────────────────────

type SlackPeerType = "none" | "channel" | "direct"

// ── component ───────────────────────────────────────────────────────────────

export function BindingsPage() {
  const queryClient = useQueryClient()
  const { t } = useTranslation()
  const [saving, setSaving] = useState<string | null>(null)

  // inline edit state
  const [editIdx, setEditIdx] = useState<number | null>(null)
  const [editMentions, setEditMentions] = useState("")

  // add-form state
  const [showAdd, setShowAdd] = useState(false)
  const [addChannel, setAddChannel] = useState("")
  const [addAgent, setAddAgent] = useState("")
  const [addPeerType, setAddPeerType] = useState<SlackPeerType>("none")
  const [addPeerId, setAddPeerId] = useState("")
  const [addMentions, setAddMentions] = useState("")

  const {
    data: cfg,
    isPending: loading,
    error: loadError,
  } = useQuery({ queryKey: ["app-config"], queryFn: getAppConfig })

  // Each enabled secmsg daemon is asked for its live accounts, and the
  // discovered channel names are folded into the options. Discovered accounts
  // are not in config, so config parsing alone cannot surface them; a daemon
  // that is offline is simply skipped.
  //
  // A SECOND query rather than part of the one above, deliberately: an
  // unreachable daemon must not hold up the page. The bindings list renders as
  // soon as the config lands, and the discovered names appear when they arrive
  // — which is what the old fire-and-forget `void mergeSecMsgChannels(cfg)`
  // achieved, without the extra state write.
  const { data: discovered = [] } = useQuery({
    queryKey: ["secmsg-channels"],
    enabled: cfg !== undefined,
    queryFn: async () => {
      const daemons = asArray(asRecord(asRecord(cfg).channels).secmsg)
        .map(asRecord)
        .filter((d) => d.enabled !== false && asString(d.address) !== "")
      const names: string[] = []
      await Promise.all(
        daemons.map(async (d) => {
          const daemonName = asString(d.name)
          try {
            const r = await getSecMsgAccounts(daemonName || "secmsg")
            for (const acct of r.accounts) {
              names.push(secmsgChannelName(daemonName, acct))
            }
          } catch {
            // Daemon unreachable — skip; pinned accounts (if any) still list.
          }
        }),
      )
      return names
    },
  })

  const fetchError = loadError
    ? loadError instanceof Error
      ? loadError.message
      : "Failed to load"
    : ""

  // Derived straight from the queries — these were three pieces of state whose
  // only writer was the loader, which is the definition of derived data.
  const bindings = useMemo(() => (cfg ? parseBindings(cfg) : []), [cfg])
  const agents = useMemo(() => (cfg ? parseAgentIds(cfg) : []), [cfg])
  const channels = useMemo(
    () =>
      Array.from(
        new Set([...(cfg ? parseChannelNames(cfg) : []), ...discovered]),
      ).sort(),
    [cfg, discovered],
  )

  // Kept as a named refresh so the post-save call sites read unchanged.
  const loadData = async () => {
    await queryClient.invalidateQueries({ queryKey: ["app-config"] })
    await queryClient.invalidateQueries({ queryKey: ["secmsg-channels"] })
  }

  const isSlack = addChannel === "slack"

  const buildPeer = (): { kind: string; id: string } | undefined => {
    if (!isSlack || addPeerType === "none") return undefined
    if (addPeerType === "direct") return { kind: "direct", id: "" }
    return { kind: "channel", id: addPeerId.trim() }
  }

  const saveBindings = async (next: Binding[], label: string) => {
    setSaving(label)
    try {
      await patchAppConfig({
        bindings: next.map((b) => {
          // Preserve fields the UI does not model (default, deliver_to,
          // deliver_peer_kind, group_trigger, …) by spreading the original
          // object; match and agent_mentions are UI-owned, so rebuild them.
          const base = b.raw ? { ...b.raw } : {}
          delete base.match
          delete base.agent_mentions
          return {
            ...base,
            agent_id: b.agent_id,
            match: {
              channel: b.match.channel,
              ...(b.match.peer && b.match.peer.kind !== "none"
                ? {
                    peer: b.match.peer.id
                      ? b.match.peer
                      : { kind: b.match.peer.kind },
                  }
                : {}),
            },
            ...(b.agent_mentions !== undefined
              ? { agent_mentions: b.agent_mentions }
              : {}),
          }
        }),
      })
      toast.success("Saved")
      await loadData()
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to save")
    } finally {
      setSaving(null)
    }
  }

  const handleDelete = (index: number) => {
    void saveBindings(
      bindings.filter((_, i) => i !== index),
      `delete-${index}`,
    )
  }

  const startEditMentions = (index: number) => {
    setEditIdx(index)
    setEditMentions((bindings[index].agent_mentions ?? []).join(", "))
  }

  const cancelEditMentions = () => {
    setEditIdx(null)
    setEditMentions("")
  }

  const saveMentions = (index: number) => {
    const mentions = parseMentionsInput(editMentions)
    const next = bindings.map((b, i) =>
      i === index
        ? { ...b, agent_mentions: mentions.length > 0 ? mentions : undefined }
        : b,
    )
    void saveBindings(next, `mentions-${index}`).then(() => {
      setEditIdx(null)
      setEditMentions("")
    })
  }

  const handleAdd = () => {
    if (!addChannel.trim()) {
      toast.error("Channel is required")
      return
    }
    if (!addAgent.trim()) {
      toast.error("Agent is required")
      return
    }
    if (isSlack && addPeerType === "channel" && !addPeerId.trim()) {
      toast.error("Slack channel ID is required")
      return
    }
    const peer = buildPeer()
    const mentions = parseMentionsInput(addMentions)
    const next: Binding[] = [
      ...bindings,
      {
        agent_id: addAgent,
        match: { channel: addChannel, ...(peer ? { peer } : {}) },
        ...(mentions.length > 0 ? { agent_mentions: mentions } : {}),
      },
    ]
    void saveBindings(next, "add").then(() => {
      setShowAdd(false)
      setAddChannel("")
      setAddAgent("")
      setAddPeerType("none")
      setAddPeerId("")
      setAddMentions("")
    })
  }

  return (
    <div className="flex h-full flex-col">
      <PageHeader title={t("navigation.bindings")}>
        <Button
          size="sm"
          variant="outline"
          onClick={() => setShowAdd(true)}
          disabled={showAdd}
        >
          <IconPlus className="size-4" />
          Add Binding
        </Button>
      </PageHeader>

      <div className="min-h-0 flex-1 overflow-y-auto px-4 pb-8 sm:px-6">
        <div className="w-full max-w-250 space-y-3 pt-4">
          {loading && (
            <div className="flex items-center justify-center py-20">
              <IconLoader2 className="text-muted-foreground size-6 animate-spin" />
            </div>
          )}

          {fetchError && (
            <div className="text-destructive bg-destructive/10 rounded-lg px-4 py-3 text-sm">
              {fetchError}
            </div>
          )}

          {!loading && !fetchError && (
            <>
              {/* Bindings table */}
              <div className="border-border/60 bg-card overflow-hidden rounded-xl border">
                {bindings.length === 0 ? (
                  <p className="text-muted-foreground px-4 py-6 text-center text-sm">
                    No bindings configured — all messages route to the default
                    agent.
                  </p>
                ) : (
                  <table className="w-full text-sm">
                    <thead>
                      <tr className="border-border/40 border-b">
                        <th className="text-muted-foreground px-4 py-2.5 text-left text-xs font-medium">
                          Channel
                        </th>
                        <th className="text-muted-foreground px-4 py-2.5 text-left text-xs font-medium">
                          Peer
                        </th>
                        <th className="text-muted-foreground px-4 py-2.5 text-left text-xs font-medium">
                          Agent
                        </th>
                        <th className="text-muted-foreground px-4 py-2.5 text-left text-xs font-medium">
                          Agent Mentions
                        </th>
                        <th className="px-4 py-2.5">
                          <span className="sr-only">Actions</span>
                        </th>
                      </tr>
                    </thead>
                    <tbody>
                      {bindings.map((b, i) => (
                        <tr
                          key={i}
                          className="border-border/30 hover:bg-muted/20 border-b transition-colors last:border-0"
                        >
                          <td className="px-4 py-2.5 font-mono text-xs">
                            {b.match.channel}
                          </td>
                          <td className="text-muted-foreground px-4 py-2.5 font-mono text-xs">
                            {peerLabel(b.match.peer) || (
                              <span className="opacity-40">—</span>
                            )}
                          </td>
                          <td className="px-4 py-2.5 font-mono text-xs">
                            {b.agent_id}
                          </td>
                          <td className="px-4 py-2.5 text-xs">
                            {editIdx === i ? (
                              <div className="flex items-center gap-1.5">
                                <Input
                                  value={editMentions}
                                  onChange={(e) =>
                                    setEditMentions(e.target.value)
                                  }
                                  placeholder="e.g. amber, karen"
                                  className="h-7 font-mono text-xs"
                                  onKeyDown={(e) => {
                                    if (e.key === "Enter") saveMentions(i)
                                    if (e.key === "Escape") cancelEditMentions()
                                  }}
                                  // Deliberate: this input only exists because
                                  // the user just clicked to edit this row, so
                                  // focus belongs here. Removing it would make
                                  // every edit take an extra click.
                                  // oxlint-disable-next-line no-autofocus
                                  autoFocus
                                />
                                <Button
                                  size="icon-sm"
                                  variant="ghost"
                                  onClick={() => saveMentions(i)}
                                  disabled={saving === `mentions-${i}`}
                                  className="shrink-0"
                                >
                                  {saving === `mentions-${i}` ? (
                                    <IconLoader2 className="size-3.5 animate-spin" />
                                  ) : (
                                    <span className="text-xs font-medium">
                                      Save
                                    </span>
                                  )}
                                </Button>
                                <Button
                                  size="icon-sm"
                                  variant="ghost"
                                  onClick={cancelEditMentions}
                                  className="text-muted-foreground shrink-0"
                                >
                                  <IconX className="size-3.5" />
                                </Button>
                              </div>
                            ) : (
                              <div className="group/mentions flex items-center gap-1.5">
                                {b.agent_mentions &&
                                b.agent_mentions.length > 0 ? (
                                  <span className="text-muted-foreground font-mono">
                                    {b.agent_mentions.join(", ")}
                                  </span>
                                ) : (
                                  <span className="opacity-40">—</span>
                                )}
                                <button
                                  type="button"
                                  onClick={() => startEditMentions(i)}
                                  className="text-muted-foreground hover:text-foreground cursor-pointer bg-transparent opacity-0 transition-opacity group-hover/mentions:opacity-100"
                                >
                                  <IconEdit className="size-3.5" />
                                </button>
                              </div>
                            )}
                          </td>
                          <td className="px-4 py-2.5 text-right">
                            <Button
                              variant="ghost"
                              size="icon-sm"
                              onClick={() => handleDelete(i)}
                              disabled={saving === `delete-${i}`}
                              className="text-muted-foreground hover:text-destructive hover:bg-destructive/10"
                            >
                              {saving === `delete-${i}` ? (
                                <IconLoader2 className="size-3.5 animate-spin" />
                              ) : (
                                <IconTrash className="size-3.5" />
                              )}
                            </Button>
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                )}
              </div>

              {/* Add binding form */}
              {showAdd && (
                <div className="border-border/60 bg-card space-y-3 rounded-xl border p-4">
                  <span className="text-sm font-semibold">New Binding</span>

                  <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                    {/* Channel */}
                    <div className="space-y-1.5">
                      <p className="text-muted-foreground text-xs font-medium">
                        Channel
                      </p>
                      {channels.length > 0 ? (
                        <Select
                          value={addChannel || "__none__"}
                          onValueChange={(v) => {
                            setAddChannel(v === "__none__" ? "" : v)
                            setAddPeerType("none")
                            setAddPeerId("")
                          }}
                        >
                          <SelectTrigger className="w-full">
                            <SelectValue placeholder="Select channel" />
                          </SelectTrigger>
                          <SelectContent>
                            <SelectItem value="__none__">
                              Select channel
                            </SelectItem>
                            {[...channels]
                              .sort((a, b) => a.localeCompare(b))
                              .map((ch) => (
                                <SelectItem key={ch} value={ch}>
                                  {ch}
                                </SelectItem>
                              ))}
                          </SelectContent>
                        </Select>
                      ) : (
                        <Input
                          value={addChannel}
                          onChange={(e) => setAddChannel(e.target.value)}
                          placeholder="e.g. telegram-alice"
                        />
                      )}
                    </div>

                    {/* Agent */}
                    <div className="space-y-1.5">
                      <p className="text-muted-foreground text-xs font-medium">
                        Agent
                      </p>
                      {agents.length > 0 ? (
                        <Select
                          value={addAgent || "__none__"}
                          onValueChange={(v) =>
                            setAddAgent(v === "__none__" ? "" : v)
                          }
                        >
                          <SelectTrigger className="w-full">
                            <SelectValue placeholder="Select agent" />
                          </SelectTrigger>
                          <SelectContent>
                            <SelectItem value="__none__">
                              Select agent
                            </SelectItem>
                            {[...agents]
                              .sort((x, y) => x.localeCompare(y))
                              .map((a) => (
                                <SelectItem key={a} value={a}>
                                  {a}
                                </SelectItem>
                              ))}
                          </SelectContent>
                        </Select>
                      ) : (
                        <Input
                          value={addAgent}
                          onChange={(e) => setAddAgent(e.target.value)}
                          placeholder="Agent ID"
                        />
                      )}
                    </div>
                  </div>

                  {/* Slack peer — only shown when channel is slack */}
                  {isSlack && (
                    <div className="space-y-1.5">
                      <p className="text-muted-foreground text-xs font-medium">
                        Slack routing
                      </p>
                      <div className="flex flex-wrap gap-2">
                        {(
                          [
                            { value: "none", label: "All messages" },
                            { value: "channel", label: "Specific channel" },
                            { value: "direct", label: "Direct messages" },
                          ] as { value: SlackPeerType; label: string }[]
                        ).map(({ value, label }) => (
                          <button
                            key={value}
                            type="button"
                            onClick={() => {
                              setAddPeerType(value)
                              setAddPeerId("")
                            }}
                            className={[
                              "cursor-pointer rounded-md border px-2.5 py-1 text-xs font-medium transition-colors",
                              addPeerType === value
                                ? "border-primary/50 bg-secondary text-foreground"
                                : "border-border/50 text-muted-foreground hover:border-border hover:text-foreground bg-transparent",
                            ].join(" ")}
                          >
                            {label}
                          </button>
                        ))}
                      </div>
                      {addPeerType === "channel" && (
                        <Input
                          value={addPeerId}
                          onChange={(e) => setAddPeerId(e.target.value)}
                          placeholder="Slack channel ID (e.g. C0ANLEQP5GQ)"
                          className="mt-1.5 font-mono text-xs"
                        />
                      )}
                    </div>
                  )}

                  {/* Agent mentions */}
                  <div className="space-y-1.5">
                    <p className="text-muted-foreground text-xs font-medium">
                      Agent mentions{" "}
                      <span className="opacity-60">
                        (optional — comma-separated agent IDs that can be
                        @mentioned to reroute)
                      </span>
                    </p>
                    <Input
                      value={addMentions}
                      onChange={(e) => setAddMentions(e.target.value)}
                      placeholder="e.g. amber, karen"
                      className="font-mono text-xs"
                    />
                  </div>

                  <div className="flex justify-end gap-2">
                    <Button
                      variant="outline"
                      onClick={() => {
                        setShowAdd(false)
                        setAddChannel("")
                        setAddAgent("")
                        setAddPeerType("none")
                        setAddPeerId("")
                        setAddMentions("")
                      }}
                      disabled={saving === "add"}
                    >
                      Cancel
                    </Button>
                    <Button onClick={handleAdd} disabled={saving === "add"}>
                      {saving === "add" ? (
                        <>
                          <IconLoader2 className="size-4 animate-spin" />{" "}
                          Adding...
                        </>
                      ) : (
                        "Add"
                      )}
                    </Button>
                  </div>
                </div>
              )}
            </>
          )}
        </div>
      </div>
    </div>
  )
}
