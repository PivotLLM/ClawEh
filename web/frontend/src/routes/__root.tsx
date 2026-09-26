import { createRootRoute, redirect } from "@tanstack/react-router"

import { getAuthStatus } from "@/api/auth"
import { RootLayout } from "@/components/root-layout"
import { LOGIN_PATH, loginHref } from "@/lib/auth-redirect"

export const Route = createRootRoute({
  // The auth gate: every route except the login page needs a session. The
  // server enforces this on /api/* regardless — the redirect is so the user
  // sees a login form rather than a page full of failed requests. A transient
  // error reaching /api/auth/status does not redirect: the page's own API
  // calls will 401 and the fetch wrapper (lib/auth-redirect) handles that.
  beforeLoad: async ({ location }) => {
    if (location.pathname === LOGIN_PATH) return
    let authenticated = true
    try {
      authenticated = (await getAuthStatus()).authenticated
    } catch {
      return
    }
    if (!authenticated) {
      throw redirect({
        href: loginHref(location.pathname + location.searchStr),
      })
    }
  },
  component: RootLayout,
})
