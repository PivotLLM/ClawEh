import { Outlet, useRouterState } from "@tanstack/react-router"

import { ConfigPage } from "@/components/config/config-page"

export function ConfigLayout() {
  const pathname = useRouterState({
    select: (state) => state.location.pathname,
  })

  if (pathname === "/config") {
    return <ConfigPage />
  }

  return <Outlet />
}
