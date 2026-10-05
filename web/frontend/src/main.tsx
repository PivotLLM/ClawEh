import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { RouterProvider, createRouter } from "@tanstack/react-router"
import { StrictMode } from "react"
import ReactDOM from "react-dom/client"

import "./i18n"
import "./index.css"
import { installAuthRedirect } from "./lib/auth-redirect"
import { routeTree } from "./routeTree.gen"
import {
  isGatewayReachable,
  isNetworkError,
  onGatewayReachable,
} from "./store/connection"

// Every API module fetches on its own; one wrapper on the global fetch sends
// the browser to the login page when the session is gone (401) and tracks
// whether the gateway can be reached at all.
installAuthRedirect()

// While the gateway is unreachable a query keeps retrying every few seconds
// instead of failing: the page keeps its last data or its loading state and the
// shell shows the connection banner. Every other error keeps the default of
// three retries.
const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: (failureCount, error) =>
        (isNetworkError(error) && !isGatewayReachable()) || failureCount < 3,
      retryDelay: (attempt, error) =>
        Math.min(isNetworkError(error) ? 5000 : 30000, 1000 * 2 ** attempt),
    },
  },
})

// The gateway is back: fetch everything on screen again at once.
onGatewayReachable(() => {
  void queryClient.invalidateQueries()
})

const router = createRouter({
  routeTree,
  context: {
    queryClient,
  },
})

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router
  }
}

const rootElement = document.getElementById("root")!
if (!rootElement.innerHTML) {
  const root = ReactDOM.createRoot(rootElement)
  root.render(
    <StrictMode>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </StrictMode>,
  )
}
