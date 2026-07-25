import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useEffect, useRef, useState } from "react"
import { toast } from "sonner"

import { PageHeader } from "@/components/page-header"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import {
  type DeviceStatus,
  approveDevice,
  assignDeviceAgent,
  generateDevicePairing,
  getDeviceStatus,
  listPairedDevices,
  listPendingDevices,
  regenerateWordToken,
  rejectDevice,
  removeDevice,
  saveDeviceSettings,
} from "@/api/devices"

// copyToClipboard works in both secure and insecure contexts. navigator.clipboard
// is undefined when the WebUI is served over plain HTTP on a non-localhost host, so
// fall back to a hidden-textarea + execCommand("copy"). Returns whether it succeeded
// (so the UI doesn't claim success when nothing was copied).
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

export function DevicesPage() {
  const qc = useQueryClient()
  const status = useQuery({ queryKey: ["device-status"], queryFn: getDeviceStatus })
  const pending = useQuery({
    queryKey: ["device-pending"],
    queryFn: listPendingDevices,
    refetchInterval: 3000,
  })
  const paired = useQuery({ queryKey: ["device-paired"], queryFn: listPairedDevices })

  const [lan, setLan] = useState(false)
  const [extHost, setExtHost] = useState("")
  const [extPort, setExtPort] = useState("")
  const [extTls, setExtTls] = useState(false)
  const [qr, setQr] = useState<DeviceStatus | null>(null)

  // Auto-save state for the Network card. Refs mirror the fields so the debounced
  // save reads current values; timers drive the "Saving… / Saved ✓" hint. Refs are
  // synced in an effect (not during render) to satisfy react-hooks/refs.
  type SaveStatus = "saving" | "saved" | "error" | null
  const [netStatus, setNetStatus] = useState<SaveStatus>(null)
  const lanRef = useRef(lan)
  const extHostRef = useRef(extHost)
  const extPortRef = useRef(extPort)
  const extTlsRef = useRef(extTls)
  const saveTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const savedTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)

  useEffect(() => {
    lanRef.current = lan
    extHostRef.current = extHost
    extPortRef.current = extPort
    extTlsRef.current = extTls
  }, [lan, extHost, extPort, extTls])

  useEffect(
    () => () => {
      clearTimeout(saveTimer.current)
      clearTimeout(savedTimer.current)
    },
    [],
  )

  useEffect(() => {
    if (!status.data) return
    setLan(status.data.listen_lan)
    const url = status.data.external_url
    if (!url) {
      setExtHost("")
      setExtPort("")
      setExtTls(false)
      return
    }
    try {
      const u = new URL(url)
      setExtHost(u.hostname)
      setExtPort(u.port)
      setExtTls(u.protocol === "https:" || u.protocol === "wss:")
    } catch {
      setExtHost(url)
      setExtPort("")
      setExtTls(false)
    }
  }, [status.data])

  // Compose the stored external_url from the current host/port/TLS values (read via
  // refs so the debounced save sees the latest). Empty host means "direct LAN"
  // (auto-detect), so external_url is cleared.
  const buildExternalURL = () => {
    const host = extHostRef.current.trim()
    if (host === "") return ""
    const scheme = extTlsRef.current ? "https" : "http"
    const port = extPortRef.current.trim()
    return port ? `${scheme}://${host}:${port}` : `${scheme}://${host}`
  }

  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ["device-status"] })
    void qc.invalidateQueries({ queryKey: ["device-paired"] })
    void qc.invalidateQueries({ queryKey: ["device-pending"] })
  }

  // doSave persists the Network card. It does NOT refetch device-status, so a host
  // being typed is not reverted mid-edit; the "currently listening" line refreshes
  // on the next navigation.
  const doSave = async () => {
    setNetStatus("saving")
    try {
      await saveDeviceSettings({
        listen_lan: lanRef.current,
        external_url: buildExternalURL(),
      })
      setNetStatus("saved")
      clearTimeout(savedTimer.current)
      savedTimer.current = setTimeout(() => setNetStatus(null), 2000)
    } catch (e) {
      setNetStatus("error")
      toast.error(e instanceof Error ? e.message : "Save failed")
    }
  }
  const doSaveRef = useRef(doSave)
  useEffect(() => {
    doSaveRef.current = doSave
  })

  const scheduleNetSave = () => {
    clearTimeout(saveTimer.current)
    saveTimer.current = setTimeout(() => void doSaveRef.current(), 600)
  }

  const genMut = useMutation({
    mutationFn: generateDevicePairing,
    onSuccess: (d) => {
      setQr(d)
      toast.success("Pairing QR generated")
      refresh()
    },
    onError: (e: Error) => toast.error(e.message),
  })
  const approveMut = useMutation({
    mutationFn: approveDevice,
    onSuccess: () => {
      toast.success("Device approved")
      refresh()
    },
    onError: (e: Error) => toast.error(e.message),
  })
  const rejectMut = useMutation({
    mutationFn: rejectDevice,
    onSuccess: () => {
      toast.success("Pairing rejected")
      refresh()
    },
    onError: (e: Error) => toast.error(e.message),
  })
  const removeMut = useMutation({
    mutationFn: removeDevice,
    onSuccess: () => {
      toast.success("Device removed")
      refresh()
    },
    onError: (e: Error) => toast.error(e.message),
  })
  const regenWordMut = useMutation({
    mutationFn: regenerateWordToken,
    onSuccess: () => {
      toast.success("New profile token generated")
      refresh()
    },
    onError: (e: Error) => toast.error(e.message),
  })
  const assignAgentMut = useMutation({
    mutationFn: ({ id, agentId }: { id: string; agentId: string }) =>
      assignDeviceAgent(id, agentId),
    onSuccess: () => {
      toast.success("Assistant updated")
      refresh()
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const s = status.data
  const pendingList = pending.data?.pending ?? []
  const pairedList = paired.data?.devices ?? []
  const agentOptions = paired.data?.agents ?? []

  return (
    <>
      <PageHeader title="Devices">
        {netStatus && (
          <span
            className={`text-xs ${netStatus === "error" ? "text-destructive" : netStatus === "saved" ? "text-emerald-500" : "text-muted-foreground"}`}
          >
            {netStatus === "saving"
              ? "Saving…"
              : netStatus === "saved"
                ? "Saved ✓"
                : "Save failed"}
          </span>
        )}
      </PageHeader>
      <div className="space-y-6 overflow-y-auto px-6 pb-8">
        {/* Network */}
        <Card>
          <CardHeader>
            <CardTitle>Network</CardTitle>
            <CardDescription>
              The device gateway listens on its own port, separate from the WebUI.
              {s && (
                <>
                  {" "}
                  Currently listening on{" "}
                  <code className="text-foreground">
                    {s.listen_host}:{s.listen_port}
                  </code>{" "}
                  ({s.listen_lan ? "local network" : "loopback only"}).
                </>
              )}
            </CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="flex items-center justify-between gap-4">
              <div>
                <Label htmlFor="lan-switch">Listen for local network connections</Label>
                <p className="text-muted-foreground text-sm">
                  Off = loopback only (127.0.0.1). On = reachable from your LAN
                  (0.0.0.0).
                </p>
              </div>
              <Switch
                id="lan-switch"
                checked={lan}
                onCheckedChange={(v) => {
                  setLan(v)
                  scheduleNetSave()
                }}
              />
            </div>
            <div className="space-y-2">
              <Label>External address (reverse proxy / tunnel)</Label>
              <div className="flex gap-2">
                <Input
                  className="flex-1"
                  placeholder="host or IP (blank = direct LAN)"
                  value={extHost}
                  onChange={(e) => {
                    setExtHost(e.target.value)
                    scheduleNetSave()
                  }}
                />
                <Input
                  className="w-28"
                  placeholder="port"
                  inputMode="numeric"
                  value={extPort}
                  onChange={(e) => {
                    setExtPort(e.target.value)
                    scheduleNetSave()
                  }}
                />
              </div>
              <div className="flex items-center gap-2">
                <Switch
                  id="tls-switch"
                  checked={extTls}
                  onCheckedChange={(v) => {
                    setExtTls(v)
                    scheduleNetSave()
                  }}
                />
                <Label htmlFor="tls-switch">Use TLS (secure wss connection)</Label>
              </div>
              <p className="text-muted-foreground text-sm">
                What devices are told to connect to. Leave host blank for direct LAN
                access (auto-detected). Set these when a reverse proxy or tunnel fronts
                the gateway — with TLS on, the host must match the proxy's certificate.
              </p>
            </div>
            {s?.warnings?.length ? (
              <ul className="text-sm text-amber-600 dark:text-amber-400">
                {s.warnings.map((w) => (
                  <li key={w}>⚠ {w}</li>
                ))}
              </ul>
            ) : null}
            <div className="flex items-center gap-3">
              <a href="/config" className="text-muted-foreground text-sm underline">
                Advanced config
              </a>
            </div>
          </CardContent>
        </Card>

        {/* Pair */}
        <Card>
          <CardHeader>
            <CardTitle>Pair a device</CardTitle>
            <CardDescription>
              Generate a QR code, then scan it with your device.
              The first connection appears below for approval.
            </CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <Button onClick={() => genMut.mutate()} disabled={genMut.isPending}>
              {qr ? "Regenerate pairing QR" : "Generate pairing QR"}
            </Button>
            {qr?.qr_png && (
              <div className="space-y-2">
                <img
                  src={qr.qr_png}
                  alt="Device pairing QR code"
                  className="border-border h-64 w-64 rounded border bg-white p-2"
                />
                <p className="text-muted-foreground text-sm">
                  Connect URL:{" "}
                  <code>
                    {qr.protocol}://{qr.ips[0]}:{qr.port}
                  </code>
                </p>
                {qr.qr_ascii && (
                  <details className="text-sm">
                    <summary className="cursor-pointer">Show payload</summary>
                    <pre className="bg-muted overflow-x-auto rounded p-2 text-xs">
                      {qr.payload}
                    </pre>
                  </details>
                )}
              </div>
            )}
            {s?.word_token && (
              <div className="border-border space-y-2 rounded border p-3">
                <Label>Profile Token (for apps that can't scan the QR)</Label>
                <p className="text-muted-foreground text-sm">
                  Type this into the app's Profile Token field. It authenticates the same as
                  the QR; the device still needs your approval below.
                </p>
                <div className="flex items-center gap-2">
                  <code className="bg-muted flex-1 rounded px-2 py-1 text-sm break-all">
                    {s.word_token}
                  </code>
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => {
                      void copyToClipboard(s.word_token).then((ok) =>
                        ok
                          ? toast.success("Profile token copied")
                          : toast.error("Copy failed — select the text and copy manually"),
                      )
                    }}
                  >
                    Copy
                  </Button>
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => regenWordMut.mutate()}
                    disabled={regenWordMut.isPending}
                  >
                    Regenerate
                  </Button>
                </div>
              </div>
            )}
          </CardContent>
        </Card>

        {/* Pending */}
        <Card>
          <CardHeader>
            <CardTitle>Pending approvals</CardTitle>
            <CardDescription>
              Devices waiting for you to approve their pairing.
            </CardDescription>
          </CardHeader>
          <CardContent>
            {pendingList.length === 0 ? (
              <p className="text-muted-foreground text-sm">No pending devices.</p>
            ) : (
              <ul className="divide-border divide-y">
                {pendingList.map((p) => (
                  <li
                    key={p.request_id}
                    className="flex items-center justify-between gap-4 py-3"
                  >
                    <div className="min-w-0">
                      <div className="font-medium">
                        {p.display_name || p.client_id || "Unknown device"}
                      </div>
                      <div className="text-muted-foreground truncate text-xs">
                        {p.platform} · role {p.role} · {p.device_id.slice(0, 12)}…
                      </div>
                    </div>
                    <div className="flex shrink-0 gap-2">
                      <Button
                        size="sm"
                        onClick={() => approveMut.mutate(p.request_id)}
                        disabled={approveMut.isPending}
                      >
                        Approve
                      </Button>
                      <Button
                        size="sm"
                        variant="outline"
                        onClick={() => rejectMut.mutate(p.request_id)}
                        disabled={rejectMut.isPending}
                      >
                        Reject
                      </Button>
                    </div>
                  </li>
                ))}
              </ul>
            )}
          </CardContent>
        </Card>

        {/* Paired */}
        <Card>
          <CardHeader>
            <CardTitle>Paired devices</CardTitle>
            <CardDescription>Approved devices. Remove to revoke access.</CardDescription>
          </CardHeader>
          <CardContent>
            {pairedList.length === 0 ? (
              <p className="text-muted-foreground text-sm">No paired devices.</p>
            ) : (
              <ul className="divide-border divide-y">
                {pairedList.map((d) => (
                  <li
                    key={d.device_id}
                    className="flex items-center justify-between gap-4 py-3"
                  >
                    <div className="min-w-0">
                      <div className="font-medium">
                        {d.display_name || "Device"}
                      </div>
                      <div className="text-muted-foreground truncate text-xs">
                        {d.platform} · roles {d.roles.join(", ") || "—"} ·{" "}
                        {d.device_id.slice(0, 12)}…
                      </div>
                    </div>
                    <div className="flex items-center gap-2">
                      {d.client_mode === "node" ? (
                        <select
                          className="border-border bg-background rounded border px-2 py-1 text-sm"
                          aria-label="Assistant"
                          value={d.agent_id}
                          disabled={assignAgentMut.isPending}
                          onChange={(e) =>
                            assignAgentMut.mutate({ id: d.device_id, agentId: e.target.value })
                          }
                        >
                          <option value="">Default assistant</option>
                          {agentOptions.map((a) => (
                            <option key={a.id} value={a.id}>
                              {a.name}
                            </option>
                          ))}
                        </select>
                      ) : (
                        <span className="text-muted-foreground text-xs">
                          assistant chosen in app
                        </span>
                      )}
                      <Button
                        size="sm"
                        variant="outline"
                        onClick={() => removeMut.mutate(d.device_id)}
                        disabled={removeMut.isPending}
                      >
                        Remove
                      </Button>
                    </div>
                  </li>
                ))}
              </ul>
            )}
          </CardContent>
        </Card>
      </div>
    </>
  )
}
