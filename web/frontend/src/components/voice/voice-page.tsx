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
  type STTPreset,
  type STTProvider,
  getVoiceSTT,
  saveVoiceSTT,
} from "@/api/voice"

const PROVIDER_CHOICES = ["groq", "openai", "openrouter", "custom"]

function presetFor(presets: STTPreset[], provider: string): STTPreset | undefined {
  return presets.find((p) => p.provider === provider)
}

export function VoicePage() {
  const qc = useQueryClient()
  const stt = useQuery({ queryKey: ["voice-stt"], queryFn: getVoiceSTT })

  const [rows, setRows] = useState<STTProvider[]>([])
  useEffect(() => {
    if (stt.data) {
      setRows(stt.data.stt)
    }
  }, [stt.data])

  const presets = stt.data?.presets ?? []

  const saveMut = useMutation({
    mutationFn: () => saveVoiceSTT(rows),
    onSuccess: async () => {
      toast.success("Speech-to-text settings saved")
      await qc.invalidateQueries({ queryKey: ["voice-stt"] })
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const update = (i: number, patch: Partial<STTProvider>) =>
    setRows((prev) => prev.map((r, idx) => (idx === i ? { ...r, ...patch } : r)))

  const addRow = () =>
    setRows((prev) => [
      ...prev,
      { provider: "groq", enabled: prev.length === 0, api_key: "", base_url: "", model: "" },
    ])

  const removeRow = (i: number) =>
    setRows((prev) => prev.filter((_, idx) => idx !== i))

  // The first enabled backend with a key is the one actually used to transcribe.
  const activeIndex = rows.findIndex((r) => r.enabled)

  return (
    <div className="flex flex-col gap-6">
      <PageHeader title="Speech-to-Text" />

      <p className="text-muted-foreground px-6 text-sm">
        Transcribe inbound voice messages before they reach the assistant. The
        first enabled backend is used; the rest are kept for future fallback.
      </p>

      <Card>
        <CardHeader>
          <CardTitle>Transcription backends</CardTitle>
          <CardDescription>
            Any OpenAI-compatible Whisper endpoint (Groq, OpenAI, OpenRouter, or a
            custom host). Leave the endpoint and model blank to use the provider
            default.
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
              const isActive = i === activeIndex && row.enabled
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
                      />
                      <span className="text-sm font-medium">
                        {row.enabled ? "Enabled" : "Disabled"}
                      </span>
                      {isActive && (
                        <span className="rounded bg-green-500/15 px-2 py-0.5 text-xs text-green-600">
                          Active
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
                      <Label>Provider</Label>
                      <select
                        className="border-input bg-background h-9 rounded-md border px-3 text-sm"
                        value={row.provider}
                        onChange={(e) => update(i, { provider: e.target.value })}
                      >
                        {PROVIDER_CHOICES.map((p) => (
                          <option key={p} value={p}>
                            {p}
                          </option>
                        ))}
                      </select>
                    </div>
                    <div className="flex flex-col gap-1.5">
                      <Label>API key</Label>
                      <Input
                        type="password"
                        value={row.api_key ?? ""}
                        placeholder="required"
                        onChange={(e) => update(i, { api_key: e.target.value })}
                      />
                    </div>
                    <div className="flex flex-col gap-1.5">
                      <Label>Endpoint (base URL)</Label>
                      <Input
                        value={row.base_url ?? ""}
                        placeholder={preset?.base_url ?? "https://.../v1"}
                        onChange={(e) => update(i, { base_url: e.target.value })}
                      />
                    </div>
                    <div className="flex flex-col gap-1.5">
                      <Label>Model</Label>
                      <Input
                        value={row.model ?? ""}
                        placeholder={preset?.model ?? "whisper-1"}
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
            <Button
              onClick={() => saveMut.mutate()}
              disabled={saveMut.isPending}
            >
              {saveMut.isPending ? "Saving…" : "Save"}
            </Button>
          </div>
        </CardContent>
      </Card>
    </div>
  )
}
