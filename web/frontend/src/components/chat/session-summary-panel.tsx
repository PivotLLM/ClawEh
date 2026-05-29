// Session summary panel — displays structured or prose context summary for a session.
import { useTranslation } from "react-i18next"

import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"

// Summary schema mirrors pkg/llmcontext.Summary (JSON tags).
interface SeqRange {
  seq_start: number
  seq_end?: number
}

interface SummaryItem {
  text?: string
  refs?: SeqRange[]
  exact?: string
}

interface SummaryState {
  goals?: SummaryItem[]
  progress?: SummaryItem[]
  pending?: SummaryItem[]
  constraints?: SummaryItem[]
}

interface KeyMoment {
  seq?: number
  refs?: SeqRange[]
  role?: string
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
    if (parsed.version !== 2 || !parsed.state) return null
    return parsed
  } catch {
    return null
  }
}

function formatRefs(refs?: SeqRange[], fallbackSeq?: number): string {
  if ((!refs || refs.length === 0) && fallbackSeq) return `#${fallbackSeq}`
  if (!refs || refs.length === 0) return ""
  return refs
    .map((ref) => {
      const end = ref.seq_end ?? ref.seq_start
      return end === ref.seq_start
        ? `#${ref.seq_start}`
        : `#${ref.seq_start}–#${end}`
    })
    .join(", ")
}

function renderStateItems(title: string, items: SummaryItem[] | undefined) {
  if (!items || items.length === 0) return null
  return (
    <div>
      <span className="text-muted-foreground text-xs font-semibold tracking-wide uppercase">
        {title}
      </span>
      <ul className="mt-1 space-y-1">
        {items.map((item, i) => {
          const refs = formatRefs(item.refs)
          return (
            <li key={i} className="flex gap-2 text-sm">
              {refs && (
                <span className="text-muted-foreground shrink-0">{refs}</span>
              )}
              <span>
                {item.exact ? (
                  <code className="bg-muted rounded px-1 py-0.5 text-xs">
                    {item.exact}
                  </code>
                ) : (
                  item.text
                )}
              </span>
            </li>
          )
        })}
      </ul>
    </div>
  )
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
          <CardTitle>
            {t("chat.summaryPanel.title", "Context Summary")}
          </CardTitle>
        </CardHeader>
        <CardContent className="pt-3">
          <p className="text-muted-foreground text-sm whitespace-pre-wrap">
            {summary}
          </p>
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
          <p className="text-muted-foreground mt-1 text-xs">
            {model && <span>{model}</span>}
            {model && generated_at && <span> · </span>}
            {generated_at && (
              <span>{new Date(generated_at).toLocaleString()}</span>
            )}
          </p>
        )}
      </CardHeader>
      <CardContent className="space-y-3 pt-3">
        {/* State */}
        <div className="space-y-2">
          {renderStateItems(t("chat.summaryPanel.goals", "Goals"), state.goals)}
          {renderStateItems(
            t("chat.summaryPanel.progress", "Progress"),
            state.progress,
          )}
          {renderStateItems(
            t("chat.summaryPanel.pending", "Pending"),
            state.pending,
          )}
          {renderStateItems(
            t("chat.summaryPanel.constraints", "Constraints"),
            state.constraints,
          )}
        </div>

        {/* Key Moments */}
        {key_moments && key_moments.length > 0 && (
          <div>
            <span className="text-muted-foreground text-xs font-semibold tracking-wide uppercase">
              {t("chat.summaryPanel.keyMoments", "Key Moments")}
            </span>
            <ul className="mt-1 space-y-1">
              {key_moments.map((km, i) => {
                const refs = formatRefs(km.refs, km.seq)
                return (
                  <li key={`${refs}-${i}`} className="flex gap-2 text-sm">
                    {refs && (
                      <span className="text-muted-foreground shrink-0">
                        {refs}
                      </span>
                    )}
                    <span>
                      {km.role && (
                        <span className="font-medium">{km.role}: </span>
                      )}
                      {km.exact ? (
                        <code className="bg-muted rounded px-1 py-0.5 text-xs">
                          {km.exact}
                        </code>
                      ) : (
                        km.summary
                      )}
                    </span>
                  </li>
                )
              })}
            </ul>
          </div>
        )}

        {/* Message Index */}
        {message_index && message_index.length > 0 && (
          <div>
            <span className="text-muted-foreground text-xs font-semibold tracking-wide uppercase">
              {t("chat.summaryPanel.messageIndex", "Retrievable History")}
            </span>
            <ul className="mt-1 space-y-0.5">
              {message_index.map((e, i) => (
                <li key={i} className="flex gap-2 text-sm">
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
