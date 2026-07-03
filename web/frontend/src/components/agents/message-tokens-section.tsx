import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useState } from "react"
import { toast } from "sonner"

import {
  createMessageToken,
  deleteMessageToken,
  listMessageTokens,
  updateMessageToken,
  type MessageToken,
} from "@/api/message-tokens"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"

// copyToClipboard works in both secure and insecure contexts. navigator.clipboard
// is undefined when the WebUI is served over plain HTTP on a non-localhost host, so
// fall back to a hidden-textarea + execCommand("copy"). Returns whether it
// succeeded (so the UI doesn't claim success when nothing was copied).
async function copyToClipboard(text: string): Promise<boolean> {
  if (navigator.clipboard && window.isSecureContext) {
    try {
      await navigator.clipboard.writeText(text)
      return true
    } catch {
      // fall through to the legacy path
    }
  }
  try {
    const ta = document.createElement("textarea")
    ta.value = text
    ta.style.position = "fixed"
    ta.style.opacity = "0"
    document.body.appendChild(ta)
    ta.focus()
    ta.select()
    const ok = document.execCommand("copy")
    document.body.removeChild(ta)
    return ok
  } catch {
    return false
  }
}

function copy(text: string, label: string) {
  void copyToClipboard(text).then((ok) =>
    ok
      ? toast.success(`${label} copied`)
      : toast.error("Copy failed — select the text and copy manually"),
  )
}

