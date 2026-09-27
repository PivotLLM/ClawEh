import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import { SidebarProvider } from "@/components/ui/sidebar"

import { DevicesPage } from "./devices-page"

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}))

// The page links to /network; the router is not mounted here.
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to }: { children: React.ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))

const pairStatus = {
  payload: "",
  ips: ["ops42.example.com"],
  port: 42330,
  protocol: "wss",
  enabled: true,
  hasToken: true,
  word_token: "alpha bravo charlie delta echo",
  listen_host: "0.0.0.0",
  listen_port: 18791,
  listen_lan: true,
  external_url: "https://ops42.example.com:42330",
  connect_url: "wss://ops42.example.com:42330",
  warnings: [],
}

const pendingRequest = {
  request_id: "req-1",
  device_id: "device-0123456789abcdef",
  display_name: "Rabbit R1",
  platform: "android",
  client_id: "rabbit",
  role: "node",
  remote_ip: "203.0.113.5",
  created_at_ms: 1,
}

function renderPage(pending: object[] = []) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      const body = url.endsWith("/api/devices/pair")
        ? pairStatus
        : url.endsWith("/api/devices/pending")
          ? { pending }
          : { devices: [], agents: [] }
      return { ok: true, json: async () => body }
    }),
  )
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={qc}>
      <SidebarProvider>
        <DevicesPage />
      </SidebarProvider>
    </QueryClientProvider>,
  )
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe("DevicesPage", () => {
  it("shows the address devices connect to", async () => {
    renderPage()
    await waitFor(() =>
      expect(screen.getByTestId("devices-connect-url").textContent).toBe(
        "wss://ops42.example.com:42330",
      ),
    )
    expect(screen.getByText(/pages\.devices\.connect_to/)).toBeTruthy()
  })

  it("shows the client address of a pending request next to its name", async () => {
    renderPage([pendingRequest])
    await screen.findByText("Rabbit R1")
    expect(screen.getByTestId("pending-remote-ip").textContent).toBe(
      " · from 203.0.113.5",
    )
  })

  it("shows no address for a pending request without one", async () => {
    renderPage([{ ...pendingRequest, remote_ip: "" }])
    await screen.findByText("Rabbit R1")
    expect(screen.queryByTestId("pending-remote-ip")).toBe(null)
  })
})
