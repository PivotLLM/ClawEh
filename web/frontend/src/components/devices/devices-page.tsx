import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useEffect, useState } from "react"
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
  generateDevicePairing,
  getDeviceStatus,
  listPairedDevices,
  listPendingDevices,
  rejectDevice,
  removeDevice,
  saveDeviceSettings,
} from "@/api/devices"

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
  const [extUrl, setExtUrl] = useState("")
  const [qr, setQr] = useState<DeviceStatus | null>(null)

  useEffect(() => {
    if (status.data) {
      setLan(status.data.listen_lan)
      setExtUrl(status.data.external_url)
    }
  }, [status.data])

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
  const saveMut = useMutation({
    mutationFn: () => saveDeviceSettings({ listen_lan: lan, external_url: extUrl }),
    onSuccess: () => {
      toast.success("Network settings saved")
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

  const s = status.data
  const pendingList = pending.data?.pending ?? []
  const pairedList = paired.data?.devices ?? []

  return (
    <>
      <PageHeader title="Devices" />
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
              <Switch id="lan-switch" checked={lan} onCheckedChange={setLan} />
            </div>
            <div className="space-y-1">
              <Label htmlFor="ext-url">External URL</Label>
              <Input
                id="ext-url"
                value={extUrl}
                placeholder={s ? `http://${s.ips[0] ?? "<ip>"}:${s.listen_port}` : ""}
                onChange={(e) => setExtUrl(e.target.value)}
              />
              <p className="text-muted-foreground text-sm">
                What devices are told to connect to. Leave blank to auto-detect the
                LAN address. Set to e.g. <code>https://claw.example.com</code> when
                using a reverse proxy / Cloudflare.
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
              <Button onClick={() => saveMut.mutate()} disabled={saveMut.isPending}>
                Save network settings
              </Button>
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
              Generate a QR code, then scan it with your device (e.g. the Rabbit R1).
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
                    <Button
                      size="sm"
                      variant="outline"
                      onClick={() => removeMut.mutate(d.device_id)}
                      disabled={removeMut.isPending}
                    >
                      Remove
                    </Button>
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
