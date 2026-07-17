import { useCallback, useEffect, useState } from "react"

import { getGatewayLogs } from "@/api/gateway"

// The logs view is fetched on mount and on explicit refresh only (no polling),
// so scrolling up to read history is never interrupted by a background update.
export function useGatewayLogs(lines: number) {
  const [logs, setLogs] = useState<string[]>([])
  const [error, setError] = useState("")
  const [loading, setLoading] = useState(false)

  const refresh = useCallback(async () => {
    setLoading(true)
    try {
      const data = await getGatewayLogs(lines)
      setLogs(data.logs ?? [])
      setError(data.error ?? "")
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed to load logs")
    } finally {
      setLoading(false)
    }
  }, [lines])

  // Fetch on mount and whenever the requested line count changes.
  useEffect(() => {
    void refresh()
  }, [refresh])

  return { logs, error, loading, refresh }
}
