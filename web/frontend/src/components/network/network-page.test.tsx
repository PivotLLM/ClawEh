import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import type { TLSStatus } from "@/api/tls"
import { SidebarProvider } from "@/components/ui/sidebar"

import { NetworkPage } from "./network-page"

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (key: string, opts?: Record<string, unknown>) =>
      opts && "message" in opts ? `${key}:${String(opts.message)}` : key,
  }),
}))

// The page links to /devices and /mcp/config; the router is not mounted here.
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to }: { children: React.ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))

const config = {
  gateway: {
    host: "0.0.0.0",
    port: 18790,
    tls_port: 18443,
    external_url: "https://claw.example.com",
    allowed_cidrs: ["192.168.1.0/24"],
    tls: { mode: "all", extra_names: ["claw.home.arpa"] },
  },
  channels: {
    device: { host: "127.0.0.1", port: 18791, auto_approve: false },
  },
  mcp_host: { listen: "127.0.0.1:5911" },
}

const tls: TLSStatus = {
  mode: "all",
  source: "self-signed",
  cert_file: "",
  key_file: "",
  extra_names: ["claw.home.arpa"],
  tls_port: 18443,
  http_host: "0.0.0.0",
  http_port: 18790,
  external_url: "https://claw.example.com",
  urls: {
    localhost: "http://127.0.0.1:18790/",
    http: [],
    https: ["https://claw.example.com:18443/"],
  },
  certificate: {
    present: true,
    subject: "CN=claw",
    names: ["claw", "claw.example.com"],
    not_after: "2027-09-01T00:00:00Z",
    fingerprint: "AA:BB:CC:DD",
    self_signed: true,
  },
  restart_required: false,
}

interface Call {
  url: string
  method: string
  body: unknown
}

// Answers by URL and records every write. `tlsValidate` decides what
// POST /api/tls/validate returns; `tlsStatus` is mutable so a test can make the
// refetch after an action come back different.
function stubFetch(opts: {
  tlsStatus?: () => TLSStatus
  tlsValidate?: { status: number; body: unknown }
  tlsRegenerate?: () => unknown
  patch?: { status: number; body: unknown }
}) {
  const calls: Call[] = []
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      const method = init?.method ?? "GET"
      const body = init?.body ? JSON.parse(String(init.body)) : undefined
      if (method !== "GET") calls.push({ url, method, body })
      const reply = (status: number, json: unknown) => ({
        ok: status < 400,
        status,
        statusText: "",
        json: async () => json,
        text: async () => JSON.stringify(json),
      })
      if (url.endsWith("/api/config") && method === "GET") {
        return reply(200, config)
      }
      if (url.endsWith("/api/config") && method === "PATCH") {
        const p = opts.patch ?? { status: 200, body: { status: "ok" } }
        return reply(p.status, p.body)
      }
      if (url.endsWith("/api/tls") && method === "GET") {
        return reply(200, (opts.tlsStatus ?? (() => tls))())
      }
      if (url.endsWith("/api/tls/validate")) {
        const v = opts.tlsValidate ?? { status: 200, body: tls.certificate }
        return reply(v.status, v.body)
      }
      if (url.endsWith("/api/tls/regenerate")) {
        return reply(200, (opts.tlsRegenerate ?? (() => tls.certificate))())
      }
      throw new Error(`unexpected fetch ${method} ${url}`)
    }),
  )
  return calls
}

function renderPage() {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={qc}>
      <SidebarProvider>
        <NetworkPage />
      </SidebarProvider>
    </QueryClientProvider>,
  )
}

const value = (id: string) => (screen.getByTestId(id) as HTMLInputElement).value

afterEach(() => {
  vi.unstubAllGlobals()
})

