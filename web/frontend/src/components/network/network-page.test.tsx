import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import * as system from "@/api/system"
import type { TLSStatus } from "@/api/tls"
import { SidebarProvider } from "@/components/ui/sidebar"
import { TooltipProvider } from "@/components/ui/tooltip"

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

// jsdom's location cannot be replaced; the page reloads through this seam.
vi.mock("@/api/system", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/system")>()),
  reloadPage: vi.fn(),
}))
const reloadPage = vi.mocked(system.reloadPage)

const config = {
  gateway: {
    host: "0.0.0.0",
    port: 18790,
    tls_port: 18443,
    external_url: "https://claw.example.com",
    allowed_cidrs: ["192.168.1.0/24"],
    lockout_exempt: ["192.168.1.10"],
    tls: { mode: "all", extra_names: ["claw.home.arpa"] },
  },
  channels: {
    device: {
      host: "127.0.0.1",
      port: 18791,
      auto_approve: false,
      external_url: "https://ops42.example.com:42333",
    },
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
// refetch after an action come back different; `ready` answers each /ready
// poll in turn (the last value repeats).
function stubFetch(opts: {
  config?: unknown
  tlsStatus?: () => TLSStatus
  tlsValidate?: { status: number; body: unknown }
  tlsRegenerate?: () => unknown
  patch?: { status: number; body: unknown }
  restart?: { status: number; body: unknown }
  ready?: number[]
}) {
  const calls: Call[] = []
  const ready = [...(opts.ready ?? [200])]
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
        return reply(200, opts.config ?? config)
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
      if (url.endsWith("/api/system/restart")) {
        const r = opts.restart ?? {
          status: 202,
          body: { status: "restarting" },
        }
        return reply(r.status, r.body)
      }
      if (url.endsWith("/ready")) {
        const status = ready.length > 1 ? ready.shift()! : ready[0]
        return reply(status, {})
      }
      throw new Error(`unexpected fetch ${method} ${url}`)
    }),
  )
  return calls
}

function newClient() {
  return new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
}

function renderPage(qc = newClient()) {
  return render(
    <QueryClientProvider client={qc}>
      <TooltipProvider>
        <SidebarProvider>
          <NetworkPage />
        </SidebarProvider>
      </TooltipProvider>
    </QueryClientProvider>,
  )
}

const value = (id: string) => (screen.getByTestId(id) as HTMLInputElement).value

const patches = (calls: Call[]) =>
  calls.filter((c) => c.method === "PATCH").map((c) => c.body)

// Autosave is debounced; wait on the call rather than the clock.
async function waitForPatch(calls: Call[], n = 1) {
  await waitFor(() => expect(patches(calls).length).toBe(n), {
    timeout: 3000,
  })
}

