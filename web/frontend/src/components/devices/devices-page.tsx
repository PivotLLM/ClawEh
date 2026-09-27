import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

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
} from "@/api/devices"
import { PageHeader } from "@/components/page-header"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Label } from "@/components/ui/label"

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
  const { t } = useTranslation()
  const qc = useQueryClient()
  const status = useQuery({
    queryKey: ["device-status"],
    queryFn: getDeviceStatus,
  })
  const pending = useQuery({
    queryKey: ["device-pending"],
    queryFn: listPendingDevices,
    refetchInterval: 3000,
  })
  const paired = useQuery({
    queryKey: ["device-paired"],
    queryFn: listPairedDevices,
  })

  const [qr, setQr] = useState<DeviceStatus | null>(null)

  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ["device-status"] })
    void qc.invalidateQueries({ queryKey: ["device-paired"] })
    void qc.invalidateQueries({ queryKey: ["device-pending"] })
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
      <PageHeader title="Devices" />
      <div className="space-y-6 overflow-y-auto px-6 pb-8">
        {/* Pair. The listener itself (scope, port, external URL, allowlist,
            auto-approve) is configured on the Network page with the other
            listeners; this page is pairing and the paired devices. */}
        <Card>
          <CardHeader>
            <CardTitle>Pair a device</CardTitle>
            <CardDescription>
              Generate a QR code, then scan it with your device. The first
              connection appears below for approval.
              {s && (
                <>
                  {" "}
                  {t("pages.devices.connect_to")}{" "}
                  <code
                    className="text-foreground"
                    data-testid="devices-connect-url"
                  >
                    {s.connect_url}
                  </code>
                  . The device gateway is listening on{" "}
                  <code className="text-foreground">
                    {s.listen_host}:{s.listen_port}
                  </code>{" "}
                  ({s.listen_lan ? "local network" : "loopback only"}); change
                  that on the{" "}
                  <Link to="/network" className="underline">
                    Network page
                  </Link>
                  .
                </>
              )}
            </CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            {s?.warnings?.length ? (
              <ul className="text-sm text-amber-600 dark:text-amber-400">
                {s.warnings.map((w) => (
                  <li key={w}>⚠ {w}</li>
                ))}
              </ul>
            ) : null}
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
                  Type this into the app's Profile Token field. It authenticates
                  the same as the QR; the device still needs your approval
                  below.
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
                          : toast.error(
                              "Copy failed — select the text and copy manually",
                            ),
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
              <p className="text-muted-foreground text-sm">
                No pending devices.
              </p>
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
                        {p.remote_ip ? (
                          <span
                            className="text-muted-foreground font-normal"
                            data-testid="pending-remote-ip"
                          >
                            {" "}
                            · from {p.remote_ip}
                          </span>
                        ) : null}
                      </div>
                      <div className="text-muted-foreground truncate text-xs">
                        {p.platform} · role {p.role} ·{" "}
                        {p.device_id.slice(0, 12)}…
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
            <CardDescription>
              Approved devices. Remove to revoke access.
            </CardDescription>
          </CardHeader>
          <CardContent>
            {pairedList.length === 0 ? (
              <p className="text-muted-foreground text-sm">
                No paired devices.
              </p>
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
                            assignAgentMut.mutate({
                              id: d.device_id,
                              agentId: e.target.value,
                            })
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
