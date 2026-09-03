import { Navigate, Outlet, useRouterState } from "@tanstack/react-router"

export function ChannelsLayout() {
  const pathname = useRouterState({
    select: (state) => state.location.pathname,
  })

  if (pathname === "/channels") {
    return <Navigate to="/channels/$name" params={{ name: "webui" }} />
  }

  return <Outlet />
}
