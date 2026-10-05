import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import {
  chatSocketUrl,
  connectChat,
  disconnectChat,
} from "./claw-chat-controller"

// Every socket the controller opens, recorded so the test can assert on the URL
// and the subprotocols it was constructed with.
interface OpenedSocket {
  url: string
  protocols: string | string[] | undefined
  socket: FakeWebSocket
}

let opened: OpenedSocket[] = []

class FakeWebSocket {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSING = 2
  static readonly CLOSED = 3

  readyState = FakeWebSocket.CONNECTING
  onopen: (() => void) | null = null
  onclose: ((e: unknown) => void) | null = null
  onerror: ((e: unknown) => void) | null = null
  onmessage: ((e: unknown) => void) | null = null

  constructor(url: string, protocols?: string | string[]) {
    opened.push({ url, protocols, socket: this })
  }

  // A refused handshake: the browser fires error then close, no status.
  fail() {
    this.readyState = FakeWebSocket.CLOSED
    this.onerror?.({})
    this.onclose?.({})
  }

  send() {}
  close() {
    this.readyState = FakeWebSocket.CLOSED
  }
}

function stubLocation(overrides: Partial<Location>) {
  vi.stubGlobal("location", { ...window.location, ...overrides })
}

beforeEach(() => {
  opened = []
  localStorage.clear()
  vi.stubGlobal("WebSocket", FakeWebSocket)
  vi.stubGlobal("fetch", vi.fn())
  stubLocation({
    protocol: "http:",
    host: "localhost:18790",
    hostname: "localhost",
  })
})

afterEach(() => {
  disconnectChat()
  vi.unstubAllGlobals()
})

describe("connectChat session handling", () => {
  // The browser authenticates the socket with its login session cookie, which
  // it attaches on its own. Nothing about the WebUI channel token reaches the
  // browser any more: no fetch for it, no subprotocol, nothing in the URL.
  it("opens the socket with no token and no subprotocol", async () => {
    await connectChat()

    expect(opened).toHaveLength(1)
    const [socket] = opened
    expect(socket.protocols).toBeUndefined()
    expect(socket.url).not.toContain("token")
    expect(fetch).not.toHaveBeenCalled()
  })

  // Same origin as the page: that is what makes the cookie travel with the
  // handshake, and what the server's origin check requires.
  it("connects to /webui/ws on the page's own origin", async () => {
    await connectChat()
    const url = new URL(opened[0].url)
    expect(url.protocol).toBe("ws:")
    expect(url.host).toBe("localhost:18790")
    expect(url.pathname).toBe("/webui/ws")
  })

  it("passes the session id in the query string", async () => {
    await connectChat()
    const url = new URL(opened[0].url)
    expect(url.searchParams.get("session_id")).toBeTruthy()
  })

  it("uses wss: when the page was served over https", () => {
    stubLocation({ protocol: "https:", host: "claw.example.com" })
    expect(chatSocketUrl("abc")).toBe(
      "wss://claw.example.com/webui/ws?session_id=abc",
    )
  })

  it("does not open a second socket while one is connecting", async () => {
    await connectChat()
    await connectChat()
    expect(opened).toHaveLength(1)
  })
})

// The browser hides why a handshake failed, so after every failure the
// controller asks /api/auth/status once and acts on the answer: a dead session
// goes to the login page, a gateway that is down is retried on a 5s cap, and
// anything else keeps the 30s cap.
describe("reconnect after a socket failure", () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  // Let the probe's fetch, its json() and the scheduling settle.
  async function flush() {
    for (let i = 0; i < 10; i++) await Promise.resolve()
  }

  async function failCurrentSocket() {
    opened[opened.length - 1].socket.fail()
    await flush()
  }

  function authStatus(authenticated: boolean) {
    return { ok: true, status: 200, json: async () => ({ authenticated }) }
  }

  it("probes the session once per failure and goes to login when it is gone", async () => {
    const assign = vi.fn()
    stubLocation({ pathname: "/models", search: "?tab=2", assign })
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(authStatus(false)))

    await connectChat()
    await failCurrentSocket()

    // error and close both fired for the one socket: one probe, not two.
    expect(fetch).toHaveBeenCalledTimes(1)
    expect(fetch).toHaveBeenCalledWith("/api/auth/status")
    expect(assign).toHaveBeenCalledWith("/login?next=%2Fmodels%3Ftab%3D2")

    await vi.advanceTimersByTimeAsync(60000)
    expect(opened).toHaveLength(1)
  })

  it("treats a 401 from the probe as a lost session", async () => {
    const assign = vi.fn()
    stubLocation({ pathname: "/agents", search: "", assign })
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({ ok: false, status: 401 }),
    )

    await connectChat()
    await failCurrentSocket()
    await vi.advanceTimersByTimeAsync(60000)

    expect(assign).toHaveBeenCalledWith("/login?next=%2Fagents")
    expect(opened).toHaveLength(1)
  })

  it("keeps reconnecting on a 5 second cap while the gateway is unreachable", async () => {
    const assign = vi.fn()
    stubLocation({ pathname: "/", search: "", assign })
    vi.stubGlobal(
      "fetch",
      vi.fn().mockRejectedValue(new TypeError("Failed to fetch")),
    )

    await connectChat()
    // 1s, 2s, 4s, then 5s for ever: a new attempt within 5s every time.
    for (let i = 0; i < 6; i++) {
      const before = opened.length
      await failCurrentSocket()
      await vi.advanceTimersByTimeAsync(5000)
      expect(opened).toHaveLength(before + 1)
    }
    expect(fetch).toHaveBeenCalledTimes(6)
    expect(assign).not.toHaveBeenCalled()
  })

  it("backs off towards 30 seconds when the gateway answers and the session is valid", async () => {
    const assign = vi.fn()
    stubLocation({ pathname: "/", search: "", assign })
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(authStatus(true)))

    await connectChat()
    // 1s, 2s, 4s: the fourth wait is 8s, past the cap that applies only while down.
    for (let i = 0; i < 3; i++) {
      await failCurrentSocket()
      await vi.advanceTimersByTimeAsync(4000)
    }
    const before = opened.length
    await failCurrentSocket()
    await vi.advanceTimersByTimeAsync(5000)
    expect(opened).toHaveLength(before)
    await vi.advanceTimersByTimeAsync(3000)
    expect(opened).toHaveLength(before + 1)
    expect(assign).not.toHaveBeenCalled()
  })
})
