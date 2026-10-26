import { IconAlertTriangle } from "@tabler/icons-react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import {
  type DeviceStatus,
  type PairedDevice,
  approveDevice,
  assignDeviceAgent,
  generateDevicePairing,
  getDeviceStatus,
  listPairedDevices,
  listPendingDevices,
  regenerateWordToken,
  rejectDevice,
  removeDevice,
  setDeviceUser,
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
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"

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

// DeviceUserField edits the name the assistant is told a device's messages come
// from. Saved on blur or Enter; an empty field shows what will be used instead.
function DeviceUserField({
  device,
  onSave,
}: {
  device: PairedDevice
  onSave: (userName: string) => Promise<unknown>
}) {
  const { t } = useTranslation()
  const [draft, setDraft] = useState<string | null>(null)
  const id = `device-user-${device.device_id}`
  const commit = () => {
    if (draft === null) return
    const name = draft.trim()
    if (name === device.user_name) {
      setDraft(null)
      return
    }
    void onSave(name).finally(() => setDraft(null))
  }
  return (
    <div className="flex flex-col gap-1">
      <div className="flex items-center gap-2">
        <Label htmlFor={id}>{t("pages.devices.user_label")}</Label>
        <Input
          id={id}
          data-testid="device-user"
          className="h-8 w-40"
          value={draft ?? device.user_name}
          placeholder={device.user_default || device.display_name}
          aria-describedby={`${id}-hint`}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault()
              commit()
            }
          }}
          onBlur={commit}
        />
      </div>
      <p id={`${id}-hint`} className="text-muted-foreground text-xs">
        {t("pages.devices.user_hint")}
      </p>
    </div>
  )
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

  const setUserMut = useMutation({
    mutationFn: ({ id, userName }: { id: string; userName: string }) =>
      setDeviceUser(id, userName),
    onSuccess: () => {
      toast.success("User updated")
      refresh()
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const s = status.data
  const pendingList = pending.data?.pending ?? []
  const pairedList = paired.data?.devices ?? []
  const agentOptions = paired.data?.agents ?? []
  // Why an assignment is ignored, as the server reports it.
  const staleAgentNote = (d: PairedDevice) => {
    const agent = d.agent_name || d.agent_id
    switch (d.agent_state) {
      case "disabled":
        return t("pages.devices.agent_disabled", { agent })
      case "set_aside":
        return t("pages.devices.agent_set_aside", { agent })
      default:
        return t("pages.devices.agent_missing", { agent })
    }
  }

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
              Generate a QR code and scan it with your device, then approve it
              below.
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
                  .
                </>
              )}
            </CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            {/* Access is a label; a problem is an icon with short hover
                text, and the link goes where it is fixed. */}
            {s && (
              <div
                className="flex items-center gap-2 text-sm"
                data-testid="devices-access"
              >
                <span className="text-muted-foreground">Access:</span>
                <span>{s.listen_lan ? "network" : "localhost"}</span>
                {s.warnings?.length ? (
                  <Tooltip>
                    <TooltipTrigger asChild>
                      <span
                        className="text-amber-600 dark:text-amber-400"
                        data-testid="devices-warning"
                      >
                        <IconAlertTriangle className="size-4" aria-hidden />
                        <span className="sr-only">{s.warnings.join(" ")}</span>
                      </span>
                    </TooltipTrigger>
                    <TooltipContent>{s.warnings.join(" ")}</TooltipContent>
                  </Tooltip>
                ) : null}
                <Link to="/network" className="underline">
                  Change
                </Link>
              </div>
            )}
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
                      {d.agent_missing && (
                        <p
                          data-testid="device-agent-missing"
                          className="text-xs text-amber-600 dark:text-amber-400"
                        >
                          {staleAgentNote(d)}
                        </p>
                      )}
                    </div>
                    <div className="flex items-center gap-2">
                      <DeviceUserField
                        device={d}
                        onSave={(userName) =>
                          setUserMut
                            .mutateAsync({ id: d.device_id, userName })
                            .catch(() => undefined)
                        }
                      />
                      {d.client_mode === "node" ? (
                        <select
                          className="border-border bg-background rounded border px-2 py-1 text-sm"
                          aria-label="Assistant"
                          value={d.agent_ref ?? d.agent_id}
                          disabled={assignAgentMut.isPending}
                          onChange={(e) =>
                            assignAgentMut.mutate({
                              id: d.device_id,
                              agentId: e.target.value,
                            })
                          }
                        >
                          <option value="">
                            {t("pages.devices.default_assistant")}
                          </option>
                          {d.agent_state === "not_found" && (
                            <option value={d.agent_ref ?? d.agent_id}>
                              {d.agent_id}
                            </option>
                          )}
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
