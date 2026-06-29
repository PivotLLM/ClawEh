import { useRouterState } from "@tanstack/react-router"
import type { ReactNode } from "react"
import { Toaster } from "sonner"

import { AppHeader } from "@/components/app-header"
import { AppSidebar } from "@/components/app-sidebar"
import { SidebarProvider } from "@/components/ui/sidebar"
import { TooltipProvider } from "@/components/ui/tooltip"

export function AppLayout({ children }: { children: ReactNode }) {
  // The setup wizard is a focused, full-screen onboarding flow — it renders
  // without the sidebar/header chrome so first-run users aren't distracted by
  // navigation into a not-yet-configured app.
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  if (pathname === "/setup") {
    return (
      <TooltipProvider>
        <div className="bg-background h-dvh overflow-y-auto">{children}</div>
        <Toaster position="bottom-center" />
      </TooltipProvider>
    )
  }

  return (
    <TooltipProvider>
      <SidebarProvider className="flex h-dvh flex-col overflow-hidden">
        <AppHeader />

        <div className="flex flex-1 overflow-hidden">
          <AppSidebar />
          <div className="flex w-full flex-col overflow-hidden">
            <main className="flex min-h-0 w-full max-w-full flex-1 flex-col overflow-hidden">
              {children}
            </main>
          </div>
        </div>
        <Toaster position="bottom-center" />
      </SidebarProvider>
    </TooltipProvider>
  )
}
