# TLS

ClawEh serves plain HTTP on loopback only (`127.0.0.1:18790` and
`[::1]:18790`). Set `gateway.host` to a LAN address or `0.0.0.0` and it also
serves HTTPS on `gateway.host:gateway.tls_port` (default `18443`). Plain HTTP is
never served off-box, and there is no switch to turn HTTPS off. The only choice
is where the certificate comes from.

## Self-signed (default)

With no `gateway.tls` block the gateway generates
`<CLAW_HOME>/tls/self-signed.crt` and `self-signed.key` (mode 0600) the first
time the HTTPS listener starts: ECDSA P-256, valid one year, for the machine's
host name, its FQDN, every non-loopback interface address, the host of
`gateway.external_url` and any `gateway.tls.extra_names`. It is regenerated
automatically within 30 days of expiry or when those names change, and on
`claw tls --regenerate`.

Your browser warns once about the certificate. Compare the SHA-256 fingerprint
it shows with the one `claw tls` prints before accepting it. To cover a DNS
alias or a NAT address, add it to `extra_names`:

```json
"gateway": {
  "host": "0.0.0.0",
  "tls": { "extra_names": ["claw.home.arpa", "203.0.113.5"] }
}
```

## Your own certificate

```json
"gateway": {
  "host": "0.0.0.0",
  "tls": {
    "cert_file": "/etc/letsencrypt/live/claw.example.com/fullchain.pem",
    "key_file":  "/etc/letsencrypt/live/claw.example.com/privkey.pem"
  }
}
```

Both keys must be set (PEM; the files may live anywhere the service user can
read). The gateway checks the files every minute and swaps a renewed pair in
without a restart, so a certbot or acme.sh hook only has to write the files;
symlinks are followed. A pair that fails to load is ignored: the previous
certificate keeps serving and the "TLS certificate reload failed" alert is
raised. Alerts also fire 14 and 3 days before an operator-supplied certificate
expires. `Strict-Transport-Security` is sent only with your own certificate,
never with the self-signed one, so a browser is never locked out of a
self-signed install.

## Other listeners

- The MCP host (`mcp_host.listen`) must stay on a loopback address; the gateway
  refuses to start otherwise. The CLIs that use it connect locally.
- The device gateway (`channels.device`, port 18791) is plain WebSocket with its
  own token and Ed25519 pairing authentication.
- The LINE webhook is a path on the gateway listener, so off-box it is served
  over HTTPS. LINE will not accept a self-signed certificate: use your own, or
  a reverse proxy (see `docs/remote-access.md`).

## Inspecting

`claw status` prints the URLs to open (loopback HTTP, network HTTPS), the
certificate's expiry and fingerprint, and whether an admin account exists.
`claw tls` prints the certificate in full: source (self-signed or file),
subject, names, expiry and SHA-256 fingerprint. The fingerprint is also logged
at startup and shown in the configuration report.

Changing `gateway.host`, `port`, `tls_port` or `tls.*` needs a gateway restart;
listeners are bound at start.