// MessageTokensSection renders the per-agent long-lived message-API tokens: a list
// of named tokens (each copyable + revocable), an add control, and the endpoint
// URL an external app POSTs to. It is self-contained (its own react-query state)
// so it can be dropped into the agent settings card with just the agent id.
export function MessageTokensSection({ agentId }: { agentId: string }) {
  const qc = useQueryClient()
  const queryKey = ["message-tokens", agentId]
  const q = useQuery({ queryKey, queryFn: () => listMessageTokens(agentId) })
  const [name, setName] = useState("")

  const refresh = () => void qc.invalidateQueries({ queryKey })

  const createMut = useMutation({
    mutationFn: () => createMessageToken(agentId, name.trim()),
    onSuccess: () => {
      setName("")
      toast.success("Token created")
      refresh()
    },
    onError: (e: Error) => toast.error(e.message),
  })
  const deleteMut = useMutation({
    mutationFn: (id: string) => deleteMessageToken(agentId, id),
    onSuccess: () => {
      toast.success("Token revoked")
      refresh()
    },
    onError: (e: Error) => toast.error(e.message),
  })
  const updateMut = useMutation({
    mutationFn: (v: { id: string; ratePerMin: number; blockMinutes: number }) =>
      updateMessageToken(agentId, v.id, {
        ratePerMin: v.ratePerMin,
        blockMinutes: v.blockMinutes,
      }),
    onSuccess: () => {
      toast.success("Rate limit updated")
      refresh()
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const tokens = q.data?.tokens ?? []
  const base = q.data?.endpoint_base ?? ""
  const exampleURL = base + (tokens[0]?.token ?? "<token>")

  return (
    <div className="space-y-3">
      <div>
        <p className="text-foreground text-sm font-semibold">Integration Tokens</p>
        <p className="text-muted-foreground text-xs">
          Long-lived tokens that let an external app POST a message to this agent.
          Delivery behaves like a scheduled event — it goes to the agent's default
          channel, no conversation required.
        </p>
      </div>

      {tokens.length === 0 ? (
        <p className="text-muted-foreground text-xs">No tokens yet.</p>
      ) : (
        <ul className="divide-border/60 divide-y">
          {tokens.map((tk) => (
            <TokenRow
              key={tk.id}
              tk={tk}
              base={base}
              onRevoke={() => deleteMut.mutate(tk.id)}
              revoking={deleteMut.isPending}
              onSave={(ratePerMin, blockMinutes) =>
                updateMut.mutate({ id: tk.id, ratePerMin, blockMinutes })
              }
            />
          ))}
        </ul>
      )}

      <div className="flex items-center gap-2">
        <Input
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="Token name (e.g. gps-tracker)"
          className="h-7 flex-1 text-xs"
        />
        <Button
          size="sm"
          onClick={() => createMut.mutate()}
          disabled={createMut.isPending}
        >
          Add token
        </Button>
      </div>

      <div className="space-y-1">
        <p className="text-muted-foreground text-xs">Endpoint (POST the message body):</p>
        <code className="bg-muted block overflow-x-auto rounded px-2 py-1 text-xs">
          {exampleURL}
        </code>
      </div>
    </div>
  )
}

// quotaStatus renders a token's live rate-limit state: a block countdown when
// blocked, otherwise the current window usage against the limit.
function quotaStatus(tk: MessageToken): string {
  if (tk.blocked) {
    const mins = Math.ceil(tk.block_remaining_sec / 60)
    return `blocked · clears in ${mins}m`
  }
  return `${tk.hits_in_window}/${tk.rate_per_min} this minute`
}

// TokenRow renders one token with its config inputs (req/min, block min, saved
// on blur) and a live status line. Local input state lets the operator edit
// without every keystroke firing a PATCH; onSave commits on blur.
function TokenRow({
  tk,
  base,
  onRevoke,
  revoking,
  onSave,
}: {
  tk: MessageToken
  base: string
  onRevoke: () => void
  revoking: boolean
  onSave: (ratePerMin: number, blockMinutes: number) => void
}) {
  const [rate, setRate] = useState(String(tk.rate_per_min))
  const [block, setBlock] = useState(String(tk.block_minutes))

  // Commit only when a value actually changed, so a blur without an edit is a
  // no-op. A blank/invalid field resolves to 0 = "use the default".
  const save = () => {
    const r = Number.parseInt(rate, 10)
    const b = Number.parseInt(block, 10)
    const ratePerMin = Number.isFinite(r) && r > 0 ? r : 0
    const blockMinutes = Number.isFinite(b) && b > 0 ? b : 0
    if (ratePerMin !== tk.rate_per_min || blockMinutes !== tk.block_minutes) {
      onSave(ratePerMin, blockMinutes)
    }
  }

  return (
    <li className="flex items-start justify-between gap-2 py-2">
      <div className="min-w-0 flex-1">
        <div className="text-sm font-medium">{tk.name || "(unnamed)"}</div>
        <div className="text-muted-foreground flex items-center gap-2 text-xs">
          <code className="bg-muted truncate rounded px-1.5 py-0.5">
            {tk.token}
          </code>
          <span className="shrink-0">
            {new Date(tk.created_at_ms).toLocaleString()}
          </span>
        </div>
        <div className="mt-1 flex items-center gap-2 text-xs">
          <label className="text-muted-foreground flex items-center gap-1">
            <Input
              type="number"
              min={0}
              value={rate}
              onChange={(e) => setRate(e.target.value)}
              onBlur={save}
              className="h-6 w-14 text-xs"
            />
            req/min
          </label>
          <label className="text-muted-foreground flex items-center gap-1">
            <Input
              type="number"
              min={0}
              value={block}
              onChange={(e) => setBlock(e.target.value)}
              onBlur={save}
              className="h-6 w-14 text-xs"
            />
            block min
          </label>
          <span className={tk.blocked ? "text-destructive" : "text-muted-foreground"}>
            {quotaStatus(tk)}
          </span>
        </div>
      </div>
      <div className="flex shrink-0 gap-1.5">
        <Button
          size="sm"
          variant="outline"
          onClick={() => copy(base + tk.token, "Endpoint URL")}
        >
          Copy URL
        </Button>
        <Button size="sm" variant="outline" onClick={() => copy(tk.token, "Token")}>
          Copy
        </Button>
        <Button
          size="sm"
          variant="outline"
          onClick={onRevoke}
          disabled={revoking}
        >
          Revoke
        </Button>
      </div>
    </li>
  )
}