describe("NetworkPage", () => {
  it("shows every listener setting from the config, and the live certificate", async () => {
    stubFetch({})
    renderPage()

    await screen.findByTestId("cert-fingerprint")
    expect(value("network-http-port")).toBe("18790")
    expect(value("network-tls-port")).toBe("18443")
    expect(value("network-external-url")).toBe("https://claw.example.com")
    expect(value("network-allowed-cidrs")).toBe("192.168.1.0/24")
    expect(value("network-device-port")).toBe("18791")
    expect(screen.getByTestId("network-mcp-listen").textContent).toBe(
      "127.0.0.1:5911",
    )
    expect(
      screen.getByTestId("network-extra-names-tags").textContent,
    ).toContain("claw.home.arpa")
    // gateway.host = 0.0.0.0 is the "Network" scope; gateway.tls.mode is "all".
    expect(
      screen
        .getByRole("radio", { name: "pages.network.http.scope_network" })
        .getAttribute("aria-checked"),
    ).toBe("true")
    expect(
      screen
        .getByRole("radio", { name: "pages.network.https.mode_all" })
        .getAttribute("aria-checked"),
    ).toBe("true")
    expect(screen.getByTestId("cert-fingerprint").textContent).toBe(
      "AA:BB:CC:DD",
    )
    expect(screen.getByTestId("network-urls").textContent).toContain(
      "https://claw.example.com:18443/",
    )
    expect(screen.queryByTestId("network-restart-banner")).toBe(null)
  })

  it("reveals the certificate paths only under External", async () => {
    stubFetch({})
    renderPage()
    await screen.findByTestId("cert-fingerprint")

    expect(screen.queryByTestId("cert-file")).toBe(null)
    expect(screen.getByTestId("cert-regenerate")).toBeTruthy()

    fireEvent.click(
      screen.getByRole("radio", { name: "pages.network.certificate.external" }),
    )
    expect(screen.getByTestId("cert-file")).toBeTruthy()
    expect(screen.getByTestId("cert-key-file")).toBeTruthy()
    expect(screen.queryByTestId("cert-regenerate")).toBe(null)

    fireEvent.click(
      screen.getByRole("radio", {
        name: "pages.network.certificate.self_signed",
      }),
    )
    expect(screen.queryByTestId("cert-file")).toBe(null)
  })

  // The whole point of validating first: a typo in a path must be reported and
  // must not reach the configuration, or the listener restarts without a cert.
  it("does not save certificate paths the server rejects", async () => {
    const calls = stubFetch({
      tlsValidate: {
        status: 400,
        body: { error: "open /nope/fullchain.pem: no such file" },
      },
    })
    renderPage()
    await screen.findByTestId("cert-fingerprint")

    fireEvent.click(
      screen.getByRole("radio", { name: "pages.network.certificate.external" }),
    )
    fireEvent.change(screen.getByTestId("cert-file"), {
      target: { value: "/nope/fullchain.pem" },
    })
    fireEvent.change(screen.getByTestId("cert-key-file"), {
      target: { value: "/nope/privkey.pem" },
    })
    fireEvent.click(screen.getByTestId("cert-save"))

    const err = await screen.findByTestId("cert-error")
    expect(err.textContent).toBe("open /nope/fullchain.pem: no such file")
    expect(calls.map((c) => c.url)).toEqual(["/api/tls/validate"])
  })

  it("saves certificate paths once the server has loaded them", async () => {
    const calls = stubFetch({})
    renderPage()
    await screen.findByTestId("cert-fingerprint")

    fireEvent.click(
      screen.getByRole("radio", { name: "pages.network.certificate.external" }),
    )
    fireEvent.change(screen.getByTestId("cert-file"), {
      target: { value: "/etc/ssl/claw.pem" },
    })
    fireEvent.change(screen.getByTestId("cert-key-file"), {
      target: { value: "/etc/ssl/claw.key" },
    })
    fireEvent.click(screen.getByTestId("cert-save"))

    await screen.findByTestId("cert-notice")
    expect(calls.map((c) => `${c.method} ${c.url}`)).toEqual([
      "POST /api/tls/validate",
      "PATCH /api/config",
    ])
    expect(calls[1].body).toEqual({
      gateway: {
        tls: { cert_file: "/etc/ssl/claw.pem", key_file: "/etc/ssl/claw.key" },
      },
    })
  })

  it("regenerates the self-signed certificate and shows the new fingerprint", async () => {
    let fingerprint = "AA:BB:CC:DD"
    const calls = stubFetch({
      tlsStatus: () => ({
        ...tls,
        certificate: { ...tls.certificate!, fingerprint },
      }),
      tlsRegenerate: () => {
        fingerprint = "11:22:33:44"
        return { ...tls.certificate, fingerprint }
      },
    })
    renderPage()
    await screen.findByTestId("cert-fingerprint")

    fireEvent.click(screen.getByTestId("cert-regenerate"))

    await waitFor(() =>
      expect(screen.getByTestId("cert-fingerprint").textContent).toBe(
        "11:22:33:44",
      ),
    )
    expect(calls.map((c) => `${c.method} ${c.url}`)).toEqual([
      "POST /api/tls/regenerate",
    ])
  })

  it("saves the listener settings as one patch and flags the restart", async () => {
    const calls = stubFetch({})
    renderPage()
    await screen.findByTestId("cert-fingerprint")

    const save = screen.getByTestId("network-save") as HTMLButtonElement
    expect(save.disabled).toBe(true)

    fireEvent.change(screen.getByTestId("network-tls-port"), {
      target: { value: "18444" },
    })
    fireEvent.click(
      screen.getByRole("radio", { name: "pages.network.https.mode_localhost" }),
    )
    expect(save.disabled).toBe(false)
    fireEvent.click(save)

    await screen.findByTestId("network-restart-banner")
    const patch = calls.find((c) => c.method === "PATCH")?.body as {
      gateway: Record<string, unknown>
      channels: Record<string, unknown>
    }
    expect(patch.gateway).toEqual({
      host: "0.0.0.0",
      port: 18790,
      tls_port: 18444,
      external_url: "https://claw.example.com",
      allowed_cidrs: ["192.168.1.0/24"],
      tls: { mode: "localhost", extra_names: ["claw.home.arpa"] },
    })
    expect(patch.channels).toEqual({
      device: {
        host: "127.0.0.1",
        port: 18791,
        external_url: "",
        allowed_cidrs: [],
        auto_approve: false,
      },
    })
  })

  it("shows the server's validation error inline and keeps the form", async () => {
    stubFetch({
      patch: {
        status: 400,
        body: { error: "gateway.tls_port 18790 must differ from gateway.port" },
      },
    })
    renderPage()
    await screen.findByTestId("cert-fingerprint")

    fireEvent.change(screen.getByTestId("network-external-url"), {
      target: { value: "https://other.example.com" },
    })
    fireEvent.click(screen.getByTestId("network-save"))

    const err = await screen.findByTestId("network-save-error")
    expect(err.textContent).toBe(
      "gateway.tls_port 18790 must differ from gateway.port",
    )
    expect(value("network-external-url")).toBe("https://other.example.com")
    expect(screen.queryByTestId("network-restart-banner")).toBe(null)
  })

  it("says so when no certificate has been generated yet", async () => {
    stubFetch({
      tlsStatus: () => ({
        ...tls,
        mode: "off",
        urls: { localhost: tls.urls.localhost, http: [], https: [] },
        // The server sends an empty object, not null, when there is none yet.
        certificate: {
          present: false,
          subject: "",
          names: [],
          not_after: "",
          fingerprint: "",
          self_signed: false,
        },
      }),
    })
    renderPage()

    await screen.findByTestId("cert-none")
    expect(screen.queryByTestId("cert-fingerprint")).toBe(null)
    expect(screen.getByText("pages.network.urls.none")).toBeTruthy()
  })
})
