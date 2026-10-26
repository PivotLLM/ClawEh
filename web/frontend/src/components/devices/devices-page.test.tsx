import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import { SidebarProvider } from "@/components/ui/sidebar"
import { TooltipProvider } from "@/components/ui/tooltip"

import { DevicesPage } from "./devices-page"

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (key: string, opts?: { agent?: string }) =>
      opts?.agent ? `${key}:${opts.agent}` : key,
  }),
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

const pairedDevice = {
  device_id: "device-fedcba9876543210",
  display_name: "Kitchen R1",
  platform: "android",
  client_mode: "node",
  roles: ["node"],
  scopes: [],
  agent_id: "",
  agent_missing: false,
  user_name: "",
  user_default: "alice",
  approved_at_ms: 1,
  last_seen_at_ms: 1,
}

function renderPage(
  pending: object[] = [],
  paired: { devices: object[]; agents: object[] } = { devices: [], agents: [] },
  pair: object = pairStatus,
) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      const body = url.endsWith("/api/devices/pair")
        ? pair
        : url.endsWith("/api/devices/pending")
          ? { pending }
          : paired
      return { ok: true, json: async () => body }
    }),
  )
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={qc}>
      <TooltipProvider>
        <SidebarProvider>
          <DevicesPage />
        </SidebarProvider>
      </TooltipProvider>
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

  it("shows access as a label, with no warning when devices can connect", async () => {
    renderPage()
    const access = await screen.findByTestId("devices-access")
    expect(access.textContent).toContain("network")
    expect(screen.queryByTestId("devices-warning")).toBeNull()
    expect(
      screen.getByRole("link", { name: "Change" }).getAttribute("href"),
    ).toBe("/network")
  })

  it("marks a problem with an icon whose hover text names it", async () => {
    renderPage([], undefined, {
      ...pairStatus,
      listen_lan: false,
      warnings: ["Devices on your network can't connect."],
    })
    const access = await screen.findByTestId("devices-access")
    expect(access.textContent).toContain("localhost")
    expect(screen.getByTestId("devices-warning").textContent).toBe(
      "Devices on your network can't connect.",
    )
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

  it("marks an assignment to a deleted agent", async () => {
    renderPage([], {
      devices: [
        {
          ...pairedDevice,
          agent_id: "Bob",
          agent_ref: "bob",
          agent_missing: true,
          agent_state: "not_found",
        },
      ],
      agents: [{ id: "alice", name: "Alice" }],
    })
    const note = await screen.findByTestId("device-agent-missing")
    expect(note.textContent).toBe("pages.devices.agent_missing:Bob")
    const select = screen.getByLabelText("Assistant") as HTMLSelectElement
    expect(select.value).toBe("bob")
    expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual([
      "pages.devices.default_assistant",
      "Bob",
      "Alice",
    ])
  })

  it("names a disabled agent in the note, with one option for it", async () => {
    renderPage([], {
      devices: [
        {
          ...pairedDevice,
          agent_id: "Bob",
          agent_ref: "bob",
          agent_name: "Bob B.",
          agent_missing: true,
          agent_state: "disabled",
        },
      ],
      agents: [{ id: "bob", name: "Bob B." }],
    })
    const note = await screen.findByTestId("device-agent-missing")
    expect(note.textContent).toBe("pages.devices.agent_disabled:Bob B.")
    const select = screen.getByLabelText("Assistant") as HTMLSelectElement
    expect(select.value).toBe("bob")
    expect(screen.getAllByRole("option")).toHaveLength(2)
  })

  it("marks a human agent that is set aside", async () => {
    renderPage([], {
      devices: [
        {
          ...pairedDevice,
          agent_id: "bob",
          agent_ref: "bob",
          agent_name: "Bob",
          agent_missing: true,
          agent_state: "set_aside",
        },
      ],
      agents: [{ id: "bob", name: "Bob" }],
    })
    const note = await screen.findByTestId("device-agent-missing")
    expect(note.textContent).toBe("pages.devices.agent_set_aside:Bob")
  })

  it("shows the device's user name in the User field", async () => {
    renderPage([], {
      devices: [{ ...pairedDevice, user_name: "Bob" }],
      agents: [],
    })
    await screen.findByText("Kitchen R1")
    const field = screen.getByLabelText(
      "pages.devices.user_label",
    ) as HTMLInputElement
    expect(field.id).toBe("device-user-device-fedcba9876543210")
    expect(field.value).toBe("Bob")
    expect(screen.getByText("pages.devices.user_hint")).toBeTruthy()
  })

  it("shows the default user as the placeholder of an empty User field", async () => {
    renderPage([], { devices: [pairedDevice], agents: [] })
    await screen.findByText("Kitchen R1")
    const field = screen.getByLabelText(
      "pages.devices.user_label",
    ) as HTMLInputElement
    expect(field.value).toBe("")
    expect(field.placeholder).toBe("alice")
  })

  it("falls back to the device name as placeholder when there is no default user", async () => {
    renderPage([], {
      devices: [{ ...pairedDevice, user_default: "" }],
      agents: [],
    })
    await screen.findByText("Kitchen R1")
    const field = screen.getByLabelText(
      "pages.devices.user_label",
    ) as HTMLInputElement
    expect(field.placeholder).toBe("Kitchen R1")
  })

  it("saves the User field on blur", async () => {
    renderPage([], { devices: [pairedDevice], agents: [] })
    await screen.findByText("Kitchen R1")
    const field = screen.getByLabelText("pages.devices.user_label")
    fireEvent.change(field, { target: { value: " Bob " } })
    fireEvent.blur(field)
    await waitFor(() => {
      const calls = (fetch as unknown as ReturnType<typeof vi.fn>).mock.calls
      const save = calls.find(([url]) => String(url).endsWith("/user"))
      expect(save).toBeTruthy()
      expect(String(save?.[0])).toBe(
        "/api/devices/device-fedcba9876543210/user",
      )
      expect(save?.[1]?.method).toBe("POST")
      expect(JSON.parse(save?.[1]?.body as string)).toEqual({
        user_name: "Bob",
      })
    })
  })

  it("saves the User field on Enter and sends nothing when unchanged", async () => {
    renderPage([], {
      devices: [{ ...pairedDevice, user_name: "Bob" }],
      agents: [],
    })
    await screen.findByText("Kitchen R1")
    const field = screen.getByLabelText("pages.devices.user_label")
    const saves = () =>
      (fetch as unknown as ReturnType<typeof vi.fn>).mock.calls.filter(
        ([url]) => String(url).endsWith("/user"),
      )
    fireEvent.change(field, { target: { value: "Bob" } })
    fireEvent.keyDown(field, { key: "Enter" })
    expect(saves()).toHaveLength(0)
    fireEvent.change(field, { target: { value: "" } })
    fireEvent.keyDown(field, { key: "Enter" })
    await waitFor(() => expect(saves()).toHaveLength(1))
    expect(JSON.parse(saves()[0][1]?.body as string)).toEqual({
      user_name: "",
    })
  })

  it("shows no note for a valid assignment", async () => {
    renderPage([], {
      devices: [{ ...pairedDevice, agent_id: "alice", agent_ref: "alice" }],
      agents: [{ id: "alice", name: "Alice" }],
    })
    await screen.findByText("Kitchen R1")
    expect(screen.queryByTestId("device-agent-missing")).toBe(null)
  })
})
