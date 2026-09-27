import { describe, expect, it } from "vitest"

import {
  DEVICE_HOST_ERROR,
  EMPTY_NETWORK_FORM,
  NetworkFieldError,
  type NetworkForm,
  buildNetworkFormFromConfig,
  buildNetworkPatch,
  deviceHostFromURL,
  deviceURLFromHost,
  diffPatch,
  listenerChanged,
} from "./network-form"

const loaded: NetworkForm = buildNetworkFormFromConfig({
  gateway: {
    host: "0.0.0.0",
    port: 18790,
    tls_port: 18443,
    external_url: "https://claw.example.com",
    allowed_cidrs: ["10.0.0.0/8", "192.168.1.0/24", "172.16.0.0/12"],
    lockout_exempt: ["192.168.1.10"],
    tls: { mode: "all", extra_names: ["claw.home.arpa"] },
  },
  channels: {
    device: {
      host: "0.0.0.0",
      port: 18791,
      tls: true,
      external_url: "https://ops42.example.com:42333",
    },
  },
  mcp_host: { listen: "127.0.0.1:5911" },
})

describe("diffPatch", () => {
  it("keeps only the leaves that differ, preserving the nesting", () => {
    expect(
      diffPatch(
        { a: { b: 1, c: "x", d: [1, 2] }, e: true, f: { g: 1 } },
        { a: { b: 1, c: "y", d: [1, 2] }, e: false, f: { g: 1 } },
      ),
    ).toEqual({ a: { c: "x" }, e: true })
  })

  it("treats arrays as leaves and returns {} for equal inputs", () => {
    expect(diffPatch({ a: [1, 2] }, { a: [1, 3] })).toEqual({ a: [1, 2] })
    expect(diffPatch({ a: { b: [1] } }, { a: { b: [1] } })).toEqual({})
  })
})

describe("buildNetworkPatch", () => {
  it("sends nothing when nothing changed", () => {
    expect(buildNetworkPatch(loaded, loaded)).toEqual({})
  })

  // The production failure: an unchanged host and allowlist must never be
  // re-sent, so a form that was not filled correctly cannot overwrite them.
  it("sends only gateway.external_url when only it changed", () => {
    const patch = buildNetworkPatch(
      { ...loaded, externalUrl: "https://other.example.com" },
      loaded,
    )
    expect(patch).toEqual({
      gateway: { external_url: "https://other.example.com" },
    })
  })

  it("sends the allowlist only when the parsed list differs", () => {
    // Same networks, different whitespace: no change.
    expect(
      buildNetworkPatch(
        {
          ...loaded,
          allowedCIDRsText: "10.0.0.0/8, 192.168.1.0/24\n172.16.0.0/12\n",
        },
        loaded,
      ),
    ).toEqual({})
    expect(
      buildNetworkPatch({ ...loaded, allowedCIDRsText: "10.0.0.0/8" }, loaded),
    ).toEqual({ gateway: { allowed_cidrs: ["10.0.0.0/8"] } })
  })

  it("maps scope, ports, HTTPS mode and device TLS to their keys", () => {
    const patch = buildNetworkPatch(
      {
        ...loaded,
        httpScope: "localhost",
        tlsPort: "18444",
        httpsMode: "localhost",
        deviceTLS: false,
        deviceAutoApprove: true,
      },
      loaded,
    )
    expect(patch).toEqual({
      gateway: {
        host: "127.0.0.1",
        tls_port: 18444,
        tls: { mode: "localhost" },
      },
      channels: { device: { tls: false, auto_approve: true } },
    })
  })

  it("sends gateway.lockout_exempt from the editor, parsed like the allowlist", () => {
    expect(
      buildNetworkPatch(
        { ...loaded, lockoutExemptText: "192.168.1.10, 10.0.0.0/8\n" },
        loaded,
      ),
    ).toEqual({ gateway: { lockout_exempt: ["192.168.1.10", "10.0.0.0/8"] } })
    expect(
      buildNetworkPatch(
        { ...loaded, lockoutExemptText: "192.168.1.10" },
        loaded,
      ),
    ).toEqual({})
  })

  it("stores the device external address as https://host[:port]", () => {
    expect(
      buildNetworkPatch(
        { ...loaded, deviceExternalHost: "claw.example.com:42333" },
        loaded,
      ),
    ).toEqual({
      channels: { device: { external_url: "https://claw.example.com:42333" } },
    })
    expect(
      buildNetworkPatch({ ...loaded, deviceExternalHost: "  " }, loaded),
    ).toEqual({ channels: { device: { external_url: "" } } })
  })

  it("refuses a device external address with a scheme or a path, as a field error", () => {
    for (const bad of [
      "wss://claw.example.com",
      "https://claw.example.com:42333",
      "claw.example.com/ws",
    ]) {
      let caught: unknown
      try {
        buildNetworkPatch({ ...loaded, deviceExternalHost: bad }, loaded)
      } catch (err) {
        caught = err
      }
      expect(caught).toBeInstanceOf(NetworkFieldError)
      expect((caught as NetworkFieldError).field).toBe("deviceExternalHost")
      expect((caught as Error).message).toBe(DEVICE_HOST_ERROR)
    }
  })

  // A stored http:// or ws:// URL is shown as its host and left alone until
  // the operator edits the field; then it is saved back as https://.
  it("rewrites a stored http:// or ws:// device URL only once the field is edited", () => {
    const base = buildNetworkFormFromConfig({
      channels: { device: { external_url: "ws://claw.example.com:18791/" } },
    })
    expect(base.deviceExternalHost).toBe("claw.example.com:18791")
    expect(buildNetworkPatch(base, base)).toEqual({})
    expect(
      buildNetworkPatch({ ...base, deviceAutoApprove: true }, base),
    ).toEqual({ channels: { device: { auto_approve: true } } })
    expect(
      buildNetworkPatch(
        { ...base, deviceExternalHost: "claw.example.com:18792" },
        base,
      ),
    ).toEqual({
      channels: { device: { external_url: "https://claw.example.com:18792" } },
    })
  })

  it("refuses a bad value with a message for the operator", () => {
    expect(() =>
      buildNetworkPatch({ ...loaded, tlsPort: "18790" }, loaded),
    ).toThrow("HTTP port and HTTPS port must differ.")
    expect(() =>
      buildNetworkPatch({ ...loaded, devicePort: "abc" }, loaded),
    ).toThrow("Device gateway port")
  })
})

