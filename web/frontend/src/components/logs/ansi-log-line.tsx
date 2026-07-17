import { Fragment, useMemo } from "react"

import { parseAnsiSegments } from "@/lib/ansi-log"

type AnsiLogLineProps = {
  line: string
}

export function AnsiLogLine({ line }: AnsiLogLineProps) {
  const segments = useMemo(() => parseAnsiSegments(line), [line])

  // whitespace-pre-wrap preserves the log's own spacing and wraps at the
  // container edge; overflow-wrap:anywhere breaks long unbreakable tokens
  // (UUIDs) only when they would overflow — no newlines are injected into the
  // text, so lines stay intact for selection/copy.
  return (
    <div className="whitespace-pre-wrap [overflow-wrap:anywhere]">
      {segments.map((segment, index) => (
        <Fragment key={`${index}-${segment.text.length}`}>
          <span style={segment.style}>{segment.text}</span>
        </Fragment>
      ))}
    </div>
  )
}
