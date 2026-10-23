import { useQuery } from "@tanstack/react-query"
import { useTranslation } from "react-i18next"

import {
  HUMAN_AGENTS_QUERY_KEY,
  type HumanAgentProblem,
  getHumanAgents,
} from "@/api/channels"

// noteKey is a note's stable identity: what it is and what it says.
export function noteKey(n: HumanAgentProblem): string {
  return `${n.kind}|${n.agent ?? ""}|${n.message}`
}

// uniqueNotes drops repeated notes (the same note from two sites), keeping
// the first.
export function uniqueNotes(notes: HumanAgentProblem[]): HumanAgentProblem[] {
  const seen = new Set<string>()
  return notes.filter((n) => {
    const k = noteKey(n)
    if (seen.has(k)) return false
    seen.add(k)
    return true
  })
}

// pageLabel names the WebUI page a note's link opens, as the sidebar names it.
export function pageLabel(link: string, t: (key: string) => string): string {
  switch (link) {
    case "/channels":
      return t("navigation.channels_group")
  }
  return link
}

// HumanSettingNotes shows, on the page that sets it, each global setting that
// names a model representing a person and is therefore ignored (a person's
// model can't be a default, summarization or vision model).
export function HumanSettingNotes({ page }: { page: string }) {
  const { t } = useTranslation()
  const { data } = useQuery({
    queryKey: HUMAN_AGENTS_QUERY_KEY,
    queryFn: getHumanAgents,
  })
  const notes = uniqueNotes(
    (data?.problems ?? []).filter(
      (p) => p.kind === "setting" && p.page === page,
    ),
  )
  if (notes.length === 0) return null
  return (
    <div className="space-y-1">
      {notes.map((n) => (
        <p
          key={noteKey(n)}
          data-testid="human-setting-note"
          className="text-xs text-amber-600 dark:text-amber-400"
        >
          {t("agents.humanIgnored", { message: n.message })}
        </p>
      ))}
    </div>
  )
}
