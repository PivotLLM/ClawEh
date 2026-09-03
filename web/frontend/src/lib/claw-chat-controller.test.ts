import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { getWebUIToken } from "@/api/webui"

import { expectConsole } from "../test-setup"
import { connectChat, disconnectChat } from "./claw-chat-controller"

vi.mock("@/api/webui", () => ({ getWebUIToken: vi.fn() }))
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

beforeEach(() => {
  opened = []
  localStorage.clear()
  vi.stubGlobal("WebSocket", FakeWebSocket)
  vi.mocked(getWebUIToken).mockResolvedValue({
    token: "s3cret-token",
    ws_url: "ws://127.0.0.1:18790/webui/ws",
    enabled: true,
  })
})

afterEach(() => {
  disconnectChat()
  vi.unstubAllGlobals()
})

describe("connectChat token handling", () => {
  // This is the property commit 7a18938 exists to establish. A token in the
  // query string is recorded by proxies, access logs, Referer headers and
  // browser history; the subprotocol is not. If someone ever "simplifies" this
  // back to ?token=, this test fails.
  it("sends the token as a subprotocol, never in the URL", async () => {
    await connectChat()

    expect(opened).toHaveLength(1)
    const [socket] = opened

    expect(socket.protocols).toEqual(["claw-token", "s3cret-token"])
    expect(socket.url).not.toContain("s3cret-token")
    expect(socket.url).not.toContain("token=")
  })

  // The marker must match channels/webui.TokenSubprotocol on the Go side. A
  // mismatch fails the handshake with no useful client-side error.
  it("offers the claw-token marker first", async () => {
    await connectChat()
    expect((opened[0].protocols as string[])[0]).toBe("claw-token")
  })

  it("passes the session id in the query string", async () => {
    await connectChat()
    const url = new URL(opened[0].url)
    expect(url.searchParams.get("session_id")).toBeTruthy()
  })

  // The gateway reports its own bind address, which is loopback by default. A
  // browser on another machine cannot reach that, so the controller rewrites
  // the host to whatever the page was served from.
  it("rewrites a loopback ws_url to the browsing host", async () => {
    vi.mocked(getWebUIToken).mockResolvedValue({
      token: "t",
      ws_url: "ws://127.0.0.1:18790/webui/ws",
      enabled: true,
    })
    vi.stubGlobal("location", {
      ...window.location,
      hostname: "claw.example.lan",
      protocol: "http:",
      host: "claw.example.lan",
    })

    await connectChat()
    expect(new URL(opened[0].url).hostname).toBe("claw.example.lan")
  })

  // …but only when the browser is genuinely elsewhere. Rewriting while the
  // browser IS on localhost would be a no-op at best and wrong at worst.
  it("leaves the ws_url alone when the browser is on localhost", async () => {
    vi.stubGlobal("location", {
      ...window.location,
      hostname: "localhost",
      protocol: "http:",
      host: "localhost",
    })

    await connectChat()
    expect(new URL(opened[0].url).hostname).toBe("127.0.0.1")
  })

  // No token means the gateway refused to issue one. Opening a socket anyway
  // would fail the handshake and start the reconnect loop against a server that
  // is working exactly as intended.
  it("does not open a socket when no token is issued", async () => {
    // The controller logs this deliberately; declared so the console guard in
    // test-setup.ts treats it as expected rather than as a failure.
    expectConsole(/No webui token available/)
    vi.mocked(getWebUIToken).mockResolvedValue({
      token: "",
      ws_url: "ws://x/y",
      enabled: true,
    })
    await connectChat()
    expect(opened).toHaveLength(0)
  })

  it("does not open a second socket while one is connecting", async () => {
    await connectChat()
    await connectChat()
    expect(opened).toHaveLength(1)
  })
})
