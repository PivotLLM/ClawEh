import { Outlet, createFileRoute, redirect } from "@tanstack/react-router"

// The Config page was split into Network (/network) and System (/system). The
// old address still works: it lands on System. /config/raw stays where it is,
// so the redirect is for this path alone, not for the children.
export const Route = createFileRoute("/config")({
  beforeLoad: ({ location }) => {
    if (location.pathname.replace(/\/+$/, "") === "/config") {
      throw redirect({ to: "/system", replace: true })
    }
  },
  component: Outlet,
})
