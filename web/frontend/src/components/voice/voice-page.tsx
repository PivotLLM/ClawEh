import { useQuery } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { useEffect, useRef, useState } from "react"
import { toast } from "sonner"

import {
  type STTProvider,
  type STTProviderOption,
  getVoiceSTT,
  saveVoiceSTT,
} from "@/api/voice"
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

type SaveStatus = "saving" | "saved" | "error" | null

function optionFor(
  providers: STTProviderOption[],
  name: string,
): STTProviderOption | undefined {
  return providers.find((p) => p.name === name)
}

// NO_KEY_REASON is the one label the user can act on from here: the key is
// entered on the Providers page, so the row links there.
const NO_KEY_REASON = "Not used: provider has no API key."

// notUsedReason is the label an enabled row carries when the backend would be
// skipped, or "" when it is usable: the provider must be configured (and still
// an HTTP provider), hold an API key, and have a model (its own or a default).
function notUsedReason(
  providers: STTProviderOption[],
  row: STTProvider,
): string {
  const option = optionFor(providers, row.provider)
  if (!option) return "Not used: provider not configured."
  if (!option.ready) return NO_KEY_REASON
  if (!row.model.trim() && !option.default_model) {
    return "Not used: needs a model."
  }
  return ""
}

export function VoicePage() {
  const stt = useQuery({ queryKey: ["voice-stt"], queryFn: getVoiceSTT })

  // Editable copy of the fetched rows, reseeded whenever a fresh fetch lands.
  // Adjusted during render rather than in an effect so a refetch never paints
  // the previous rows for a frame before correcting itself.
  //
  // The initial rows come from the query too: when the page is revisited the
  // cache already holds the answer on the first render, so the "new data"
  // comparison below never fires. Seeding rows from [] in that case showed an
  // empty list — and the next edit saved that empty list over the real
  // configuration.
  const [rows, setRows] = useState<STTProvider[]>(() => stt.data?.stt ?? [])
  const [syncedData, setSyncedData] = useState(stt.data)
  if (stt.data && stt.data !== syncedData) {
    setSyncedData(stt.data)
    setRows(stt.data.stt)
  }

  const providers = stt.data?.providers ?? []

  const [status, setStatus] = useState<SaveStatus>(null)
  // Refs let the debounced save read the latest rows; synced in an effect (not
  // during render) to satisfy react-hooks/refs.
  const rowsRef = useRef<STTProvider[]>(rows)
  useEffect(() => {
    rowsRef.current = rows
  }, [rows])
  const saveTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const savedTimer = useRef<ReturnType<typeof setTimeout> | undefined>(
    undefined,
  )

  useEffect(
    () => () => {
      clearTimeout(saveTimer.current)
      clearTimeout(savedTimer.current)
    },
    [],
  )

  // doSave persists the whole backend list (a full-replace PUT). It does NOT
  // refetch afterwards, so a model being typed is not replaced mid-edit.
  const doSave = async () => {
    setStatus("saving")
    try {
      await saveVoiceSTT(rowsRef.current)
      setStatus("saved")
      clearTimeout(savedTimer.current)
      savedTimer.current = setTimeout(() => setStatus(null), 2000)
    } catch (e) {
      setStatus("error")
      toast.error(e instanceof Error ? e.message : "Save failed")
    }
  }
  const doSaveRef = useRef(doSave)
  useEffect(() => {
    doSaveRef.current = doSave
  })

  const scheduleSave = () => {
    clearTimeout(saveTimer.current)
    saveTimer.current = setTimeout(() => void doSaveRef.current(), 600)
  }

  const update = (i: number, patch: Partial<STTProvider>) => {
    setRows((prev) =>
      prev.map((r, idx) => (idx === i ? { ...r, ...patch } : r)),
    )
    scheduleSave()
  }

  const addRow = () => {
    setRows((prev) => [
      ...prev,
      {
        provider: providers[0]?.name ?? "",
        enabled: prev.length === 0,
        model: "",
      },
    ])
    scheduleSave()
  }

  const removeRow = (i: number) => {
    setRows((prev) => prev.filter((_, idx) => idx !== i))
    scheduleSave()
  }

  // Enabled backends are tried in list order: the first is the primary, the
  // rest are its fallbacks.
  const primaryIndex = rows.findIndex(
    (r) => r.enabled && !notUsedReason(providers, r),
  )

  return (
    <div className="flex flex-col gap-6">
      <PageHeader title="Speech">
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
      </PageHeader>

      <p className="text-muted-foreground px-6 text-sm">
        Voice messages are transcribed before they reach the assistant. Enabled
        backends are tried in order.
      </p>

      <Card>
        <CardHeader>
          <CardTitle>Transcription backends</CardTitle>
          <CardDescription>
            The API key and endpoint come from the Providers page; the model may
            be left blank for a known service.
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-4">
          {rows.length === 0 ? (
            <p className="text-muted-foreground text-sm">
              No transcription backends configured. Voice messages are passed to
              the assistant untranscribed.
            </p>
          ) : (
            rows.map((row, i) => {
              const option = optionFor(providers, row.provider)
              const reason = notUsedReason(providers, row)
              return (
                <div
                  key={i}
                  className="flex flex-col gap-3 rounded-md border p-4"
                >
                  <div className="flex items-center justify-between gap-3">
                    <div className="flex items-center gap-2">
                      <Switch
                        checked={row.enabled}
                        onCheckedChange={(v) => update(i, { enabled: v })}
                        aria-label={`${row.provider} enabled`}
                      />
                      <span className="text-sm font-medium">
                        {row.enabled ? "Enabled" : "Disabled"}
                      </span>
                      {row.enabled && !reason && (
                        <span className="rounded bg-green-500/15 px-2 py-0.5 text-xs text-green-600">
                          {i === primaryIndex ? "Primary" : "Fallback"}
                        </span>
                      )}
                      {row.enabled && reason && (
                        <span className="text-xs text-amber-500">
                          {reason}
                          {reason === NO_KEY_REASON && (
                            <>
                              {" "}
                              <Link to="/providers" className="underline">
                                Add it on the Providers page
                              </Link>
                              .
                            </>
                          )}
                        </span>
                      )}
                    </div>
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => removeRow(i)}
                    >
                      Remove
                    </Button>
                  </div>

                  <div className="grid gap-3 sm:grid-cols-2">
                    <div className="flex flex-col gap-1.5">
                      <Label htmlFor={`stt-provider-${i}`}>Provider</Label>
                      <select
                        id={`stt-provider-${i}`}
                        className="border-input bg-background h-9 rounded-md border px-3 text-sm"
                        value={row.provider}
                        onChange={(e) =>
                          update(i, { provider: e.target.value })
                        }
                      >
                        {/* A provider that is no longer listed stays
                            selectable so the row is not silently rewritten. */}
                        {!option && (
                          <option value={row.provider}>{row.provider}</option>
                        )}
                        {providers.map((p) => (
                          <option key={p.name} value={p.name}>
                            {p.name}
                          </option>
                        ))}
                      </select>
                    </div>
                    <div className="flex flex-col gap-1.5">
                      <Label htmlFor={`stt-model-${i}`}>Model</Label>
                      <Input
                        id={`stt-model-${i}`}
                        value={row.model}
                        placeholder={option?.default_model || "Required"}
                        onChange={(e) => update(i, { model: e.target.value })}
                      />
                    </div>
                  </div>
                </div>
              )
            })
          )}

          <div className="flex items-center gap-3">
            <Button
              variant="outline"
              onClick={addRow}
              disabled={providers.length === 0}
            >
              Add backend
            </Button>
            {stt.data && providers.length === 0 && (
              <span className="text-muted-foreground text-sm">
                Add a provider on the{" "}
                <Link to="/providers" className="underline">
                  Providers
                </Link>{" "}
                page first.
              </span>
            )}
          </div>
        </CardContent>
      </Card>
    </div>
  )
}
