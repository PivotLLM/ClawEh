import { useQuery } from "@tanstack/react-query"

import { getHumanAgents } from "@/api/channels"

// HumanSettingNotes shows, on the page that sets it, each global setting that
// names a model representing a person and is therefore ignored (a person's
// model can't be a default, summarization or vision model).
export function HumanSettingNotes({ page }: { page: string }) {
  const { data } = useQuery({
    queryKey: ["agents-human-problems"],
    queryFn: getHumanAgents,
  })
  const notes = (data?.problems ?? []).filter(
    (p) => p.kind === "setting" && p.page === page,
  )
  if (notes.length === 0) return null
  return (
    <div className="space-y-1">
      {notes.map((n) => (
        <p
          key={n.message}
          data-testid="human-setting-note"
          className="text-xs text-amber-600 dark:text-amber-400"
        >
          Ignored: {n.message}
        </p>
      ))}
    </div>
  )
}
