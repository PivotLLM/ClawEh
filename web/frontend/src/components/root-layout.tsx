import { Outlet } from "@tanstack/react-router"
import { TanStackRouterDevtools } from "@tanstack/react-router-devtools"
import { useEffect } from "react"

import { AppLayout } from "@/components/app-layout"
import { initializeChatStore } from "@/lib/claw-chat-controller"

export const RootLayout = () => {
  useEffect(() => {
    initializeChatStore()
  }, [])

  return (
    <AppLayout>
      <Outlet />
      <TanStackRouterDevtools />
    </AppLayout>
  )
}
