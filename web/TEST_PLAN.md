# ClawEh Web UI — Test Plan

## Overview

This test plan covers manual and automated verification of the ClawEh web frontend and the WebUI HTTP layer that lives inside the merged `claw` binary. It is organized by feature area. Run against a locally running `claw` instance (`http://127.0.0.1:18790`).

## Prerequisites

- `claw` is running (`update-claw.sh`, `make install && claw`, or `go run ./cmd/claw`)
- A browser or Playwright session pointed at `http://127.0.0.1:18790`
- Because the gateway and the WebUI now run in the same process, the gateway
  is always available whenever the WebUI is reachable.

---

## 1. Branding & Identity

Verify all old "PicoClaw" / "picoclaw" references have been replaced.

| # | Test | Expected |
|---|------|----------|
| 1.1 | Page title in browser tab | "ClawEh" |
| 1.2 | App header / nav bar name | "ClawEh" |
| 1.3 | Any "About" or version display | "ClawEh" or no vendor name |
| 1.4 | Typing indicator text (if visible in chat) | "ClawEh" or agent name — not "PicoClaw" |
| 1.5 | Assistant message attribution | Not "picoclaw" |

---

## 2. Navigation & Links

| # | Test | Expected |
|---|------|----------|
| 2.1 | Click the Docs / Help link in the app header | Opens `https://github.com/PivotLLM/ClawEh` in a new tab — not `docs.picoclaw.io` |
| 2.2 | Open Channels config page → click docs link | Opens `https://github.com/PivotLLM/ClawEh` in a new tab |
| 2.3 | Inspect `<title>` in page source | `<title>ClawEh</title>` |

---

## 3. Configuration UI

| # | Test | Expected |
|---|------|----------|
| 3.1 | Open Settings → Agents → Workspace field placeholder | Shows `~/.claw/workspace` |
| 3.2 | Save a config change and reload | Config persists correctly |

---

## 4. OAuth / Credentials

OAuth is used to authenticate with providers that use browser-based or device-code login flows rather than plain API keys. Specifically:
- **Anthropic** (Claude) — browser OAuth or device code
- **OpenAI** — browser OAuth or device code
- **Google Antigravity** (Cloud Code Assist / Gemini) — browser OAuth

The flow opens a popup/new tab to the provider's auth page. When auth completes, the provider redirects to a local callback URL served by the merged `claw` binary. That callback page sends a `postMessage` with type `claw-oauth-result` back to the opener tab, closing the loop.

| # | Test | Expected |
|---|------|----------|
| 4.1 | Open Credentials page | Lists configured OAuth providers (Anthropic, OpenAI, Google Antigravity) |
| 4.2 | Inspect the OAuth callback HTML (GET `/api/oauth/callback` or trigger a flow) | Response `postMessage` type is `claw-oauth-result` — not `picoclaw-oauth-result` |
| 4.3 | Initiate a browser OAuth flow (if a provider is configured) | Popup opens; on return, credential status updates — no JS errors in console |
| 4.4 | Initiate a device-code flow | Device code sheet appears with code and verification URL |
| 4.5 | `localStorage` — no `picoclaw:` keys | `claw:last-session-id` key exists after first chat; no `picoclaw:` prefix keys |

---

## 5. Chat

| # | Test | Expected |
|---|------|----------|
| 5.1 | Navigate to Chat page | Chat UI loads without JS errors |
| 5.2 | Session ID persists across reload | `localStorage` key `claw:last-session-id` is set and restored |
| 5.3 | Send a message (requires running gateway) | Response appears; typing indicator shows |
| 5.4 | Session selector / history | Previous sessions listed correctly |

---

## 6. Gateway Control

The gateway lives in the same process as the WebUI, so /api/gateway/status
always reports "running" and /api/gateway/start is a no-op. /api/gateway/restart
is reserved for in-process config reload (the config-file watcher already
covers this path; see commit `515ed8f` for the merge).

| # | Test | Expected |
|---|------|----------|
| 6.1 | `GET /api/gateway/status` | `gateway_status: "running"` whenever `claw` is up |
| 6.2 | `CLAW_HOME` env var override | If set, `claw` uses that as the base directory |
| 6.3 | Save config via WebUI → `/api/config` | File on disk updates; in-process watcher triggers reload (no subprocess restart) |

---

## 7. Skills Browser

| # | Test | Expected |
|---|------|----------|
| 7.1 | Open Skills page | Local skills listed (if any installed) |
| 7.2 | Registry tab (if shown) | Only shown when `tools.skills.registry.enabled: true` in config |
| 7.3 | Install a local skill | Installs without error |

---

## 8. Autostart / Desktop Integration (Linux)

| # | Test | Expected |
|---|------|----------|
| 8.1 | Enable autostart in settings | Creates `~/.config/autostart/claw.desktop` |
| 8.2 | Desktop file contents | `Name=ClawEh`, `Exec=claw` (no args), launches the merged binary |
| 8.3 | Disable autostart | Removes `claw.desktop` |

---

## 9. Package Name & Build

| # | Test | Expected |
|---|------|----------|
| 9.1 | `cd web/frontend && pnpm build:backend` | Writes the bundle into `web/backend/dist/` for embedding |
| 9.2 | `make build` | Produces a single `claw` binary with the WebUI embedded |

---

## Notes

- Tests 4.2–4.5 and 6.x require a running `claw` instance.
- Tests 5.3 additionally requires a configured model so the agent loop can answer.
- Test 8.x is Linux-only; macOS uses a LaunchAgent plist instead.
- The `dist/index.html` in `web/backend/dist/` is a generated artifact from the last frontend build and may still contain old strings until the frontend is rebuilt (`cd web/frontend && pnpm build:backend`).
