// The Network page's editable copy of the listener settings: gateway.* (the
// WebUI's HTTP and HTTPS listeners) and channels.device.* (the device gateway).
// The certificate files are not here — they are saved by their own button,
// after the server has validated them.
import { parseCIDRText, parseIntField } from "@/components/config/form-model"

export type HttpScope = "localhost" | "network"
export type HttpsMode = "all" | "localhost" | "off"

export interface NetworkForm {
  httpScope: HttpScope
  httpPort: string
  httpsMode: HttpsMode
  tlsPort: string
  externalUrl: string
  tlsExtraNames: string[]
  allowedCIDRsText: string
  deviceScope: HttpScope
  devicePort: string
  deviceExternalUrl: string
  deviceAllowedCIDRsText: string
  deviceAutoApprove: boolean
  /** mcp_host.listen, shown read-only. */
  mcpListen: string
}

export const DEFAULT_HTTP_PORT = 18790
export const DEFAULT_TLS_PORT = 18443
export const DEFAULT_DEVICE_PORT = 18791

export const EMPTY_NETWORK_FORM: NetworkForm = {
  httpScope: "localhost",
  httpPort: String(DEFAULT_HTTP_PORT),
  httpsMode: "all",
  tlsPort: String(DEFAULT_TLS_PORT),
  externalUrl: "",
  tlsExtraNames: [],
  allowedCIDRsText: "",
  deviceScope: "localhost",
  devicePort: String(DEFAULT_DEVICE_PORT),
  deviceExternalUrl: "",
  deviceAllowedCIDRsText: "",
  deviceAutoApprove: false,
  mcpListen: "127.0.0.1:5911",
}

type JsonRecord = Record<string, unknown>

function asRecord(value: unknown): JsonRecord {
  if (value && typeof value === "object" && !Array.isArray(value)) {
    return value as JsonRecord
  }
  return {}
}

function asString(value: unknown): string {
  return typeof value === "string" ? value : ""
}

function asStringArray(value: unknown): string[] {
  if (!Array.isArray(value)) return []
  return value.filter((v): v is string => typeof v === "string")
}

function asPortString(value: unknown, fallback: string): string {
  return typeof value === "number" && value > 0 ? String(value) : fallback
}

// Loopback is what an unset host means; anything else is a network bind.
export function scopeOfHost(host: string): HttpScope {
  const h = host.trim().toLowerCase()
  return h === "" || h === "127.0.0.1" || h === "localhost" || h === "::1"
    ? "localhost"
    : "network"
}

export function hostOfScope(scope: HttpScope): string {
  return scope === "network" ? "0.0.0.0" : "127.0.0.1"
}

function asHttpsMode(value: unknown): HttpsMode {
  return value === "localhost" || value === "off" ? value : "all"
}

export function buildNetworkFormFromConfig(config: unknown): NetworkForm {
  const root = asRecord(config)
  const gateway = asRecord(root.gateway)
  const tls = asRecord(gateway.tls)
  const device = asRecord(asRecord(root.channels).device)
  const mcp = asRecord(root.mcp_host)
  return {
    httpScope: scopeOfHost(asString(gateway.host)),
    httpPort: asPortString(gateway.port, EMPTY_NETWORK_FORM.httpPort),
    httpsMode: asHttpsMode(tls.mode),
    tlsPort: asPortString(gateway.tls_port, EMPTY_NETWORK_FORM.tlsPort),
    externalUrl: asString(gateway.external_url),
    tlsExtraNames: asStringArray(tls.extra_names),
    allowedCIDRsText: asStringArray(gateway.allowed_cidrs).join("\n"),
    deviceScope: scopeOfHost(asString(device.host)),
    devicePort: asPortString(device.port, EMPTY_NETWORK_FORM.devicePort),
    deviceExternalUrl: asString(device.external_url),
    deviceAllowedCIDRsText: asStringArray(device.allowed_cidrs).join("\n"),
    deviceAutoApprove: device.auto_approve === true,
    mcpListen: asString(mcp.listen) || EMPTY_NETWORK_FORM.mcpListen,
  }
}

// buildNetworkPatch validates the form and returns the JSON merge patch for
// PATCH /api/config. It throws a message for the operator on a bad value.
export function buildNetworkPatch(form: NetworkForm): Record<string, unknown> {
  const port = parseIntField(form.httpPort, "HTTP port", { min: 1, max: 65535 })
  const tlsPort = parseIntField(form.tlsPort, "HTTPS port", {
    min: 1,
    max: 65535,
  })
  if (port === tlsPort) {
    throw new Error("HTTP port and HTTPS port must differ.")
  }
  const devicePort = parseIntField(form.devicePort, "Device gateway port", {
    min: 1,
    max: 65535,
  })
  return {
    gateway: {
      host: hostOfScope(form.httpScope),
      port,
      tls_port: tlsPort,
      external_url: form.externalUrl.trim(),
      allowed_cidrs: parseCIDRText(form.allowedCIDRsText),
      tls: {
        mode: form.httpsMode,
        extra_names: form.tlsExtraNames
          .map((n) => n.trim())
          .filter((n) => n.length > 0),
      },
    },
    channels: {
      device: {
        host: hostOfScope(form.deviceScope),
        port: devicePort,
        external_url: form.deviceExternalUrl.trim(),
        allowed_cidrs: parseCIDRText(form.deviceAllowedCIDRsText),
        auto_approve: form.deviceAutoApprove,
      },
    },
  }
}

// listenerChanged reports whether a save alters something the listeners are
// bound with at start, which is what needs a restart to take effect.
export function listenerChanged(a: NetworkForm, b: NetworkForm): boolean {
  return (
    a.httpScope !== b.httpScope ||
    a.httpPort !== b.httpPort ||
    a.httpsMode !== b.httpsMode ||
    a.tlsPort !== b.tlsPort ||
    a.deviceScope !== b.deviceScope ||
    a.devicePort !== b.devicePort
  )
}
