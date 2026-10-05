import type { ErrorComponentProps } from "@tanstack/react-router"
import { useState } from "react"
import { useTranslation } from "react-i18next"

import { Button } from "@/components/ui/button"

// RouteError replaces TanStack Router's built-in error boundary. That one
// prints the message in red on the page background, which in the dark theme is
// red on black; this keeps the message in the page's own foreground colour and
// leaves it selectable so it can be copied into a bug report.
export function RouteError({ error, reset }: ErrorComponentProps) {
  const { t } = useTranslation()
  const [show, setShow] = useState(false)
  const message = error instanceof Error ? error.message : String(error)

  return (
    <div className="space-y-3 p-6" role="alert" data-testid="route-error">
      <div className="flex flex-wrap items-center gap-3">
        <p className="text-foreground text-sm font-medium">
          {t("errors.boundary.title")}
        </p>
        <Button variant="outline" size="sm" onClick={() => setShow((v) => !v)}>
          {show ? t("errors.boundary.hide") : t("errors.boundary.show")}
        </Button>
        <Button variant="outline" size="sm" onClick={reset}>
          {t("errors.boundary.retry")}
        </Button>
      </div>
      {show && (
        <pre className="bg-muted text-foreground overflow-auto rounded-md p-3 font-mono text-xs break-words whitespace-pre-wrap select-text">
          {message}
        </pre>
      )}
    </div>
  )
}
