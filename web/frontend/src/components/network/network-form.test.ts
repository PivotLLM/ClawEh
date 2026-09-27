import { describe, expect, it } from "vitest"

import {
  EMPTY_NETWORK_FORM,
  type NetworkForm,
  buildNetworkFormFromConfig,
  buildNetworkPatch,
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
    tls: { mode: "all", extra_names: ["claw.home.arpa"] },
  },
  channels: { device: { host: "0.0.0.0", port: 18791, tls: true } },
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
  })
})
