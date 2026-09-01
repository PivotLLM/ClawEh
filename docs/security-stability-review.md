# Security & stability review

Review date: 2026-07-25. Status re-verified against the code on 2026-09-01.
**No code is changed by this document** — it is a prioritized backlog of proposals.

Every row in the summary below was re-checked against the current tree; two items
have since been closed and are marked as such with the reason.

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

**Yes, the upgrade requires a token.** `WebUIChannel.handleWebSocket` rejects the connection unless `authenticate` succeeds (`channels/webui/webui.go`):

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

## Summary

| # | Issue | Recommendation | Status |
|---|-------|----------------|--------|
| 1 | **No operator auth on the management port.** Reachability (bind + CIDR) is the only gate on the WebUI and `/api/*`. `GET /api/config` returns everything unmasked — API keys, bot tokens, device tokens, the WebUI token — and `GET /api/webui/token` hands the chat token to any peer that passes the allowlist, so the `/webui/ws` token gate is not independent auth. Message-token minting sits on the same surface. | Optional `gateway.auth.password_hash` (argon2id/bcrypt) enforced by middleware after the IP allowlist, covering the static UI and `/api/*`. Mask secrets on `GET /api/config` as defence in depth. See [Proposal: WebUI password](#proposal-webui-password). | Open |
| 2 | **No in-process TLS.** Edge termination (Cloudflare, nginx, Tailscale) is the only HTTPS path; `gateway.external_url` advertises `https://` without terminating it. | `gateway.tls.{enabled,cert_file,key_file}` with `GetCertificate` and mtime/fsnotify reload so ACME renewals are picked up live; a failed reload keeps the previous cert. Reverse-proxy path stays supported. See [Proposal: native HTTPS](#proposal-native-https). | Open |
| 3 | **WebUI chat transport defaults are permissive.** Setup force-enables `allow_token_query` and sets `allow_origins: ["*"]` (`web/backend/api/webui.go:88`). Tokens in query strings leak via logs and `Referer`. | Bearer-only by default, a concrete origin instead of `*`, and keep query-token strictly opt-in. | Open |
| 4 | **Data-dir permissions are not enforced.** Agent dirs are created `0755` and session/media files `0644`; config is written `0600` but load does not refuse a world-readable one. | At startup enforce `CLAW_HOME` `0700`, warn or refuse a loose `config.json`, and tighten session/token/DB files to `0600`. | Open |
| 5 | **Hot-reload is not concurrency-safe and has no drain.** Several agent-loop paths read `al.cfg` directly while reload swaps it under `al.mu`. Reload also stops and rebuilds services, so in-flight turns can fail mid-tool. | Read config and registry only through `GetConfig()` / `GetRegistry()` (or hold the RLock). Drain or cancel in-flight turns behind a clear "gateway reloading" outbound, and add an integration test for a message arriving mid-reload. | Open |
| 6 | **CIDR allowlist is fixed for the listener's lifetime** (`internal/gateway/httphost.go:31`), so an allowlist change needs a full restart. | Re-apply the allowlist on reload without restarting the process. Safe to do once #1 exists. | Open |
| 7 | **Device shared-token compare leaks length.** `subtle.ConstantTimeCompare` returns early on a length mismatch (`channels/device/server.go:395`), so it is constant-time only for equal-length inputs. | Hash-then-compare, or compare at fixed width, for the shared and word tokens. | Open |
| 8 | **Shared-host `WriteTimeout: 30s`** (`internal/gateway/httphost.go:39`) is fine after a WebSocket hijack but can still cut long non-WS responses. | Raise it, or exempt streaming paths. | Open |
| 9 | **Blast-radius footguns are documented, not enforced.** Channel `allow_from: ["*"]` plus a discoverable bot is catastrophic with tools; CLI provider defaults ship vendor sandbox bypasses (`--dangerously-skip-permissions`, `--dangerously-bypass-approvals-and-sandbox`, `--yolo`); `shell_exec` deny-regex is UX, not a sandbox. | Stronger first-run and WebUI warnings on wildcard `allow_from` and on enabling a CLI provider. Keep documenting that the real gates are the deny-regex plus `allow_remote: false`, not the regex alone. | Open |
| 10 | **Doc drift on ports.** `docs/remote-access.md` described the WebUI and device gateway as sharing port 18790. | — | **Closed** (`66e5eea`) |
| 11 | **Device gateway open when both shared secrets are empty.** | — | **Closed**. `EnsureProvisioned` now generates both the QR token and the BIP39 `word_token` when either is empty (`channels/device/provision.go:61-76`), and `authorizeGateway` guards each candidate with `secret != ""`, so an empty secret can never match — the path fails closed. |

Suggested order: **1 + 2 together** (they close the documented P0 for headless/LAN use and each is weaker alone), then **4**, then **5**, then **7**, then **3**.

### Notes on the grouping

- **#1 absorbs four originally separate entries** — the operator password, the unmasked
  `GET /api/config`, the `/api/webui/token` leak, and the external message API. They are one
  issue: nothing on port 18790 authenticates the operator. The message endpoint is already
  token-gated with per-token rate limiting and `Retry-After`
  (`internal/gateway/message_route.go:102`); what it lacks is a gate on *minting* tokens,
  which #1 provides.
- **#5 absorbs the locked-read and reload-UX entries** — both are the same reload path, and
  fixing the race without draining in-flight turns leaves the user-visible half of the
  problem in place.
- **#9 absorbs the three P2 footguns** — wildcard `allow_from`, CLI sandbox-bypass flags, and
  the `shell_exec` deny-regex. Each is a case where the safety story is documentation rather
  than a mechanism, so they share one recommendation.

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

## Reference map

| Topic | Primary locations |
|-------|-------------------|
| Shared HTTP host (no TLS today) | `internal/gateway/httphost.go` |
| CIDR allowlist | `web/backend/middleware/access_control.go`, `config/config.go` (`GatewayConfig.AllowedCIDRs`) |
| Unauthenticated config API | `web/backend/api/config.go` |
| WebUI WS token API | `web/backend/api/webui.go` |
| WebUI WS authenticate | `channels/webui/webui.go` |
| Device gateway auth | `channels/device/server.go`, `channels/device/gateway.go` |
| MCP auth (do not change) | `mcpserver/` |
| Remote access docs | `docs/remote-access.md` |
| Documented no-auth posture | `README.md` (security section) |
