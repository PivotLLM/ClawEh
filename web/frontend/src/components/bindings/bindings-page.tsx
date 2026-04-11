import { IconEdit, IconLoader2, IconPlus, IconTrash, IconX } from "@tabler/icons-react"
import { useCallback, useEffect, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import { getAppConfig, patchAppConfig } from "@/api/channels"
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
}

// ── config parsers ──────────────────────────────────────────────────────────

function asRecord(v: unknown): Record<string, unknown> {
  if (v && typeof v === "object" && !Array.isArray(v)) return v as Record<string, unknown>
  return {}
}
function asArray(v: unknown): unknown[] { return Array.isArray(v) ? v : [] }
function asString(v: unknown): string { return typeof v === "string" ? v : "" }

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
    }
  })
}

function parseChannelNames(cfg: unknown): string[] {
  const channels = asRecord(asRecord(cfg).channels)
  const names: string[] = []

  const telegramSeen = new Set<string>()
  for (const bot of asArray(channels.telegram)) {
    const b = asRecord(bot)
    if (b.enabled !== false) {
      const id = asString(b.id)
      const channelName = (!id || id === "default") ? "telegram" : `telegram-${id}`
      if (!telegramSeen.has(channelName)) {
        telegramSeen.add(channelName)
        names.push(channelName)
      }
    }
  }

  for (const name of ["webui", "slack", "discord", "matrix", "irc", "line", "whatsapp"]) {
    if (asRecord(channels[name]).enabled === true) names.push(name)
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
  return s.split(/[\s,]+/).map((x) => x.trim()).filter(Boolean)
}

// ── peer type for the add form ──────────────────────────────────────────────

type SlackPeerType = "none" | "channel" | "direct"

// ── component ───────────────────────────────────────────────────────────────

export function BindingsPage() {
  const { t } = useTranslation()
  const [loading, setLoading] = useState(true)
  const [fetchError, setFetchError] = useState("")
  const [bindings, setBindings] = useState<Binding[]>([])
  const [channels, setChannels] = useState<string[]>([])
  const [agents, setAgents] = useState<string[]>([])
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

  const loadData = useCallback(async () => {
    setLoading(true)
    try {
      const cfg = await getAppConfig()
      setBindings(parseBindings(cfg))
      setChannels(parseChannelNames(cfg))
      setAgents(parseAgentIds(cfg))
      setFetchError("")
    } catch (e) {
      setFetchError(e instanceof Error ? e.message : "Failed to load")
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { void loadData() }, [loadData])

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
        bindings: next.map((b) => ({
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
          ...(b.agent_mentions !== undefined ? { agent_mentions: b.agent_mentions } : {}),
        })),
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
    void saveBindings(bindings.filter((_, i) => i !== index), `delete-${index}`)
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
        : b
    )
    void saveBindings(next, `mentions-${index}`).then(() => {
      setEditIdx(null)
      setEditMentions("")
    })
  }

  const handleAdd = () => {
    if (!addChannel.trim()) { toast.error("Channel is required"); return }
    if (!addAgent.trim()) { toast.error("Agent is required"); return }
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
        <div className="mx-auto w-full max-w-250 pt-4 space-y-3">
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
              <div className="border-border/60 bg-card rounded-xl border overflow-hidden">
                {bindings.length === 0 ? (
                  <p className="text-muted-foreground px-4 py-6 text-sm text-center">
                    No bindings configured — all messages route to the default agent.
                  </p>
                ) : (
                  <table className="w-full text-sm">
                    <thead>
                      <tr className="border-border/40 border-b">
                        <th className="text-muted-foreground px-4 py-2.5 text-left text-xs font-medium">Channel</th>
                        <th className="text-muted-foreground px-4 py-2.5 text-left text-xs font-medium">Peer</th>
                        <th className="text-muted-foreground px-4 py-2.5 text-left text-xs font-medium">Agent</th>
                        <th className="text-muted-foreground px-4 py-2.5 text-left text-xs font-medium">Agent Mentions</th>
                        <th className="px-4 py-2.5" />
                      </tr>
                    </thead>
                    <tbody>
                      {bindings.map((b, i) => (
                        <tr
                          key={i}
                          className="border-border/30 hover:bg-muted/20 border-b last:border-0 transition-colors"
                        >
                          <td className="px-4 py-2.5 font-mono text-xs">{b.match.channel}</td>
                          <td className="px-4 py-2.5 text-muted-foreground font-mono text-xs">
                            {peerLabel(b.match.peer) || <span className="opacity-40">—</span>}
                          </td>
                          <td className="px-4 py-2.5 font-mono text-xs">{b.agent_id}</td>
                          <td className="px-4 py-2.5 text-xs">
                            {editIdx === i ? (
                              <div className="flex items-center gap-1.5">
                                <Input
                                  value={editMentions}
                                  onChange={(e) => setEditMentions(e.target.value)}
                                  placeholder="e.g. amber, karen"
                                  className="h-7 font-mono text-xs"
                                  onKeyDown={(e) => {
                                    if (e.key === "Enter") saveMentions(i)
                                    if (e.key === "Escape") cancelEditMentions()
                                  }}
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
                                    <span className="text-xs font-medium">Save</span>
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
                              <div className="flex items-center gap-1.5 group/mentions">
                                {b.agent_mentions && b.agent_mentions.length > 0 ? (
                                  <span className="text-muted-foreground font-mono">
                                    {b.agent_mentions.join(", ")}
                                  </span>
                                ) : (
                                  <span className="opacity-40">—</span>
                                )}
                                <button
                                  type="button"
                                  onClick={() => startEditMentions(i)}
                                  className="text-muted-foreground hover:text-foreground opacity-0 group-hover/mentions:opacity-100 transition-opacity cursor-pointer bg-transparent"
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
                <div className="border-border/60 bg-card rounded-xl border p-4 space-y-3">
                  <span className="text-sm font-semibold">New Binding</span>

                  <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                    {/* Channel */}
                    <div className="space-y-1.5">
                      <p className="text-muted-foreground text-xs font-medium">Channel</p>
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
                            <SelectItem value="__none__">Select channel</SelectItem>
                            {channels.map((ch) => (
                              <SelectItem key={ch} value={ch}>{ch}</SelectItem>
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
                      <p className="text-muted-foreground text-xs font-medium">Agent</p>
                      {agents.length > 0 ? (
                        <Select
                          value={addAgent || "__none__"}
                          onValueChange={(v) => setAddAgent(v === "__none__" ? "" : v)}
                        >
                          <SelectTrigger className="w-full">
                            <SelectValue placeholder="Select agent" />
                          </SelectTrigger>
                          <SelectContent>
                            <SelectItem value="__none__">Select agent</SelectItem>
                            {agents.map((a) => (
                              <SelectItem key={a} value={a}>{a}</SelectItem>
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
                      <p className="text-muted-foreground text-xs font-medium">Slack routing</p>
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
                            onClick={() => { setAddPeerType(value); setAddPeerId("") }}
                            className={[
                              "rounded-md border px-2.5 py-1 text-xs font-medium transition-colors cursor-pointer",
                              addPeerType === value
                                ? "border-primary/50 bg-secondary text-foreground"
                                : "border-border/50 bg-transparent text-muted-foreground hover:border-border hover:text-foreground",
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
                          className="font-mono text-xs mt-1.5"
                        />
                      )}
                    </div>
                  )}

                  {/* Agent mentions */}
                  <div className="space-y-1.5">
                    <p className="text-muted-foreground text-xs font-medium">
                      Agent mentions <span className="opacity-60">(optional — comma-separated agent IDs that can be @mentioned to reroute)</span>
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
                        <><IconLoader2 className="size-4 animate-spin" /> Adding...</>
                      ) : "Add"}
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