afterEach(() => {
  vi.unstubAllGlobals()
  reloadPage.mockClear()
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
    expect(value("network-lockout-exempt")).toBe("192.168.1.10")
    expect(value("network-device-port")).toBe("18791")
    // The device address is shown without its scheme.
    expect(value("network-device-external-url")).toBe("ops42.example.com:42333")
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
    // channels.device.tls missing reads as ws.
    expect(
      screen
        .getByRole("radio", { name: "pages.network.device.tls_ws" })
        .getAttribute("aria-checked"),
    ).toBe("true")
    expect(screen.getByTestId("cert-fingerprint").textContent).toBe(
      "AA:BB:CC:DD",
    )
    expect(screen.getByTestId("network-urls").textContent).toContain(
      "https://claw.example.com:18443/",
    )
    expect(screen.queryByTestId("network-restart-banner")).toBe(null)
    // No Save button: fields autosave like the rest of the WebUI.
    expect(screen.queryByTestId("network-save")).toBe(null)
  })

  // The regression: reached from another page, the config is already in the
  // cache on the first render, so the "new data" sync never fires. The form
  // used to stay at its defaults and the next save wrote them over the real
  // listener settings (host back to loopback, allowed networks emptied).
  it("fills the form from a config that is already cached, and saves only the edit", async () => {
    const calls = stubFetch({})
    const qc = newClient()
    qc.setQueryData(["config"], config)
    renderPage(qc)

    await screen.findByTestId("cert-fingerprint")
    expect(value("network-external-url")).toBe("https://claw.example.com")
    expect(value("network-allowed-cidrs")).toBe("192.168.1.0/24")
    expect(
      screen
        .getByRole("radio", { name: "pages.network.http.scope_network" })
        .getAttribute("aria-checked"),
    ).toBe("true")

    fireEvent.change(screen.getByTestId("network-external-url"), {
      target: { value: "https://other.example.com" },
    })
    await waitForPatch(calls)
    expect(patches(calls)[0]).toEqual({
      gateway: { external_url: "https://other.example.com" },
    })
  })

  it("renders no fields, and sends nothing, before the config has loaded", async () => {
    stubFetch({})
    // A fetch that never answers: the page stays in its loading state.
    vi.stubGlobal(
      "fetch",
      vi.fn(() => new Promise<Response>(() => {})),
    )
    renderPage()
    await screen.findByText("labels.loading")
    expect(screen.queryByTestId("network-external-url")).toBe(null)
    await new Promise((r) => setTimeout(r, 700))
    expect(vi.mocked(fetch).mock.calls.every(([, init]) => !init?.method)).toBe(
      true,
    )
  })

  it("sends only the changed field: external URL alone patches gateway.external_url", async () => {
    const calls = stubFetch({})
    renderPage()
    await screen.findByTestId("cert-fingerprint")

    fireEvent.change(screen.getByTestId("network-external-url"), {
      target: { value: "https://other.example.com" },
    })
    await waitForPatch(calls)
    const patch = patches(calls)[0] as { gateway: Record<string, unknown> }
    expect(patch).toEqual({
      gateway: { external_url: "https://other.example.com" },
    })
    expect(patch.gateway).not.toHaveProperty("host")
    expect(patch.gateway).not.toHaveProperty("allowed_cidrs")
    await screen.findByText("pages.network.saved")
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

  it("autosaves listener changes as one patch of the changed leaves and flags the restart", async () => {
    const calls = stubFetch({})
    renderPage()
    await screen.findByTestId("cert-fingerprint")

    fireEvent.change(screen.getByTestId("network-tls-port"), {
      target: { value: "18444" },
    })
    fireEvent.click(
      screen.getByRole("radio", { name: "pages.network.https.mode_localhost" }),
    )

    await screen.findByTestId("network-restart-banner")
    await waitForPatch(calls)
    expect(patches(calls)[0]).toEqual({
      gateway: { tls_port: 18444, tls: { mode: "localhost" } },
    })
    expect(screen.getByTestId("network-restart-banner").textContent).toContain(
      "pages.network.restart_required",
    )
    expect(screen.getByTestId("network-restart")).toBeTruthy()
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

    const err = await screen.findByTestId("network-save-error")
    expect(err.textContent).toBe(
      "gateway.tls_port 18790 must differ from gateway.port",
    )
    expect(screen.getByTestId("network-save-status").textContent).toBe(
      "pages.network.save_failed",
    )
    expect(value("network-external-url")).toBe("https://other.example.com")
    expect(screen.queryByTestId("network-restart-banner")).toBe(null)
  })

  it("restarts the gateway, waits for /ready and reloads", async () => {
    // Down once, then back: the page must not reload from the old process.
    const calls = stubFetch({
      tlsStatus: () => ({ ...tls, restart_required: true }),
      ready: [503, 200],
    })
    renderPage()
    const button = await screen.findByTestId("network-restart")
    fireEvent.click(button)

    await screen.findByText("pages.network.restarting")
    await waitFor(() => expect(reloadPage).toHaveBeenCalledTimes(1), {
      timeout: 4000,
    })
    expect(calls.map((c) => `${c.method} ${c.url}`)).toEqual([
      "POST /api/system/restart",
    ])
  })

  it("shows the server's reason when the gateway is not run as a service", async () => {
    stubFetch({
      tlsStatus: () => ({ ...tls, restart_required: true }),
      restart: {
        status: 409,
        body: { error: "not running as a service; restart ClawEh by hand" },
      },
    })
    renderPage()
    fireEvent.click(await screen.findByTestId("network-restart"))

    const err = await screen.findByTestId("network-restart-error")
    expect(err.textContent).toBe(
      "not running as a service; restart ClawEh by hand",
    )
    expect(reloadPage).not.toHaveBeenCalled()
    expect(
      (screen.getByTestId("network-restart") as HTMLButtonElement).disabled,
    ).toBe(false)
  })

  it("shows channels.device.tls as the ws/wss radio, first in its section, and saves it as a listener change", async () => {
    const calls = stubFetch({
      config: {
        ...config,
        channels: { device: { ...config.channels.device, tls: true } },
      },
    })
    renderPage()
    await screen.findByTestId("cert-fingerprint")
    const group = screen.getByTestId("network-device-tls")
    const wss = screen.getByRole("radio", {
      name: "pages.network.device.tls_wss",
    })
    expect(wss.getAttribute("aria-checked")).toBe("true")
    // The protocol choice comes before every other device control.
    for (const id of [
      "network-device-scope",
      "network-device-port",
      "network-device-external-url",
      "network-device-cidrs",
    ]) {
      const other = screen.getByTestId(id)
      expect(
        group.compareDocumentPosition(other) & Node.DOCUMENT_POSITION_FOLLOWING,
      ).toBeTruthy()
    }

    fireEvent.click(
      screen.getByRole("radio", { name: "pages.network.device.tls_ws" }),
    )
    await waitForPatch(calls)
    expect(patches(calls)[0]).toEqual({
      channels: { device: { tls: false } },
    })
    await screen.findByTestId("network-restart-banner")
  })

  it("saves the device external address as https://host[:port]", async () => {
    const calls = stubFetch({})
    renderPage()
    await screen.findByTestId("cert-fingerprint")

    fireEvent.change(screen.getByTestId("network-device-external-url"), {
      target: { value: "claw.example.com:42333" },
    })
    await waitForPatch(calls)
    expect(patches(calls)[0]).toEqual({
      channels: { device: { external_url: "https://claw.example.com:42333" } },
    })
    expect(screen.queryByTestId("network-restart-banner")).toBe(null)
  })

  it("refuses a device external address with a scheme, under the field, and saves nothing", async () => {
    const calls = stubFetch({})
    renderPage()
    await screen.findByTestId("cert-fingerprint")

    fireEvent.change(screen.getByTestId("network-device-external-url"), {
      target: { value: "wss://claw.example.com:42333" },
    })
    await screen.findByText(
      "Enter a host name or IP address, with an optional :port.",
    )
    expect(screen.queryByTestId("network-save-error")).toBe(null)
    expect(screen.getByTestId("network-save-status").textContent).toBe(
      "pages.network.save_failed",
    )
    await new Promise((r) => setTimeout(r, 300))
    expect(patches(calls)).toEqual([])

    // A corrected value clears the error and saves.
    fireEvent.change(screen.getByTestId("network-device-external-url"), {
      target: { value: "claw.example.com:42333" },
    })
    await waitForPatch(calls)
    expect(
      screen.queryByText(
        "Enter a host name or IP address, with an optional :port.",
      ),
    ).toBe(null)
  })

  it("saves the Never locked out editor as gateway.lockout_exempt", async () => {
    const calls = stubFetch({})
    renderPage()
    await screen.findByTestId("cert-fingerprint")

    fireEvent.change(screen.getByTestId("network-lockout-exempt"), {
      target: { value: "192.168.1.10, 10.0.0.0/8" },
    })
    await waitForPatch(calls)
    expect(patches(calls)[0]).toEqual({
      gateway: { lockout_exempt: ["192.168.1.10", "10.0.0.0/8"] },
    })
    expect(screen.queryByTestId("network-restart-banner")).toBe(null)
  })

  it("marks each plain-HTTP network address with a warning icon", async () => {
    stubFetch({
      tlsStatus: () => ({
        ...tls,
        urls: {
          ...tls.urls,
          http: ["http://10.0.0.5:18790/", "http://192.168.1.5:18790/"],
        },
      }),
    })
    renderPage()

    await screen.findByText("http://10.0.0.5:18790/")
    const marks = screen.getAllByTestId("network-http-warning")
    expect(marks.length).toBe(2)
    // The warning text lives only inside each mark (read by screen readers and
    // shown on hover), not as a sentence under the list.
    expect(marks[0].textContent).toBe("pages.network.urls.http_warning")
    expect(screen.getAllByText("pages.network.urls.http_warning").length).toBe(
      2,
    )
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
