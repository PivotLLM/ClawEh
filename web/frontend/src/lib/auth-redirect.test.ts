import { afterEach, describe, expect, it, vi } from "vitest"

import {
  loginHref,
  safeNext,
  shouldRedirectToLogin,
  wrapFetch,
} from "./auth-redirect"

afterEach(() => {
  vi.unstubAllGlobals()
})

describe("shouldRedirectToLogin", () => {
  it("redirects on a 401 from a protected API path", () => {
    expect(shouldRedirectToLogin(401, "/api/config", "/config")).toBe(true)
  })

  it("ignores other statuses", () => {
    expect(shouldRedirectToLogin(403, "/api/config", "/config")).toBe(false)
    expect(shouldRedirectToLogin(500, "/api/config", "/config")).toBe(false)
  })

  // A failed login IS a 401 — bouncing the login page to itself would loop.
  it("ignores the auth endpoints", () => {
    expect(shouldRedirectToLogin(401, "/api/auth/login", "/")).toBe(false)
    expect(shouldRedirectToLogin(401, "/api/auth/status", "/")).toBe(false)
  })

  it("ignores non-API and cross-origin requests", () => {
    expect(shouldRedirectToLogin(401, "/logo.png", "/")).toBe(false)
    expect(shouldRedirectToLogin(401, null, "/")).toBe(false)
  })

  it("does nothing while already on the login page", () => {
    expect(shouldRedirectToLogin(401, "/api/config", "/login")).toBe(false)
  })
})

describe("loginHref / safeNext", () => {
  it("carries the current location as next", () => {
    expect(loginHref("/models?tab=2")).toBe("/login?next=%2Fmodels%3Ftab%3D2")
    expect(loginHref("/")).toBe("/login")
    expect(loginHref("/login?next=%2Fx")).toBe("/login")
  })

  // An open redirect through `next` is the classic login-page bug.
  it("only follows same-origin paths", () => {
    expect(safeNext("/models")).toBe("/models")
    expect(safeNext("https://evil.example.com")).toBe("/")
    expect(safeNext("//evil.example.com")).toBe("/")
    expect(safeNext("/login")).toBe("/")
    expect(safeNext(undefined)).toBe("/")
  })
})

describe("wrapFetch", () => {
  it("calls onUnauthorized for a protected 401 and still returns the response", async () => {
    vi.stubGlobal("location", {
      ...window.location,
      origin: "http://claw.local",
      pathname: "/config",
      search: "",
    })
    const original = vi.fn().mockResolvedValue({ status: 401 } as Response)
    const onUnauthorized = vi.fn()
    const wrapped = wrapFetch(
      original as unknown as typeof fetch,
      onUnauthorized,
    )

    const res = await wrapped("/api/config")
    expect(res.status).toBe(401)
    expect(onUnauthorized).toHaveBeenCalledWith("/login?next=%2Fconfig")

    onUnauthorized.mockClear()
    await wrapped("/api/auth/login", { method: "POST" })
    expect(onUnauthorized).not.toHaveBeenCalled()

    original.mockResolvedValue({ status: 200 } as Response)
    await wrapped("/api/config")
    expect(onUnauthorized).not.toHaveBeenCalled()
  })
})
