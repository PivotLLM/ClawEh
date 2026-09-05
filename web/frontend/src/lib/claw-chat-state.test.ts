import { afterEach, describe, expect, it, vi } from "vitest"

import {
  clearStoredSessionId,
  generateSessionId,
  getInitialActiveSessionId,
  normalizeUnixTimestamp,
  readStoredSessionId,
  writeStoredSessionId,
} from "./claw-chat-state"

const KEY = "claw:last-session-id"

afterEach(() => {
  localStorage.clear()
})

describe("stored session id", () => {
  it("round-trips through localStorage", () => {
    writeStoredSessionId("abc-123")
    expect(localStorage.getItem(KEY)).toBe("abc-123")
    expect(readStoredSessionId()).toBe("abc-123")
  })

  // Writing "" is how the caller says "forget the session", so it must remove
  // the key rather than store an empty string that later reads back as a
  // session id of "".
  it("treats an empty write as a clear", () => {
    writeStoredSessionId("abc-123")
    writeStoredSessionId("")
    expect(localStorage.getItem(KEY)).toBeNull()
    expect(readStoredSessionId()).toBe("")
  })

  it("clears explicitly", () => {
    writeStoredSessionId("abc-123")
    clearStoredSessionId()
    expect(readStoredSessionId()).toBe("")
  })

  // A value that is only whitespace is not a usable session id; it would be
  // sent to the gateway as one and resolve to a session nobody owns.
  it("ignores a whitespace-only stored value", () => {
    localStorage.setItem(KEY, "   ")
    expect(readStoredSessionId()).toBe("")
  })

  it("trims a stored value", () => {
    localStorage.setItem(KEY, "  abc-123  ")
    expect(readStoredSessionId()).toBe("abc-123")
  })

  // Private-browsing modes and blocked site data make localStorage throw or be
  // absent entirely. Chat must still work; only the "resume last session"
  // convenience is lost.
  // localStorage is a getter-only property on the jsdom window, so it is
  // replaced by redefining it rather than assigning — the same shape an
  // environment without storage presents. (Plain assignment worked under the
  // older jsdom that vitest 4 pulled in; vitest 5 throws.)
  it("survives localStorage being unavailable", () => {
    const descriptor = Object.getOwnPropertyDescriptor(
      globalThis,
      "localStorage",
    )
    Object.defineProperty(globalThis, "localStorage", {
      value: undefined,
      configurable: true,
    })
    try {
      expect(readStoredSessionId()).toBe("")
      expect(() => writeStoredSessionId("abc")).not.toThrow()
      expect(() => clearStoredSessionId()).not.toThrow()
    } finally {
      if (descriptor) {
        Object.defineProperty(globalThis, "localStorage", descriptor)
      }
    }
  })
})

describe("generateSessionId", () => {
  it("prefers crypto.randomUUID", () => {
    const spy = vi
      .spyOn(globalThis.crypto, "randomUUID")
      .mockReturnValue("11111111-2222-4333-8444-555555555555")
    expect(generateSessionId()).toBe("11111111-2222-4333-8444-555555555555")
    expect(spy).toHaveBeenCalled()
  })

  // The getRandomValues fallback must still produce a well-formed v4 UUID:
  // version nibble 4, variant nibble 8/9/a/b. Getting this wrong yields ids
  // that look like UUIDs but collide or are rejected by anything that parses
  // them.
  it("falls back to getRandomValues and sets the v4 bits", () => {
    const crypto = globalThis.crypto as unknown as Record<string, unknown>
    const originalUUID = crypto.randomUUID
    crypto.randomUUID = undefined
    try {
      const id = generateSessionId()
      expect(id).toMatch(
        /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/,
      )
    } finally {
      crypto.randomUUID = originalUUID
    }
  })

  // globalThis.crypto is a getter-only property, so it is replaced by
  // redefining it rather than assigning — the same shape an environment
  // without web crypto presents.
  it("falls back again when there is no web crypto at all", () => {
    const descriptor = Object.getOwnPropertyDescriptor(globalThis, "crypto")
    Object.defineProperty(globalThis, "crypto", {
      value: undefined,
      configurable: true,
    })
    try {
      const id = generateSessionId()
      expect(id).toMatch(/^session-\d+-[0-9a-f]+$/)
    } finally {
      if (descriptor) Object.defineProperty(globalThis, "crypto", descriptor)
    }
  })

  it("does not repeat itself", () => {
    const ids = new Set(Array.from({ length: 200 }, () => generateSessionId()))
    expect(ids.size).toBe(200)
  })
})

describe("getInitialActiveSessionId", () => {
  it("resumes the stored session when there is one", () => {
    writeStoredSessionId("resume-me")
    expect(getInitialActiveSessionId()).toBe("resume-me")
  })

  it("generates a fresh id when there is nothing stored", () => {
    const id = getInitialActiveSessionId()
    expect(id).not.toBe("")
    expect(id.length).toBeGreaterThan(8)
  })
})

describe("normalizeUnixTimestamp", () => {
  // The gateway sends seconds in some paths and milliseconds in others. Getting
  // this backwards puts messages in 1970 or in the year 55000, which is exactly
  // the kind of thing that is invisible until someone looks at a transcript.
  it("scales seconds up to milliseconds", () => {
    expect(normalizeUnixTimestamp(1_700_000_000)).toBe(1_700_000_000_000)
  })

  it("leaves milliseconds alone", () => {
    expect(normalizeUnixTimestamp(1_700_000_000_000)).toBe(1_700_000_000_000)
  })

  it("puts the threshold at 1e12", () => {
    expect(normalizeUnixTimestamp(1e12)).toBe(1e12)
    expect(normalizeUnixTimestamp(1e12 - 1)).toBe((1e12 - 1) * 1000)
  })

  it("handles zero", () => {
    expect(normalizeUnixTimestamp(0)).toBe(0)
  })
})
