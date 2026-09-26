// Session-expiry handling for every API call the app makes.
//
// The API modules each carry their own small fetch wrapper, and a few
// components call fetch directly, so rather than teaching each one about 401
// the app wraps the global fetch once at start-up: any 401 from a protected
// /api/* path sends the browser to the login page, with the current location
// as `next` so it comes back afterwards. The auth endpoints themselves are
// excluded (a failed login is a 401 that must not bounce the login page) and
// nothing happens while already on /login.

export const LOGIN_PATH = "/login"

/** The login page URL that returns to `current` (a same-origin path) after signing in. */
export function loginHref(current: string): string {
  if (!current || current === "/" || current.startsWith(LOGIN_PATH)) {
    return LOGIN_PATH
  }
  return `${LOGIN_PATH}?next=${encodeURIComponent(current)}`
}

/**
 * A `next` value is only followed when it is a same-origin path: anything with
 * a scheme or a protocol-relative prefix could send the user off-site.
 */
export function safeNext(next: string | undefined): string {
  if (!next || !next.startsWith("/") || next.startsWith("//")) return "/"
  if (next.startsWith(LOGIN_PATH)) return "/"
  return next
}

function requestPath(input: RequestInfo | URL): string | null {
  let raw: string
  if (typeof input === "string") raw = input
  else if (input instanceof URL) raw = input.href
  else raw = input.url
  try {
    const url = new URL(raw, window.location.origin)
    if (url.origin !== window.location.origin) return null
    return url.pathname
  } catch {
    return null
  }
}

/** Whether a response with `status` for `path` means the session is gone. */
export function shouldRedirectToLogin(
  status: number,
  path: string | null,
  currentPath: string,
): boolean {
  if (status !== 401 || path === null) return false
  if (!path.startsWith("/api/")) return false
  if (path.startsWith("/api/auth/")) return false
  return !currentPath.startsWith(LOGIN_PATH)
}

/**
 * Wrap `original` so a 401 from a protected API path calls `onUnauthorized`
 * (once per response) before the response is handed back to the caller.
 */
export function wrapFetch(
  original: typeof fetch,
  onUnauthorized: (loginTo: string) => void,
): typeof fetch {
  return async (input, init) => {
    const res = await original(input, init)
    const current = window.location.pathname
    if (shouldRedirectToLogin(res.status, requestPath(input), current)) {
      onUnauthorized(loginHref(current + window.location.search))
    }
    return res
  }
}

/** Install the wrapper on the global fetch. Call once, before the app renders. */
export function installAuthRedirect(): void {
  globalThis.fetch = wrapFetch(globalThis.fetch.bind(globalThis), (to) => {
    window.location.assign(to)
  })
}
