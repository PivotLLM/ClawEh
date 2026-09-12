import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import {
  MEMORY_TYPES,
  bulkMemoryAction,
  createMemoryItem,
  getMemoryStore,
  importMemory,
  memoryExportURL,
  patchMemoryItem,
} from "./memory"

// The curation calls all write to a store the assistant may also be writing,
// so getting the request shape wrong is not caught by anything else: the UI
// would report success and change nothing.

/** Stubs fetch with one canned response. `payload` is what the endpoint
 *  returns — an object for a success, the plain-text error for a failure,
 *  matching how the API actually answers. */
function mockFetch(res: { ok?: boolean; status?: number; payload?: unknown }) {
  const fn = vi.fn().mockResolvedValue({
    ok: res.ok ?? true,
    status: res.status ?? 200,
    json: async () => res.payload ?? {},
    text: async () => (typeof res.payload === "string" ? res.payload : ""),
  })
  vi.stubGlobal("fetch", fn)
  return fn
}

beforeEach(() => {
  vi.unstubAllGlobals()
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe("memory types", () => {
  // Only `event` changes behaviour, and the order is what the operator sees in
  // every dropdown — standing types first, the one that leaves the prompt last.
  it("lists the five types with event last", () => {
    expect([...MEMORY_TYPES]).toEqual([
      "fact",
      "preference",
      "rule",
      "operational",
      "event",
    ])
  })
})

describe("getMemoryStore", () => {
  it("asks for retired memories only when told to", async () => {
    const fn = mockFetch({ payload: { id: "s1", domains: [] } })
    await getMemoryStore("s1")
    expect(fn.mock.calls[0][0]).toBe("/api/memory/s1")

    await getMemoryStore("s1", true)
    expect(fn.mock.calls[1][0]).toBe("/api/memory/s1?include_retired=1")
  })

  it("encodes the store id", async () => {
    const fn = mockFetch({ payload: {} })
    await getMemoryStore("a/b")
    expect(fn.mock.calls[0][0]).toBe("/api/memory/a%2Fb")
  })
})

describe("patchMemoryItem", () => {
  it("PATCHes just the fields given", async () => {
    const fn = mockFetch({ payload: { id: "h1", type: "event" } })
    await patchMemoryItem("s1", "h1", { type: "event" })

    const [url, init] = fn.mock.calls[0]
    expect(url).toBe("/api/memory/s1/memories/h1")
    expect(init.method).toBe("PATCH")
    expect(JSON.parse(init.body)).toEqual({ type: "event" })
  })

  it("surfaces the server's message rather than a bare status", async () => {
    mockFetch({ ok: false, status: 400, payload: "unknown memory type" })
    await expect(patchMemoryItem("s1", "h1", { type: "nope" })).rejects.toThrow(
      "unknown memory type",
    )
  })

  it("falls back to the status when the body is empty", async () => {
    mockFetch({ ok: false, status: 500, payload: "" })
    await expect(patchMemoryItem("s1", "h1", { type: "fact" })).rejects.toThrow(
      "500",
    )
  })
})

describe("bulkMemoryAction", () => {
  it("sends the action, type and ids together", async () => {
    const fn = mockFetch({ payload: { changed: 2 } })
    const res = await bulkMemoryAction("s1", {
      action: "retype",
      type: "event",
      ids: ["h1", "h2"],
    })

    const [url, init] = fn.mock.calls[0]
    expect(url).toBe("/api/memory/s1/bulk")
    expect(init.method).toBe("POST")
    expect(JSON.parse(init.body)).toEqual({
      action: "retype",
      type: "event",
      ids: ["h1", "h2"],
    })
    expect(res.changed).toBe(2)
  })

  // A bulk action over hundreds of rows partially succeeds. The per-id failures
  // have to come back, or the operator has no idea which part worked.
  it("returns per-id failures", async () => {
    mockFetch({ payload: { changed: 1, failed: { h2: "memory not found" } } })
    const res = await bulkMemoryAction("s1", {
      action: "retire",
      ids: ["h1", "h2"],
    })
    expect(res.changed).toBe(1)
    expect(res.failed).toEqual({ h2: "memory not found" })
  })
})

describe("createMemoryItem", () => {
  it("posts to the domain's memories collection", async () => {
    const fn = mockFetch({ payload: { id: "h9", origin: "user" } })
    const m = await createMemoryItem("s1", "d1", {
      type: "rule",
      text: "Do not use the word thuddy.",
    })

    const [url, init] = fn.mock.calls[0]
    expect(url).toBe("/api/memory/s1/domains/d1/memories")
    expect(init.method).toBe("POST")
    expect(JSON.parse(init.body)).toEqual({
      type: "rule",
      text: "Do not use the word thuddy.",
    })
    // The server sets origin=user; the client must not invent it.
    expect(m.origin).toBe("user")
  })
})

describe("importMemory", () => {
  it("puts the mode in the query and the document in the body", async () => {
    const fn = mockFetch({ payload: { memories_created: 3 } })
    await importMemory("s1", "format_version: 1\n", "replace")

    const [url, init] = fn.mock.calls[0]
    expect(url).toBe("/api/memory/s1/import?mode=replace")
    expect(init.method).toBe("POST")
    expect(init.body).toBe("format_version: 1\n")
  })

  it("defaults nothing: the caller always states the mode", async () => {
    const fn = mockFetch({ payload: {} })
    await importMemory("s1", "x", "merge")
    expect(fn.mock.calls[0][0]).toContain("mode=merge")
  })
})

describe("memoryExportURL", () => {
  // A plain URL rather than a fetch, so the browser downloads it directly
  // instead of the page buffering the whole document to re-offer it.
  it("builds the download URL", () => {
    expect(memoryExportURL("s1")).toBe("/api/memory/s1/export")
    expect(memoryExportURL("a/b")).toBe("/api/memory/a%2Fb/export")
  })
})
