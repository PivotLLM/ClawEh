import { Outlet, useRouterState } from "@tanstack/react-router"
import { TanStackRouterDevtools } from "@tanstack/react-router-devtools"
import { useEffect } from "react"

import { AppLayout } from "@/components/app-layout"
import { LOGIN_PATH } from "@/lib/auth-redirect"
import { initializeChatStore } from "@/lib/claw-chat-controller"

export const RootLayout = () => {
  // The chat socket needs a session: opening it from the login page would
  // just fail the handshake and start the reconnect loop. initializeChatStore
  // is idempotent, so re-running it on navigation is harmless.
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  useEffect(() => {
    if (pathname !== LOGIN_PATH) initializeChatStore()
  }, [pathname])

  return (
    <AppLayout>
      <Outlet />
      <TanStackRouterDevtools />
    </AppLayout>
  )
}
