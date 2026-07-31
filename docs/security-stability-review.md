# Security & stability review

Review date: 2026-07-25. **No code was changed for this document** — it is a prioritized backlog of proposals.

## Scope

- Focus: core stability and security of the shared gateway HTTP surface, device gateway, credentials on disk, agent-loop resilience, and tool/channel blast radius.
- **Non-goal / do not change:** MCP host auth. `/mcp` (Bearer SST) and `/internal` (`session_token` tool arg) stay as designed. MCP remains loopback-oriented (`127.0.0.1:5911` by default).

## Current trust model

| Surface | Default bind | Network ACL | App-level auth | TLS |
|---------|--------------|-------------|----------------|-----|
| WebUI + `/api/*` + `/webui/ws` | `127.0.0.1:18790` | Loopback always; else `gateway.allowed_cidrs` (default RFC1918) | **None** for REST; chat WS has a channel token (see below) | None in-process |
| Device gateway WS | `127.0.0.1:18791` | Optional CIDRs (empty = any IP) | Shared / word / device token + Ed25519 + pairing | None in-process |
| MCP host | `127.0.0.1:5911` | Loopback by default | SST session token | None (leave as-is) |

Access control for the management port today is **bind address + CIDR allowlist only** (`internal/gateway/httphost.go`, `web/backend/middleware/access_control.go`). There is no operator password, session, or basic auth on `/api/*`. Documented in README and `docs/remote-access.md`.

Edge TLS (Cloudflare Tunnel, nginx, Tailscale) is the supported HTTPS path today; `gateway.external_url` advertises `https://…` to clients but does not terminate TLS inside ClawEh.

---

## WebSocket auth confirmation

### WebUI chat — `/webui/ws`

**Yes, the upgrade requires a token.** `WebUIChannel.handleWebSocket` rejects the connection unless `authenticate` succeeds (`pkg/channels/webui/webui.go`):

- `Authorization: Bearer <token>`, or
- `?token=` when `channels.webui.allow_token_query` is enabled.

**However, that token is not independent operator auth.** `GET /api/webui/token` returns the live WebUI channel token on the **unauthenticated** management API (`web/backend/api/webui.go`). Any peer that passes the CIDR allowlist can fetch the token and then open `/webui/ws`. Setup also tends to enable `AllowTokenQuery` and permissive origins.

**Verdict:** WebUI WS is token-gated at the socket, but the token is trivially obtainable once the management port is reachable. Password-protecting the WebUI/API closes this gap without changing the channel-token model itself.

### Device gateway — separate listener (default 18791)

**Real auth stack** (unchanged by WebUI password work):

1. Optional CIDR allowlist.
2. Shared gateway token and/or BIP39 `word_token` (constant-time compare), and/or issued device token.
3. Ed25519 device identity + signed challenge.
4. Pairing approval (unless `auto_approve`).

This is separate from the WebUI port by design. Do not weaken it when adding operator password on 18790.

### MCP

Out of scope. SST-based auth on `/mcp` and `/internal` must not change.

---

## Prioritized to-do list

### P0 — Critical / high (operator exposure)

