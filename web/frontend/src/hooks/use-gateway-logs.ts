import { useQuery, useQueryClient } from "@tanstack/react-query"

import { getGatewayLogs } from "@/api/gateway"

// The logs view is fetched on mount and on explicit refresh only (no polling),
// so scrolling up to read history is never interrupted by a background update.
// staleTime: Infinity is what enforces that — without it Query would refetch on
// window focus and yank the view.
export function useGatewayLogs(lines: number) {
  const queryClient = useQueryClient()

  const { data, error, isFetching } = useQuery({
    queryKey: ["gateway-logs", lines],
    queryFn: async () => {
      const res = await getGatewayLogs(lines)
      return { logs: res.logs ?? [], error: res.error ?? "" }
    },
    staleTime: Infinity,
    refetchOnWindowFocus: false,
  })

  const refresh = async () => {
    await queryClient.invalidateQueries({ queryKey: ["gateway-logs", lines] })
  }

  return {
    logs: data?.logs ?? [],
    // A transport failure and a gateway-reported error land in the same field,
    // as they did before: the view shows one error line either way.
    error: error
      ? error instanceof Error
        ? error.message
        : "Failed to load logs"
      : (data?.error ?? ""),
    loading: isFetching,
    refresh,
  }
}
