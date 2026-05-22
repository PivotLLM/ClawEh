// Session summary panel — displays structured or prose context summary for a session.

import { useTranslation } from "react-i18next"

import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"

// Summary schema mirrors pkg/llmcontext.Summary (JSON tags).
interface SummaryState {
  goals?: string
  progress?: string
  pending?: string
  constraints?: string
}

interface KeyMoment {
  seq: number
  role: string
  summary: string
  exact?: string
}

interface IndexEntry {
  seq_start: number
  seq_end: number
  role: string
  label: string
}

interface StructuredSummary {
  version: number
  state: SummaryState
  key_moments?: KeyMoment[]
  message_index?: IndexEntry[]
  covered_seq_start: number
  covered_seq_end: number
  generated_at?: string
  model?: string
}

function tryParseStructured(raw: string): StructuredSummary | null {
  const trimmed = raw.trim()
  if (!trimmed.startsWith("{")) return null
  try {
    const parsed = JSON.parse(trimmed) as StructuredSummary
    if (parsed.version !== 1 || !parsed.state) return null
    return parsed
  } catch {
    return null
  }
}

interface SessionSummaryPanelProps {
  summary: string
}

export function SessionSummaryPanel({ summary }: SessionSummaryPanelProps) {
  const { t } = useTranslation()

  if (!summary || !summary.trim()) {
    return null
  }

  const structured = tryParseStructured(summary)

  if (!structured) {
    return (
      <Card size="sm">
        <CardHeader className="border-border border-b">
          <CardTitle>{t("chat.summaryPanel.title", "Context Summary")}</CardTitle>
        </CardHeader>
        <CardContent className="pt-3">
          <p className="text-muted-foreground text-sm whitespace-pre-wrap">{summary}</p>
        </CardContent>
      </Card>
    )
  }

  const { state, key_moments, message_index, generated_at, model } = structured

  return (
    <Card size="sm">
      <CardHeader className="border-border border-b">
        <CardTitle>{t("chat.summaryPanel.title", "Context Summary")}</CardTitle>
        {(generated_at || model) && (
          <p className="text-muted-foreground text-xs mt-1">
            {model && <span>{model}</span>}
            {model && generated_at && <span> · </span>}
            {generated_at && (
              <span>{new Date(generated_at).toLocaleString()}</span>
            )}
          </p>
        )}
      </CardHeader>
      <CardContent className="pt-3 space-y-3">
        {/* State */}
        <div className="space-y-2">
          {state.goals && (
            <div>
              <span className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
                {t("chat.summaryPanel.goals", "Goals")}
              </span>
              <p className="text-sm mt-0.5">{state.goals}</p>
            </div>
          )}
          {state.progress && (
            <div>
              <span className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
                {t("chat.summaryPanel.progress", "Progress")}
              </span>
              <p className="text-sm mt-0.5">{state.progress}</p>
            </div>
          )}
          {state.pending && (
            <div>
              <span className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
                {t("chat.summaryPanel.pending", "Pending")}
              </span>
              <p className="text-sm mt-0.5">{state.pending}</p>
            </div>
          )}
          {state.constraints && (
            <div>
              <span className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
                {t("chat.summaryPanel.constraints", "Constraints")}
              </span>
              <p className="text-sm mt-0.5">{state.constraints}</p>
            </div>
          )}
        </div>

        {/* Key Moments */}
        {key_moments && key_moments.length > 0 && (
          <div>
            <span className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
              {t("chat.summaryPanel.keyMoments", "Key Moments")}
            </span>
            <ul className="mt-1 space-y-1">
              {key_moments.map((km) => (
                <li key={km.seq} className="text-sm flex gap-2">
                  <span className="text-muted-foreground shrink-0">#{km.seq}</span>
                  <span>
                    <span className="font-medium">{km.role}:</span>{" "}
                    {km.exact ? (
                      <code className="text-xs bg-muted px-1 py-0.5 rounded">
                        {km.exact}
                      </code>
                    ) : (
                      km.summary
                    )}
                  </span>
                </li>
              ))}
            </ul>
          </div>
        )}

        {/* Message Index */}
        {message_index && message_index.length > 0 && (
          <div>
            <span className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
              {t("chat.summaryPanel.messageIndex", "Retrievable History")}
            </span>
            <ul className="mt-1 space-y-0.5">
              {message_index.map((e, i) => (
                <li key={i} className="text-sm flex gap-2">
                  <span className="text-muted-foreground shrink-0">
                    {e.seq_start === e.seq_end
                      ? `#${e.seq_start}`
                      : `#${e.seq_start}–#${e.seq_end}`}
                  </span>
                  <span>
                    <span className="font-medium">{e.role}:</span> {e.label}
                  </span>
                </li>
              ))}
            </ul>
          </div>
        )}
      </CardContent>
    </Card>
  )
}
