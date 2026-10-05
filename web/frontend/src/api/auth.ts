// API client for the WebUI login session (POST /api/auth/login etc.). The
// session lives in an HttpOnly cookie the browser attaches on its own, so
// nothing here stores a credential.

export interface AuthStatus {
  /** An admin account exists (`claw admin` has been run on the server). */
  configured: boolean
  /** This browser holds a live session. */
  authenticated: boolean
  username: string
}

/** A refused login: 401 for bad credentials, 429 while locked out. */
export class LoginError extends Error {
  readonly status: number
  /** Seconds until another attempt is accepted; set on 429. */
  readonly retryAfter?: number

  constructor(status: number, message: string, retryAfter?: number) {
    super(message)
    this.name = "LoginError"
    this.status = status
    this.retryAfter = retryAfter
  }
}

export async function getAuthStatus(): Promise<AuthStatus> {
  const res = await fetch("/api/auth/status")
  if (!res.ok) {
    throw new Error(`API error: ${res.status} ${res.statusText}`)
  }
  return res.json() as Promise<AuthStatus>
}

export async function login(username: string, password: string): Promise<void> {
  const res = await fetch("/api/auth/login", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ username, password }),
  })
  if (res.ok) return

  let message = `${res.status} ${res.statusText}`
  let retryAfter: number | undefined
  try {
    const body = (await res.json()) as { error?: string; retry_after?: number }
    if (typeof body.error === "string" && body.error !== "")
      message = body.error
    if (typeof body.retry_after === "number") retryAfter = body.retry_after
  } catch {
    // Non-JSON body: keep the status line.
  }
  throw new LoginError(res.status, message, retryAfter)
}

export async function logout(): Promise<void> {
  const res = await fetch("/api/auth/logout", { method: "POST" })
  if (!res.ok) {
    throw new Error(`API error: ${res.status} ${res.statusText}`)
  }
}
