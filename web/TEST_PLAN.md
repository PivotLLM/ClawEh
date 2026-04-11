# ClawEh Web UI — Test Plan

## Overview

This test plan covers manual and automated verification of the ClawEh web frontend and backend launcher. It is organized by feature area. Run against a locally running `claw-web` instance (`http://127.0.0.1:18800`).

## Prerequisites

- `claw-web` is running (`update-claw.sh` or `cd web/backend && ./claw-web`)
- A browser or Playwright session pointed at `http://127.0.0.1:18800`
- The claw gateway may or may not be running; tests note where it matters

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

The flow opens a popup/new tab to the provider's auth page. When auth completes, the provider redirects to a local callback URL served by `claw-web`. That callback page sends a `postMessage` with type `claw-oauth-result` back to the opener tab, closing the loop.

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

| # | Test | Expected |
|---|------|----------|
| 6.1 | Start gateway from web UI | Gateway starts; status shows running |
| 6.2 | `CLAW_BINARY` env var override | If set, `claw-web` uses that binary path |
| 6.3 | `CLAW_HOME` env var override | If set, `claw-web` uses that as the base directory |
| 6.4 | Stop gateway from web UI | Gateway stops cleanly |

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
| 8.1 | Enable autostart in settings | Creates `~/.config/autostart/claw-web.desktop` |
| 8.2 | Desktop file contents | `Name=ClawEh Web`, `Exec=claw-web`, `Icon=claw-web` |
| 8.3 | Disable autostart | Removes `claw-web.desktop` |

---

## 9. Package Name & Build

| # | Test | Expected |
|---|------|----------|
| 9.1 | `cat web/frontend/package.json \| grep name` | `"name": "claw-web"` |
| 9.2 | Build frontend: `cd web && make` | Output binary named `claw-web` |
| 9.3 | `.goreleaser.yaml` build IDs | `claw`, `claw-launcher`, `claw-launcher-tui` |

---

## Notes

- Tests 4.2–4.5 and 6.x require a running `claw-web` instance.
- Tests 5.3 additionally requires a configured and running claw gateway.
- Test 8.x is Linux-only; macOS uses a LaunchAgent plist instead.
- The `dist/index.html` in `web/backend/dist/` is a generated artifact from the last frontend build and may still contain old strings until the frontend is rebuilt (`cd web && make`).