describe("buildNetworkFormFromConfig", () => {
  it("reads channels.device.tls, missing as off", () => {
    expect(loaded.deviceTLS).toBe(true)
    expect(buildNetworkFormFromConfig({}).deviceTLS).toBe(false)
    expect(buildNetworkFormFromConfig({})).toEqual(EMPTY_NETWORK_FORM)
  })

  it("shows the device external URL as host[:port] and the exempt list one per line", () => {
    expect(loaded.deviceExternalHost).toBe("ops42.example.com:42333")
    expect(loaded.lockoutExemptText).toBe("192.168.1.10")
  })
})

describe("device external address mapping", () => {
  it("strips https, http, wss and ws schemes and a trailing slash", () => {
    expect(deviceHostFromURL("https://claw.example.com:42333")).toBe(
      "claw.example.com:42333",
    )
    expect(deviceHostFromURL("http://10.0.0.5:18791/")).toBe("10.0.0.5:18791")
    expect(deviceHostFromURL("wss://claw.example.com/")).toBe(
      "claw.example.com",
    )
    expect(deviceHostFromURL("WS://claw.example.com")).toBe("claw.example.com")
    expect(deviceHostFromURL("claw.example.com")).toBe("claw.example.com")
    expect(deviceHostFromURL("")).toBe("")
  })

  it("adds https:// to a typed host and leaves blank blank", () => {
    expect(deviceURLFromHost("claw.example.com:42333")).toBe(
      "https://claw.example.com:42333",
    )
    expect(deviceURLFromHost(" 10.0.0.5 ")).toBe("https://10.0.0.5")
    expect(deviceURLFromHost("")).toBe("")
  })
})

describe("listenerChanged", () => {
  it("counts scopes, ports, the HTTPS mode and device TLS, not URLs or lists", () => {
    expect(listenerChanged(loaded, loaded)).toBe(false)
    expect(listenerChanged({ ...loaded, deviceTLS: false }, loaded)).toBe(true)
    expect(listenerChanged({ ...loaded, tlsPort: "18444" }, loaded)).toBe(true)
    expect(listenerChanged({ ...loaded, httpsMode: "off" }, loaded)).toBe(true)
    expect(
      listenerChanged({ ...loaded, externalUrl: "https://x.example" }, loaded),
    ).toBe(false)
    expect(listenerChanged({ ...loaded, allowedCIDRsText: "" }, loaded)).toBe(
      false,
    )
    expect(listenerChanged({ ...loaded, lockoutExemptText: "" }, loaded)).toBe(
      false,
    )
  })
})
