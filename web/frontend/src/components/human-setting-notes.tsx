import { useQuery } from "@tanstack/react-query"

import { type HumanAgentProblem, getHumanAgents } from "@/api/channels"

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

// HumanSettingNotes shows, on the page that sets it, each global setting that
// names a model representing a person and is therefore ignored (a person's
// model can't be a default, summarization or vision model).
export function HumanSettingNotes({ page }: { page: string }) {
  const { data } = useQuery({
    queryKey: ["agents-human-problems"],
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
          Ignored: {n.message}
        </p>
      ))}
    </div>
  )
}
