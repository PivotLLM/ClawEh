# TLS

The WebUI and its API are served on two listeners that share one handler (the
login, the IP allowlist and the Host check apply to both):

| Listener | Where | Setting | Default |
|---|---|---|---|
| Plain HTTP | `gateway.port` (18790) | `gateway.host` | `127.0.0.1` and `[::1]`: this machine only |
| HTTPS | `gateway.tls_port` (18443) | `gateway.tls.mode` | `"all"`: every interface |

`gateway.tls.mode` is one of:

- `"all"` (the default, also what an absent key means): HTTPS on every
  interface, IPv4 and IPv6.
- `"localhost"`: HTTPS on `127.0.0.1` and `[::1]` only.
- `"off"`: no HTTPS listener.

`gateway.host` places plain HTTP only. Leave it at `127.0.0.1` and plain HTTP
never leaves the machine. Setting it to a LAN address or `0.0.0.0` serves the
WebUI over unencrypted HTTP on the network as well: passwords and session
cookies then cross the network in the clear, and the configuration report
marks it. Loopback is always bound, so `http://127.0.0.1:18790` keeps working
whatever `gateway.host` says.

Either way, off-box clients are refused until their network is in
`gateway.allowed_cidrs` (`claw network`), and every request needs the admin
login.

All of this can be changed from the WebUI, which checks the result
before saving; the listeners are bound at start, so a change to
`gateway.host`, `port`, `tls_port`, `tls.mode` or the certificate files takes
effect after a restart (the WebUI says so).

```json
"gateway": {
  "host": "127.0.0.1",
  "tls": { "mode": "localhost" }
}
```

## Reaching it by a host name

Browse to `https://<address>:18443/` (`claw status` and the WebUI's Network
page print the addresses; Docker's bridge interfaces — `docker0`, `br-…` —
are left out, since only containers on the machine can reach those), or
give the machine a name: set `gateway.external_url` to the URL you type, for
example `https://claw.lan:18443`. Its host is added to the self-signed
certificate and to the names the gateway answers to (anything else gets
`421 Misdirected Request`). Without `external_url` the advertised URL is
`https://<host name>:<tls_port>` for mode `"all"`, `https://127.0.0.1:<tls_port>`
for `"localhost"`, and the plain-HTTP URL when HTTPS is off.

## Self-signed (default)

With no certificate files configured the gateway generates
`<CLAW_HOME>/tls/self-signed.crt` and `self-signed.key` (mode 0600) the first
time the HTTPS listener starts: ECDSA P-256, valid ten years, for the machine's
host name, its FQDN, every non-loopback interface address, the host of
`gateway.external_url`, any `gateway.tls.extra_names`, and `localhost`,
`127.0.0.1` and `::1`. It is regenerated automatically within 30 days of
expiry or when those names change at start, and on demand with the WebUI's
**Regenerate** button or `claw tls --regenerate`. Names saved in the WebUI
(`extra_names`, `external_url`) are used by the next regeneration without a
restart.

Your browser warns once about the certificate. Compare the SHA-256 fingerprint
it shows with the one `claw tls` (or the WebUI) prints before accepting it. To
cover a DNS alias or a NAT address, add it to `extra_names`:

```json
"gateway": {
  "tls": { "extra_names": ["claw.home.arpa", "203.0.113.5"] }
}
```

## Your own certificate

```json
"gateway": {
  "tls": {
    "cert_file": "/etc/letsencrypt/live/claw.example.com/fullchain.pem",
    "key_file":  "/etc/letsencrypt/live/claw.example.com/privkey.pem"
  }
}
```

Both keys must be set (PEM, absolute paths; the files may live anywhere the
service user can read). The WebUI checks a pair before saving it — both files
readable by the service user, the key matching the certificate, the
certificate currently valid — and refuses to save one the gateway could not
start on. The gateway checks the files every minute and swaps a renewed pair
in without a restart, so a certbot or acme.sh hook only has to write the
files; symlinks are followed. A pair that fails to load is ignored: the
previous certificate keeps serving and the "TLS certificate reload failed"
alert is raised. Alerts also fire 14 and 3 days before an operator-supplied
certificate expires. `Strict-Transport-Security` is sent only with your own
certificate, never with the self-signed one, so a browser is never locked out
of a self-signed install.

## Other listeners

- The MCP host (`mcp_host.listen`) must stay on a loopback address; the gateway
  refuses to start otherwise. The CLIs that use it connect locally.
- The device gateway (`channels.device`, port 18791) has its own token and
  Ed25519 pairing authentication and is plain WebSocket by default. Set
  `channels.device.tls: true` to serve `wss://` on the same port with the
  certificate described above (it needs `gateway.tls.mode` other than `"off"`);
  the pairing QR code and `claw status` then advertise `wss://`.
- The LINE webhook is a path on the WebUI/API listeners, so off-box it is
  served over HTTPS. LINE will not accept a self-signed certificate: use your
  own, or a reverse proxy (see `docs/remote-access.md`).

## Inspecting

`claw status` prints the URLs to open (localhost HTTP, network HTTP when
`gateway.host` is not loopback, HTTPS per `gateway.tls.mode`), the
certificate's expiry and fingerprint, and whether an admin account exists.
`claw tls` prints the certificate in full: source (self-signed or file),
subject, names, expiry and SHA-256 fingerprint. The fingerprint is also logged
at startup, shown in the WebUI and in the configuration report.

The WebUI uses `GET /api/tls` (settings, URLs, certificate, whether a restart
is pending), `POST /api/tls/validate` (check a certificate pair without
saving) and `POST /api/tls/regenerate` (replace the self-signed certificate on
the running listener); all require the login.
