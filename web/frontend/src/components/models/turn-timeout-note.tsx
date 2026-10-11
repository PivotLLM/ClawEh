import { Link } from "@tanstack/react-router"
import type { TFunction } from "i18next"
import { useTranslation } from "react-i18next"

// formatDuration renders whole seconds as "15 minutes", "1 hour 30 minutes",
// "45 seconds": hours, minutes and seconds, leaving out the zero parts.
export function formatDuration(seconds: number, t: TFunction): string {
  const hours = Math.floor(seconds / 3600)
  const minutes = Math.floor((seconds % 3600) / 60)
  const secs = seconds % 60
  const parts: string[] = []
  if (hours > 0) parts.push(t("models.duration.hours", { count: hours }))
  if (minutes > 0) parts.push(t("models.duration.minutes", { count: minutes }))
  if (secs > 0 || parts.length === 0) {
    parts.push(t("models.duration.seconds", { count: secs }))
  }
  return parts.join(" ")
}

interface TurnTimeoutNoteProps {
  // The Request timeout field as typed, in seconds; blank means the default.
  requestTimeout: string
  // The install's effective Turn timeout in seconds; 0 when unknown.
  turnTimeout: number
}

// TurnTimeoutNote says, at a model's Request timeout field, that a turn ends
// before that timeout can, and links to the Turn timeout that decides.
export function TurnTimeoutNote({
  requestTimeout,
  turnTimeout,
}: TurnTimeoutNoteProps) {
  const { t } = useTranslation()
  const request = Number(requestTimeout)
  if (turnTimeout <= 0 || !Number.isFinite(request) || request <= turnTimeout) {
    return null
  }
  return (
    <p
      className="text-xs text-amber-700 dark:text-amber-300"
      data-testid="model-turn-timeout-note"
    >
      {t("models.turnTimeoutNote.prefix", {
        duration: formatDuration(turnTimeout, t),
      })}{" "}
      <Link to="/system" hash="turn-timeout" className="underline">
        {t("models.turnTimeoutNote.link")}
      </Link>
      {t("models.turnTimeoutNote.suffix")}
    </p>
  )
}
