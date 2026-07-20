import {
  Navigate,
  Outlet,
  createFileRoute,
  useRouterState,
} from "@tanstack/react-router"

export const Route = createFileRoute("/mcp")({
  component: MCPLayout,
})

function MCPLayout() {
  const pathname = useRouterState({
    select: (state) => state.location.pathname,
  })

  if (pathname === "/mcp") {
    return <Navigate to="/mcp/config" />
  }

  return <Outlet />
}
