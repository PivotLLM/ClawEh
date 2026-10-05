// API client for the HTTPS listener's certificate (GET /api/tls and the
// validate / regenerate actions the Network page offers).

export interface TLSCertificate {
  present: boolean
  subject: string
  names: string[]
  /** RFC 3339 expiry. */
  not_after: string
  /** SHA-256 fingerprint as `claw tls` prints it. */
  fingerprint: string
  self_signed: boolean
  /** Why an existing certificate file could not be read (present is false). */
  error?: string
}

export interface TLSStatus {
  /** gateway.tls.mode: all | localhost | off. */
  mode: string
  /** Where the certificate comes from. */
  source: "self-signed" | "file"
  cert_file: string
  key_file: string
  extra_names: string[]
  tls_port: number
  http_host: string
  http_port: number
  external_url: string
  urls: {
    localhost: string
    /** Plain HTTP off loopback: only when gateway.host is a network address. */
    http: string[]
    https: string[]
  }
  /** present is false until the HTTPS listener has produced or loaded one. */
  certificate: TLSCertificate | null
  /** A listener setting changed since start; the running listeners differ. */
  restart_required: boolean
}

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const res = await fetch(path, options)
  if (!res.ok) {
    let message = `API error: ${res.status} ${res.statusText}`
    try {
      const body = (await res.json()) as { error?: string }
      if (typeof body.error === "string" && body.error.trim() !== "") {
        message = body.error
      }
    } catch {
      // keep fallback
    }
    throw new Error(message)
  }
  return res.json() as Promise<T>
}

const jsonPost = (body: unknown): RequestInit => ({
  method: "POST",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify(body),
})

// getTLS normalises the lists at the boundary: Go encodes an empty slice as
// null, and the page maps over them.
export async function getTLS(): Promise<TLSStatus> {
  const data = await request<Partial<TLSStatus>>("/api/tls")
  const cert = data.certificate
  return {
    mode: data.mode ?? "all",
    source: data.source === "file" ? "file" : "self-signed",
    cert_file: data.cert_file ?? "",
    key_file: data.key_file ?? "",
    extra_names: data.extra_names ?? [],
    tls_port: data.tls_port ?? 18443,
    http_host: data.http_host ?? "127.0.0.1",
    http_port: data.http_port ?? 18790,
    external_url: data.external_url ?? "",
    urls: {
      localhost: data.urls?.localhost ?? "",
      http: data.urls?.http ?? [],
      https: data.urls?.https ?? [],
    },
    certificate: cert ? { ...cert, names: cert.names ?? [] } : null,
    restart_required: data.restart_required === true,
  }
}

// validateTLSFiles asks the server to load a certificate/key pair without
// saving anything. A 400 carries the reason, surfaced as the thrown message.
export const validateTLSFiles = (files: {
  cert_file: string
  key_file: string
}) => request<TLSCertificate>("/api/tls/validate", jsonPost(files))

// regenerateTLS replaces the self-signed certificate and returns the new one.
export const regenerateTLS = () =>
  request<TLSCertificate>("/api/tls/regenerate", { method: "POST" })
