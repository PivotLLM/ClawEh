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
  allowed_agents: [],
  agent_allowed: true,
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

  const agentSaves = () =>
    (fetch as unknown as ReturnType<typeof vi.fn>).mock.calls.filter(([url]) =>
      String(url).endsWith("/agents"),
    )
  const twoAgents = [
    { id: "alice", name: "Alice" },
    { id: "bob", name: "Bob" },
  ]

  it("shows Assistants as All, with no checkboxes, when none are selected", async () => {
    renderPage([], { devices: [pairedDevice], agents: twoAgents })
    await screen.findByText("Kitchen R1")
    const all = screen.getByRole("radio", { name: "pages.devices.agents_all" })
    expect(all.id).toBe("device-agents-all-device-fedcba9876543210")
    expect(all.getAttribute("aria-checked")).toBe("true")
    expect(
      screen
        .getByRole("radio", { name: "pages.devices.agents_selected" })
        .getAttribute("aria-checked"),
    ).toBe("false")
    expect(screen.queryAllByRole("checkbox")).toHaveLength(0)
    expect(screen.queryByTestId("device-agent-not-allowed")).toBe(null)
  })

  it("shows Selected with the stored assistants checked", async () => {
    renderPage([], {
      devices: [{ ...pairedDevice, allowed_agents: ["bob"] }],
      agents: twoAgents,
    })
    await screen.findByText("Kitchen R1")
    expect(
      screen
        .getByRole("radio", { name: "pages.devices.agents_selected" })
        .getAttribute("aria-checked"),
    ).toBe("true")
    const alice = screen.getByRole("checkbox", { name: "Alice" })
    const bob = screen.getByRole("checkbox", { name: "Bob" })
    expect(alice.id).toBe("device-agent-device-fedcba9876543210-alice")
    expect(alice.getAttribute("aria-checked")).toBe("false")
    expect(bob.getAttribute("aria-checked")).toBe("true")
  })

  it("saves the set when a checkbox is checked", async () => {
    renderPage([], {
      devices: [{ ...pairedDevice, allowed_agents: ["bob"] }],
      agents: twoAgents,
    })
    await screen.findByText("Kitchen R1")
    fireEvent.click(screen.getByRole("checkbox", { name: "Alice" }))
    await waitFor(() => expect(agentSaves()).toHaveLength(1))
    const [url, init] = agentSaves()[0]
    expect(String(url)).toBe("/api/devices/device-fedcba9876543210/agents")
    expect(init?.method).toBe("POST")
    expect(JSON.parse(init?.body as string)).toEqual({
      allowed_agents: ["bob", "alice"],
    })
  })

  it("saves an empty list when All is chosen", async () => {
    renderPage([], {
      devices: [{ ...pairedDevice, allowed_agents: ["bob"] }],
      agents: twoAgents,
    })
    await screen.findByText("Kitchen R1")
    fireEvent.click(
      screen.getByRole("radio", { name: "pages.devices.agents_all" }),
    )
    await waitFor(() => expect(agentSaves()).toHaveLength(1))
    expect(JSON.parse(agentSaves()[0][1]?.body as string)).toEqual({
      allowed_agents: [],
    })
  })

  it("does not save Selected until one assistant is checked", async () => {
    renderPage([], { devices: [pairedDevice], agents: twoAgents })
    await screen.findByText("Kitchen R1")
    fireEvent.click(
      screen.getByRole("radio", { name: "pages.devices.agents_selected" }),
    )
    expect(screen.getByTestId("device-agents-choose-one").textContent).toBe(
      "pages.devices.agents_choose_one",
    )
    expect(screen.getAllByRole("checkbox")).toHaveLength(2)
    expect(agentSaves()).toHaveLength(0)
    fireEvent.click(screen.getByRole("checkbox", { name: "Bob" }))
    await waitFor(() => expect(agentSaves()).toHaveLength(1))
    expect(JSON.parse(agentSaves()[0][1]?.body as string)).toEqual({
      allowed_agents: ["bob"],
    })
  })

  it("ignores clicks while a save is pending and clears the draft after the refetch", async () => {
    renderPage([], {
      devices: [{ ...pairedDevice, allowed_agents: ["bob"] }],
      agents: twoAgents,
    })
    await screen.findByText("Kitchen R1")
    // Hold the POST open; the controls must be disabled until it resolves.
    let release: (() => void) | undefined
    const held = new Promise<void>((r) => (release = r))
    const original = fetch as unknown as (
      input: RequestInfo | URL,
      init?: RequestInit,
    ) => Promise<{ ok: boolean; json: () => Promise<unknown> }>
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        if (String(input).endsWith("/agents")) await held
        return original(input, init)
      }),
    )
    fireEvent.click(screen.getByRole("checkbox", { name: "Alice" }))
    await waitFor(() => expect(agentSaves()).toHaveLength(1))
    const bob = screen.getByRole("checkbox", { name: "Bob" })
    await waitFor(() => expect(bob.hasAttribute("disabled")).toBe(true))
    expect(
      screen
        .getByRole("radio", { name: "pages.devices.agents_all" })
        .hasAttribute("disabled"),
    ).toBe(true)
    fireEvent.click(bob)
    fireEvent.click(
      screen.getByRole("radio", { name: "pages.devices.agents_all" }),
    )
    expect(agentSaves()).toHaveLength(1)
    release?.()
    await waitFor(() => expect(bob.hasAttribute("disabled")).toBe(false))
    expect(agentSaves()).toHaveLength(1)
  })

  it("marks a stored id that is not a configured assistant", async () => {
    renderPage([], {
      devices: [{ ...pairedDevice, allowed_agents: ["bob", "carol"] }],
      agents: twoAgents,
    })
    await screen.findByText("Kitchen R1")
    const carol = screen.getByRole("checkbox", { name: /carol/ })
    expect(carol.id).toBe("device-agent-device-fedcba9876543210-carol")
    expect(
      document.querySelector(`label[for="${carol.id}"]`)?.textContent,
    ).toBe("carol pages.devices.agent_not_found")
    expect(carol.getAttribute("aria-checked")).toBe("true")
    expect(
      screen
        .getByRole("checkbox", { name: "Bob" })
        .getAttribute("aria-checked"),
    ).toBe("true")
  })

  it("warns when the assigned assistant is outside the selected set", async () => {
    renderPage([], {
      devices: [
        {
          ...pairedDevice,
          agent_id: "alice",
          agent_ref: "alice",
          allowed_agents: ["bob"],
          agent_allowed: false,
        },
      ],
      agents: twoAgents,
    })
    await screen.findByText("Kitchen R1")
    expect(screen.getByTestId("device-agent-not-allowed").textContent).toBe(
      "pages.devices.agent_not_allowed",
    )
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
