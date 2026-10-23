import { useQuery } from "@tanstack/react-query"
import { useEffect, useRef, useState } from "react"
import { toast } from "sonner"

import {
  type STTPreset,
  type STTProvider,
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

const PROVIDER_CHOICES = ["groq", "openai", "openrouter", "custom"]

type SaveStatus = "saving" | "saved" | "error" | null

function presetFor(
  presets: STTPreset[],
  provider: string,
): STTPreset | undefined {
  return presets.find((p) => p.provider === provider)
}

// missingEndpoint: a provider with no preset (custom) has no default endpoint
// or model, so the backend is skipped until both are set.
function missingEndpoint(presets: STTPreset[], row: STTProvider): boolean {
  return (
    !presetFor(presets, row.provider) &&
    (!row.base_url?.trim() || !row.model?.trim())
  )
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

  const presets = stt.data?.presets ?? []

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
  // refetch afterwards, so an api_key being typed is not blanked mid-edit.
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
        provider: "groq",
        enabled: prev.length === 0,
        api_key: "",
        base_url: "",
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
    (r) => r.enabled && !missingEndpoint(presets, r),
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
            Leave the endpoint and model blank to use the provider&apos;s
            defaults.
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
              const preset = presetFor(presets, row.provider)
              const unusable = missingEndpoint(presets, row)
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
                      {row.enabled && !unusable && (
                        <span className="rounded bg-green-500/15 px-2 py-0.5 text-xs text-green-600">
                          {i === primaryIndex ? "Primary" : "Fallback"}
                        </span>
                      )}
                      {row.enabled && unusable && (
                        <span className="text-xs text-amber-500">
                          Not used: needs an endpoint and model.
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
                        {PROVIDER_CHOICES.map((p) => (
                          <option key={p} value={p}>
                            {p}
                          </option>
                        ))}
                      </select>
                    </div>
                    <div className="flex flex-col gap-1.5">
                      <Label htmlFor={`stt-api-key-${i}`}>API key</Label>
                      <Input
                        id={`stt-api-key-${i}`}
                        type="password"
                        value={row.api_key ?? ""}
                        placeholder="Blank uses the key from Providers"
                        onChange={(e) => update(i, { api_key: e.target.value })}
                      />
                    </div>
                    <div className="flex flex-col gap-1.5">
                      <Label htmlFor={`stt-base-url-${i}`}>
                        Endpoint (base URL)
                      </Label>
                      <Input
                        id={`stt-base-url-${i}`}
                        value={row.base_url ?? ""}
                        placeholder={preset?.base_url ?? "Required"}
                        onChange={(e) =>
                          update(i, { base_url: e.target.value })
                        }
                      />
                    </div>
                    <div className="flex flex-col gap-1.5">
                      <Label htmlFor={`stt-model-${i}`}>Model</Label>
                      <Input
                        id={`stt-model-${i}`}
                        value={row.model ?? ""}
                        placeholder={preset?.model ?? "Required"}
                        onChange={(e) => update(i, { model: e.target.value })}
                      />
                    </div>
                  </div>
                </div>
              )
            })
          )}

          <div className="flex items-center gap-3">
            <Button variant="outline" onClick={addRow}>
              Add backend
            </Button>
          </div>
        </CardContent>
      </Card>
    </div>
  )
}