- [ ] **WebUI / management password** — Add optional operator password so LAN peers past the CIDR allowlist cannot read/write config, mint tokens, or approve devices. See [Proposal: WebUI password](#proposal-webui-password).
- [ ] **Native HTTPS with cert paths + reload** — Allow pointing at existing cert/key files and hot-reload on change (Let’s Encrypt / certbot / acme.sh). See [Proposal: native HTTPS](#proposal-native-https).
- [ ] **Treat `GET /api/config` as secret** — Today it returns the full config **unmasked** (API keys, bot tokens, device tokens, WebUI token). Password + HTTPS are the primary fix; consider masking on GET (like `/api/providers`) as defense in depth.
- [ ] **Doc drift** — `docs/remote-access.md` still says WebUI and device gateway share port 18790; device gateway is a **separate** listener (default 18791). Fix when touching remote-access docs for TLS.

### P1 — Medium (stability / defense in depth)

- [ ] **Data-dir permissions** — Agents dirs often created `0755`; session/media files can be `0644`. Config is saved `0600` but load does not refuse world-readable configs. On startup: enforce `CLAW_HOME` `0700`, warn/refuse loose `config.json`, tighten session/token/DB files to `0600`.
- [ ] **Hot-reload races** — Some agent-loop paths read `al.cfg` / registry without the lock while reload swaps under `al.mu`. Always snapshot via `GetConfig()` / `GetRegistry()` (or hold RLock for the field read).
- [ ] **Reload UX** — Config reload stops/rebuilds services; in-flight turns can fail mid-tool. Drain or cancel with a clear “gateway reloading” outbound; add an integration test for a message mid-reload.
- [ ] **Device empty shared secrets** — When both shared token and word token are empty, gateway token auth is open (loopback-dev convenience). Refuse start when device is enabled unless an explicit insecure flag is set; keep pairing as a second gate, not the only one.
- [ ] **Device token length oracle** — `subtle.ConstantTimeCompare` short-circuits on length mismatch. Hash-then-compare or fixed-width compare for shared / word tokens.
- [ ] **WebUI chat defaults** — Setup forces `AllowTokenQuery=true` and often `AllowOrigins=["*"]`. Prefer Bearer-only by default; tokens in query strings leak via logs/Referer.
- [ ] **CIDR allowlist hot-reload** — Allowlist is fixed for the listener lifetime (`httphost.go`). Optional: re-apply without full process restart once operator auth exists.
- [ ] **HTTP `WriteTimeout: 30s`** on shared host — Fine after WS hijack; can still cut long non-WS responses. Raise or exempt streaming paths.

### P2 — Lower / operational footguns

- [ ] **Channel `allow_from: ["*"]`** — Empty allowlist is deny-all (good); wildcard plus discoverable bots is catastrophic with tools. Stronger first-run UX / WebUI warnings. Device channel rewriting empty → `*` is intentional for paired devices — keep documented.
- [ ] **CLI provider dangerous flags** — Defaults include vendor sandbox bypasses (`--dangerously-skip-permissions`, etc.). Document clearly; consider safer defaults for new installs.
- [ ] **`shell_exec` deny-regex** — Deny patterns are UX, not a sandbox. Remote exec remains gated (`allow_remote` default false). Keep documenting that.
- [ ] **External message API** — `POST /api/message/{token}` is token + rate-limit gated on the same no-auth port. After WebUI password, either require operator auth for minting tokens only (already enough if `/api` is gated) or add IP rate limits; leave injection path token-based. Do not change MCP.

### Already in reasonable shape (keep / monitor)

- [x] Panic recovery on message path, tool goroutines, HTTP middleware, subagents.
- [x] Fallback chain + timeouts (`request_timeout`, turn budget, spawnllm `ClassifyError` including auth/sign-in patterns as of spawnllm v0.1.8).
- [x] Workspace file tools restrict-by-default; write surface under workspace.
- [x] MCP loopback + SST identity; ACL on tool dispatch.

---

## Proposal: native HTTPS

**Goal:** Let the operator point ClawEh at **existing** certificate files and pick up renewals without a full gateway restart — compatible with Let’s Encrypt tooling (certbot, acme.sh, lego) that writes or atomically replaces files on disk.

### Config sketch

```json
{
  "gateway": {
    "host": "0.0.0.0",
    "port": 18790,
    "tls": {
      "enabled": true,
      "cert_file": "/etc/letsencrypt/live/claw.example.com/fullchain.pem",
      "key_file": "/etc/letsencrypt/live/claw.example.com/privkey.pem"
    },
    "external_url": "https://claw.example.com"
  }
}
```

- Paths may live outside `CLAW_HOME` (typical for system ACME layouts). ClawEh does **not** run ACME challenges itself in this proposal — renewal stays with the external tool.
- When `tls.enabled` is false or unset, behavior stays HTTP-only (current).
- Reverse-proxy / tunnel path remains supported and documented.

### Runtime behavior

1. Shared `httpHost` (`internal/gateway/httphost.go`) uses TLS when enabled (`ListenAndServeTLS` or `tls.NewListener` + `Serve`).
2. Build a `tls.Config` with `GetCertificate` that returns the currently loaded cert.
3. **Reload on change:** watch `cert_file` and `key_file` (mtime and/or fsnotify). On change, re-read both, parse with `tls.LoadX509KeyPair`, atomically swap the in-memory cert used by `GetCertificate`. Handles in-place overwrite and symlink retarget (common Let’s Encrypt layout).
4. Failed reload keeps the previous cert and logs an error — do not tear down the listener or agent stack.
5. Optional follow-on: same `tls` block for `channels.device` (separate listener). **MCP stays HTTP on loopback.**

### Compatibility notes

- Clients and `external_url` should use `https` / `wss` when TLS is on.
- Behind a reverse proxy that already terminates TLS, leave `gateway.tls.enabled` false and keep advertising `https://…` via `external_url`.
- Pair with WebUI password before binding `0.0.0.0` with a public cert: TLS encrypts the wire; it does not authenticate the operator by itself.

### Out of scope for this proposal

- Built-in ACME client / HTTP-01 challenge handler (can be a later optional feature).
- Changing MCP or device pairing crypto.

---

## Proposal: WebUI password

**Goal:** Optional password protection for the **management WebUI and `/api/*`**, so that network reachability (CIDR / bind) is no longer the only gate. MCP auth unchanged.

### Config sketch

```json
{
  "gateway": {
    "auth": {
      "password_hash": "$argon2id$..."
    }
  }
}
```

Prefer storing a **hash** (argon2id or bcrypt), not a plaintext password. Setting/rotating the password can be a CLI (`claw auth set-password`) or a one-time setup wizard field that writes the hash to config or an adjacent `0600` file under `CLAW_HOME`.

### Runtime behavior

1. IP allowlist remains first (`middleware.IPAllowlist`).
2. New middleware on the **shared mux** after the allowlist:
   - Protect: embedded static UI, `/api/*` (config, providers, devices, webui token, sessions, skills, …).
   - Leave open (recommended): `/health`, `/ready` if present; channel webhooks that use their own secrets/signatures (LINE, etc.) — document which paths are exempt.
3. Auth mechanism: HTTP Basic **or** session cookie after a login form. Cookie + HTTPS is preferred for browsers; Basic is simpler for scripts. Either is acceptable for v1 if documented.
4. WebUI **chat** channel token stays for `/webui/ws`. Once `/api/webui/token` is password-gated, LAN peers can no longer mint or read that token without the operator password.
5. Device gateway listener (18791) is **not** covered by this password — it already has its own credential + pairing model.
6. MCP listener is **not** covered — SST remains the only identity.

### UX / security notes

- Empty `password_hash` = current behavior (no operator auth), for backward compatibility on loopback installs.
- Require or strongly warn: password without TLS on a non-loopback bind still exposes credentials on the LAN — WebUI should surface that when `tls.enabled` is false and `host` is not loopback.
- Constant-time compare on password verification; rate-limit failed logins.
- Do not put the password in query strings.

### What this does *not* do

- Does not replace channel `allow_from` lists for Telegram/Slack/etc.
- Does not replace device pairing or MCP SST.
- Does not encrypt secrets at rest in `config.json` (separate hardening item under P1 permissions).

---

## Suggested implementation order

1. WebUI/API password + native HTTPS (cert paths + reload) together — closes the documented P0 for headless/VM/LAN use.
2. File permission hardening under `CLAW_HOME`.
3. Locked config/registry reads + safer reload UX.
4. Device empty-token fail-closed + token compare hardening.
5. WebUI query-token / origin defaults; remote-access doc fix.

---

## Reference map

| Topic | Primary locations |
|-------|-------------------|
| Shared HTTP host (no TLS today) | `internal/gateway/httphost.go` |
| CIDR allowlist | `web/backend/middleware/access_control.go`, `pkg/config/config.go` (`GatewayConfig.AllowedCIDRs`) |
| Unauthenticated config API | `web/backend/api/config.go` |
| WebUI WS token API | `web/backend/api/webui.go` |
| WebUI WS authenticate | `pkg/channels/webui/webui.go` |
| Device gateway auth | `pkg/channels/device/server.go`, `pkg/channels/device/gateway.go` |
| MCP auth (do not change) | `pkg/mcpserver/` |
| Remote access docs | `docs/remote-access.md` |
| Documented no-auth posture | `README.md` (security section) |
