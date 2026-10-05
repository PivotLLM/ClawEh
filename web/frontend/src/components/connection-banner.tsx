import { useAtomValue } from "jotai"
import { useTranslation } from "react-i18next"

import { gatewayReachableAtom } from "@/store/connection"

// ConnectionBanner is the one line the app shell shows while the gateway is
// unreachable (a restart, a stopped service). It hides on its own when the
// next request gets an answer; nothing else about the page changes.
export function ConnectionBanner() {
  const { t } = useTranslation()
  const reachable = useAtomValue(gatewayReachableAtom)
  if (reachable) return null

  return (
    <div
      data-testid="connection-banner"
      className="shrink-0 border-b border-amber-500/40 bg-amber-500/10 px-4 py-1.5 text-center text-sm text-amber-700 dark:text-amber-300"
    >
      {t("connection.lost")}
    </div>
  )
}
