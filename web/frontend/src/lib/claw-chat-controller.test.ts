import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import {
  chatSocketUrl,
  connectChat,
  disconnectChat,
} from "./claw-chat-controller"

vi.mock("@/api/sessions", () => ({ getSessionHistory: vi.fn() }))
vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn() } }))
vi.mock("@/i18n", () => ({ default: { t: (k: string) => k } }))

// Every socket the controller opens, recorded so the test can assert on the URL
// and the subprotocols it was constructed with.
interface OpenedSocket {
  url: string
  protocols: string | string[] | undefined
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
    opened.push({ url, protocols })
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
