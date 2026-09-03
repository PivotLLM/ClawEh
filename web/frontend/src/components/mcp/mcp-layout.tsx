import { Navigate, Outlet, useRouterState } from "@tanstack/react-router"

export function MCPLayout() {
  const pathname = useRouterState({
    select: (state) => state.location.pathname,
  })

  if (pathname === "/mcp") {
    return <Navigate to="/mcp/config" />
  }

  return <Outlet />
}
