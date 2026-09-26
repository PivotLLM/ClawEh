// Configuration report API. The PDF itself is opened straight from
// /api/report/pdf; this fetches the part the Report page shows inline.

export interface ReportIdentity {
  name: string
  /** Release plus build metadata, e.g. "0.6.0+27691883 [20260902155301]". */
  version: string
  /** Build timestamp; empty under an unstamped `go build`. */
  build: string
  /** "linux/amd64 on <hostname>". */
  platform: string
  /** RFC 3339. */
  generated_at: string
}

export interface AssessmentRow {
  /** True where the PDF marks the row "*": action is recommended. */
  action: boolean
  item: string
  status: string
}

export interface ReportAssessment {
  identity: ReportIdentity
  assessment: AssessmentRow[]
}

// getReportAssessment fetches the identity line and the security assessment
// table — the same rows the PDF renders, in the same order.
export async function getReportAssessment(): Promise<ReportAssessment> {
  const res = await fetch("/api/report/assessment")
  if (!res.ok) throw new Error(`Failed to fetch assessment: ${res.status}`)
  return res.json()
}
