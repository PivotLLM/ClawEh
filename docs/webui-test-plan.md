# WebUI test plan

Regression coverage for the ClawEh web interface. Every step below has an ID, a
process, and an expected result, so it can be followed by hand — and every one is
also automated in `tests/frontend-e2e.mjs`, which prints the same IDs. There are
146 checks in all; the runner prints the same tally at the end.

```
export CLAW_E2E_USER=<admin>  CLAW_E2E_PASSWORD=<password>
node tests/frontend-e2e.mjs                      # whole plan
node tests/frontend-e2e.mjs --only F,J           # selected groups
node tests/frontend-e2e.mjs --base http://host:port
```

## Before you start

**Run it against a dev instance, never production.** Groups F, G, N and R
write. All four revert what they change — the agent created in F is deleted, the
field edited in G and the HTTPS port edited in R are restored to the values read
beforehand, and the memory domain N creates is deleted at the end — but a crash
mid-run would leave the change behind. N only ever touches the `e2e-probe`
domain it created, so an agent's real memory is not at risk, but it is still a
write. R8 regenerates the self-signed certificate (a new self-signed one takes
its place; browsers that accepted the old one warn again) and is skipped with a
note when the instance uses its own certificate or has HTTPS off. The runner
refuses port 18790 unless `--allow-prod` is passed.

**HTTPS on the dev instance.** Group R reads the certificate through
`GET /api/tls` whatever the listener state, but R8 (regenerate) only runs with
HTTPS on (`gateway.tls.mode` not `off`, a self-signed certificate, and the
listener started at least once so a certificate exists). R5 and R9 report what
is there either way. R10 expects no restart to be pending when it starts, so
restart the dev instance after changing listener settings by hand.

| Requirement | Notes |
|---|---|
| A running ClawEh | `make build && cp build/claw ~/bin/claw && sudo systemctl restart claw-dev` |
| An admin account | The WebUI and `/api/*` are behind a login. Create the account on the dev instance with `claw admin`, then `export CLAW_E2E_USER=… CLAW_E2E_PASSWORD=…`. The runner signs in first and stops with that instruction if it cannot |
| At least one agent, model and provider | The plan asserts against live data; an empty install fails A3 |
| Playwright + Chromium | Found in the npx cache under the home directory; override with `PLAYWRIGHT_MODULE` / `CHROME_PATH` |

**Every request below carries the session.** The runner keeps one browser
context, so the cookie from `POST /api/auth/login` goes with every page it opens
and every API probe it makes. Following the plan by hand, sign in once with a
cookie jar and pass it to each `curl`:

```
curl -c jar -H 'Content-Type: application/json' \
     -d '{"username":"<admin>","password":"<password>"}' $BASE/api/auth/login   # 204
curl -b jar $BASE/api/config
```

Only `/health`, `/ready` and the three `/api/auth/*` endpoints answer without
it; anything else under `/api/*` is `401`. Where a step says **anonymous**, run
it without the jar.

**Wait for startup before testing.** `/health` answers as soon as the listener
binds; `/ready` only answers 200 once the channels are up. Poll `/ready`, not
`/health`, or early steps race the boot:

```
until curl -sf http://127.0.0.1:8077/ready >/dev/null; do sleep 1; done
```

---

## A. Preconditions

| ID | Process | Expected |
|---|---|---|
| A1 | `curl -i $BASE/health` | `200`, `{"status":"ok"}` |
| A2 | `curl $BASE/api/system/version` | Matches `<semver>+<8 hex> [<14 digits>]`, e.g. `0.4.72+7a8a1d68 [20260905065403]`. A missing `+commit` means the binary was built with plain `go build` instead of `make build`; a missing `[build]` means the same |
| A3 | `curl $BASE/api/config` | `agents.list` is a non-empty array |

## B. Route smoke

**Process.** Load each of the 22 routes in a browser with the console open:
`/`, `/agents`, `/audit`, `/agent/bindings`, `/agent/tools`, `/agent/skills`,
`/channels`, `/config`, `/config/raw`, `/devices`, `/logs`, `/mcp`,
`/mcp/servers`, `/memory`, `/models`, `/network`, `/providers`, `/report`,
`/system`, `/voice`, `/setup`, `/status`.

**Expected.** Each renders substantive content (>40 characters of text) and logs
**no console errors**. A blank page or a red console entry is a failure.

## C. Shell and navigation

| ID | Process | Expected |
|---|---|---|
| C1 | Look at the sidebar | Contains Chat, Agents, Models, Channels, Services. Chat is a direct link (`a[href="/"]`), not a disclosure button |
| C2 | Look at the sidebar footer | Shows `ClawEh v<version>` |
| C3 | Click the **Models** sidebar entry, then the **Models** link beneath it | The entry is a disclosure control (`aria-expanded`), not a link. It expands, and the link navigates to `/models` without a full page load |
| C4 | `curl -o /dev/null -w '%{http_code}' $BASE/no-such-route` | `200` — unknown paths fall through to the SPA |

## D. i18n integrity

| ID | Process | Expected |
|---|---|---|
| D1 | Load all 22 routes; scan the rendered text for anything shaped like a translation key (`pages.…`, `navigation.…`), skipping the log viewer itself (`role="log"`) because log lines legitimately name config keys such as `agents.defaults.models` | None found. i18next renders the key verbatim when a lookup fails, so a leaked key is the only visible symptom of a broken locale |
| D2 | Load `/agent/tools` | No heading reads `…categories.<name>`. Tool categories come from the backend catalog; a category with no label in `en.json` shows as a raw key |

## E. Chat and WebSocket auth

The chat socket is authenticated by the login session cookie, which the browser
attaches on its own. No token is fetched or sent by the page.

| ID | Process | Expected |
|---|---|---|
| E1 | `curl -b jar $BASE/api/webui/token` | `404`. This endpoint handed the chat token to any peer inside the CIDR allowlist, which made the `/webui/ws` gate no gate at all; it was removed with the login and must not come back |
| E2 | Load `/`, inspect the WebSocket the page opens, then **anonymous** `curl -o /dev/null -w '%{http_code}' $BASE/webui/ws` | The socket URL is `ws(s)://<page host>/webui/ws?session_id=…` — the page's own origin, **no `token=` in the URL** (a query-string token is recorded by proxies, access logs, Referer headers and browser history) and **no subprotocols**. The anonymous request is `401`: the cookie is the gate |
| E3 | Load `/` and wait ~2s | Chat does not report "disconnected" or a connection error |

## F. Agents — autosave and list realignment

Creates an agent called `e2e-probe` and deletes it at the end.

| ID | Process | Expected |
|---|---|---|
| F1 | `/agents` → **Add Agent** → ID `e2e-probe` → **Add** | The agent appears in `GET /api/config` |
| F2 | Select it, set **Temperature** to `0.77`, wait ~2s | `temperature: 0.77` persisted. Saves are debounced ~600 ms; reading back immediately will race |
| F3 | Under **Internal tools**, toggle the `time_now` checkbox, wait ~2s | `tools` persisted with `time_now` added (or removed, if it was on) **and** `temperature` is still `0.77`. The second save must not clobber the first |
| F4 | Select `e2e-probe`, note its temperature; select another agent, note its temperature | `e2e-probe` shows `0.77`; the other agent does not. Adding an agent re-sorts the list and shifts every index, so the edit buffers must follow. **Select agents by their displayed name** — the rail shows `name`, falling back to `id` |
| F5 | Under **Fusion services** on the `e2e-probe` card, tick the first listed service, wait ~2s; tick it again, wait ~2s | `mcp_tools` gains the service name on the first tick and loses it on the second; the earlier edits are untouched. Skipped when the instance defines no Fusion service (`GET /api/agents/tools` has no `fusion_services`) |
| F6 | With `e2e-probe` selected, turn on the **Allow shell commands** switch under Tools, wait ~2s; turn it off, wait ~2s | `tools` gains `shell_exec` when it is turned on and loses it when turned off; every other `tools` entry and the earlier edits are unchanged. It is the only way to grant `shell_exec`: `"*"` does not include it |
| F7 | Open `/agents`, leave through the **Check Up** sidebar link, come back through the **Agents** sidebar link (no reload) | The rail lists the agents and the page does not say "No agents yet". A return visit mounts the page with its data already cached; it used to seed its list only when a new fetch landed, so the cached list never showed until a browser reload |
| F8 | Through the API, set `e2e-probe`'s models to a CLI model whose `extra_args` carries the CLI's bypass flag while that provider's "Allow CLI to bypass restrictions" is off (skip with a note when no such model exists); select `e2e-probe` on `/agents` | Under **Models** one line per CLI reads "`<CLI>` is not allowed to bypass its restrictions." followed by an **Allow it** link to the Providers page |
| F9 | Through the API add twenty temporary enabled models; select an agent, open **Add model…**, scroll the list with the wheel twice; remove the models again | The list shows a scrollbar (Radix hides it by default, which made a long list look cut off), scrolls, does not jump back, and the popup keeps its height throughout. In Radix Select's item-aligned mode the popup grows and the scroll position is rewritten on every scroll event, which on a phone flickers and snaps back to the top on release; every select uses popper mode |
| F10 | With `e2e-probe` selected, click the trash button in the card header, accept the confirmation | The agent is gone from `GET /api/config` |

## G. System page

The Config page was split: listeners are on **Network** (group R), everything
else is on **System**. `/config` still works and lands on `/system`;
`/config/raw` is unchanged.

| ID | Process | Expected |
|---|---|---|
| G1 | Load `/system` | Sections Agent defaults, Context management, Runtime, Backup and Devices all render; there is **no** listener section (no *Allowed network CIDRs*) — a second writer of `gateway.*` here would fight the Network page. No console errors |
| G2 | Note the current **Backup destination**, change it to `/tmp/e2e-probe-backup`, wait ~2s | The new value is in `backup.dest` |
| G3 | Restore the original value, wait ~2s | `backup.dest` matches what G2 recorded |
| G4 | Load `/config/raw` | The configuration document renders |
| G5 | Load `/config` | The browser lands on `/system` and the System page renders (Backup section present), no console errors |

## R. Network page

Everything about listeners: the WebUI's HTTP and HTTPS listeners, the
certificate, the IP allowlist, the device listener and the read-only
MCP host address. `GET /api/tls` reports the listener and certificate state;
`POST /api/tls/validate` loads a certificate/key pair without saving it;
`POST /api/tls/regenerate` replaces the self-signed certificate. General fields
autosave like the rest of the WebUI, each change as a `PATCH /api/config`
carrying only the field that changed (text ~0.6 s after the last keystroke, a
radio or checkbox at once); the certificate has its own buttons. A change to a
bound address, port, the HTTPS mode or device TLS shows a **Restart required to
apply changes.** banner with a **Restart now** button (`POST /api/system/restart`).
R8 and R10 write (see *Before you start*); R8 needs HTTPS on. R15 types into
a field but is refused before anything is sent. Nothing in this group clicks
**Restart now**.

| ID | Process | Expected |
|---|---|---|
| R1 | Load `/network` | Every control is present, once: HTTP **Port** and **Scope** radios (*Localhost only (default)* / *Network*), HTTPS **Listen on** radios (*All interfaces (default)* / *Localhost only* / *Off*), **HTTPS port**, **Hostname / external URL**, **Extra certificate names**, the certificate card with its *Self-signed (default)* / *External certificate* choice, **Allowed network CIDRs**, **Never locked out** and **Trusted proxies**, the device listener's **Protocol** radios (*ws (unencrypted)* / *wss (HTTPS)*), scope radios, port, **External address**, CIDRs and **Auto-approve pairings** switch, the MCP **Listen address** and the address list. There is no Save button. Eleven radios in all. No console errors |
| R2 | Compare the HTTP **Scope** radio and **Port** with `gateway.host` / `gateway.port` | *Network* is selected when the host is `0.0.0.0` (or any non-loopback address), *Localhost only* otherwise; the port field shows `gateway.port` (18790 by default). A warning line under *Network* reads *Exposing plain-text HTTP to the network is not recommended.* |
| R3 | Compare the HTTPS **Listen on** radio and **HTTPS port** with `gateway.tls.mode` / `gateway.tls_port` | Exactly the configured mode is selected (`all` when unset); the port field shows `gateway.tls_port` (18443 by default) |
| R4 | Compare **Hostname / external URL** with `gateway.external_url` | Equal (blank when unset) |
| R5 | `curl -b jar $BASE/api/tls`, then read the certificate card | With `certificate.present`, the card shows source, subject, names, expiry and the **SHA-256 fingerprint**, equal to the API's; otherwise it reads *Certificate not generated* and shows no fingerprint |
| R6 | Click *Self-signed (default)*, then *External certificate* | Under Self-signed: no path inputs, a **Regenerate certificate** button. Under External: **Certificate file** and **Private key file** inputs and a **Save certificate** button, no Regenerate |
| R7 | Under External, enter `/nonexistent/e2e-probe/fullchain.pem` and `/nonexistent/e2e-probe/privkey.pem`, **Save certificate** | An inline error with the server's reason (`POST /api/tls/validate` answered 400). `GET /api/config` → `gateway` is byte-for-byte what it was before: nothing was saved |
| R8 | With a self-signed certificate and HTTPS on, click **Regenerate certificate** (skip with a note otherwise) | The fingerprint shown changes, and `GET /api/tls` reports the new one |
| R9 | Read the **Addresses** card | Lists `urls.localhost`, every `urls.http[]` and every `urls.https[]` from `GET /api/tls` — exactly what to open. Each plain-HTTP network address, and nothing else, carries a warning triangle whose hover text is *Plain-text HTTP exposed to network.* No Docker bridge address (`172.17–31.x.0.1`) is listed. With HTTPS off it says ClawEh is reachable from this host only |
| R10 | Note `gateway.tls_port`; set **HTTPS port** to the next free number and wait for *Saved ✓*; then set it back and wait for the save | After the first save a banner reading exactly **Restart required to apply changes.** appears with a **Restart now** button beside it (do **not** click it) and `gateway.tls_port` holds the probe value; after the second save the config holds the original again. The banner is expected to stay: the running listener still differs until a restart. The step expects no banner before it starts |
| R11 | Compare **Allowed network CIDRs** with `gateway.allowed_cidrs` | One entry per line, in order (empty for loopback only) |
| R12 | Compare the **Device listener** section with `channels.device` | The **Protocol** radio (*ws (unencrypted)* / *wss (HTTPS)*) is the first control of the section and shows `tls` (*wss* exactly when `true`); the scope radio matches `host` (loopback ↔ *Localhost only*), the port field shows `port` (18791 by default), the **External address** field shows `external_url` without its scheme |
| R13 | Read the **MCP host** section | The listen address equals `mcp_host.listen` (`127.0.0.1:5911` by default) and is rendered as text, not an input: ClawEh refuses any non-loopback address, so there is nothing to edit here |
| R14 | Compare the **Protocol** radio with `channels.device.tls` | Two options; *wss (HTTPS)* is selected exactly when `tls` is `true`, *ws (unencrypted)* otherwise, a missing key reading as ws. Its hint is the one sentence *Devices connect to one port, plain or with the WebUI certificate.* |
| R15 | Compare **External address** with `channels.device.external_url`, then type `wss://e2e-probe.invalid:18791` and wait for the save delay | The field shows the stored value as `host[:port]`: no `https://`, `http://`, `wss://` or `ws://` and no trailing slash (blank when unset). The typed URL is refused under the field with *Enter a host name or IP address, with an optional :port.* and `channels.device.external_url` is unchanged: nothing was sent. Leaving the page discards the typed value |
| R16 | Compare **Never locked out** with `gateway.lockout_exempt` | One entry per line, in order (empty when unset) |
| R17 | Compare **Trusted proxies** with `gateway.trusted_proxies` | One entry per line, in order (empty when unset) |

## H. Channels

| ID | Process | Expected |
|---|---|---|
| H1 | Load `/channels` | The channel list renders |
| H2 | Load `/channels/slack` | Renders the Slack channel; does not say "not found" |
| H3 | Load `/channels/telegram` | Renders Telegram, with **no Slack content** — proves the page is driven by its route param |
| H4 | In an `allow_from` field, type a value ending in a comma, e.g. `111,` | The trailing comma survives. It used to be eaten: the field resynced from the reparsed array on every keystroke |

## I. Models and providers

| ID | Process | Expected |
|---|---|---|
| I1 | Load `/models` | Lists the models from `GET /api/models`; no console errors and **no error boundary** (the boundary's only fixed text is its **Show error** button) |
| I2 | Load `/providers` | Lists the providers from `GET /api/providers`; no console errors, no error boundary |
| I3 | `/models` → **Add Model**, then Escape | The sheet opens and closes with no console error |
| I4 | Load `/providers` and count the **Configured** / **Not configured** labels | Every card in the API grid carries one. "Configured" is the backend's answer, not a guess from the config: an API key for an HTTP provider, and for a CLI a binary that actually resolves — so a stale path reads *Not configured* and a blank one that resolves reads *Configured* |
| I5 | `/providers` → **Add Provider** → open the wire-protocol picker | No `*-cli` protocol is offered. A CLI is added by its switch in the **Local CLI agents** section; building one by hand here would produce a provider the section does not show and the grid filters out |
| I6 | Load `/providers` and count the rows in **Local CLI agents** | One row and one switch per supported CLI from `GET /api/system/clis`, whether or not the binary is installed. A CLI the host lacks is greyed out and says so — hiding it would look like ClawEh does not support it. Every **configured** row shows its model count (`1 of 1 model on`, `3 of 3 models on`), including CLIs with a single model: printing it for some and not others reads as a fault |
| I7 | Compare the provider cards against `GET /api/providers` | The grid holds only the non-CLI providers. CLI providers appear in the section above and nowhere else: one CLI is one thing to the person using it, and showing it twice under two controls is what made it confusing |
| I8 | Load `/providers` and read a CLI row | **Args:** lists the whole command line in invocation order — the provider's own flags (`-p --output-format json`), the permission flags (`--dangerously-skip-permissions`, `--yolo`), whatever the models add, then the stdin marker. Not just the configured part: someone asking what ClawEh runs on their machine is owed all of it, and some of it auto-approves tool use |
| I9 | `/providers` → edit a configured CLI from its row | The sheet offers the Command field and **no** advanced section. Proxy, `strict_compat`, `require_reasoning_content`, `no_parallel_tool_calls` and `response_format_json` are HTTP wire knobs the CLI factory never reads; shown here they were controls that did nothing, and an off switch reads as a feature available but disabled — which is how `response_format_json` came to look like the reason a CLI was not returning JSON. It always does: `--output-format json` is in the argv, not the config |
| I10 | With a CLI provider configured (`GET /api/system/clis` has a `configured` row; skip with a note otherwise), load `/providers` and `/models` | Both render with the CLI row present, no console errors and no error boundary. A CLI row carries argument lists the server encodes as `null` when empty (`required_args`, `bypass_args`, `extra_args`); spreading one used to throw *Spread syntax requires …iterable* straight into the boundary |
| I11 | With a CLI provider configured (skip with a note otherwise), load `/providers` and find the **Allow CLI to bypass restrictions** checkbox on every configured CLI row | The checkbox is present for each configured CLI and its state equals that provider's `bypass_restrictions` from `GET /api/system/clis` — off unless the operator ticked it. It is the only control that lets a CLI run with its permission-bypass flag, so it must never be missing or show the wrong state |

## J. Devices

| ID | Process | Expected |
|---|---|---|
| J1 | Load `/devices`, and read `GET /api/devices/pair` | Renders, no console errors; the **Pair a device** card says **Devices will connect to** followed by the response's `connect_url` (for example `wss://ops42.example.com:42330`) |
| J2 | Request `/api/devices`, `/api/devices/pending` and `/api/devices/pair` concurrently, 12 times | No `5xx`. These share one SQLite store; opening it per request used to lose a WAL-conversion race and return an intermittent 500 |

The client address shown after a pending request's name (`remote_ip` from
`GET /api/devices/pending`, rendered as `Rabbit R1 · from 203.0.113.5`) needs a
device asking to pair, so it is not checked here; `devices-page.test.tsx`
covers it.

## K. Logs, MCP, memory, voice, report

| ID | Process | Expected |
|---|---|---|
| K1 | Load `/logs` | Shows log lines |
| K2 | Load `/mcp` and `/mcp/servers` | Both render, no console errors |
| K3 | Load `/memory` and `/voice` | Both render, no console errors |
| K4 | Click **Check Up** in the sidebar (below the groups) | `/report` renders with the heading **Check Up**, an identity line starting `ClawEh <version>` and naming the platform (`… on <host>`), a table with the headers **Action / Item / Status** and at least one row, and below it a **Full report** button whose `href` is `/api/report/pdf`; no console errors |
| K5 | `GET /api/report/pdf` | 200, `Content-Type: application/pdf`, `Content-Disposition: inline; …`, body starts with `%PDF-` |
| K6 | `POST /api/mcp/servers/no-such-server/reconnect` | 404 with a JSON `error` (the Reconnect button on `/mcp/servers` calls this for the selected server) |
| K7 | `GET /api/gateway/alerts` | 200 with a JSON `logs` array (the operator alerts log; the Logs page shows it when its source selector is set to Alerts) |
| K8 | `GET /api/report/assessment` | 200, `Cache-Control: no-store`; JSON `identity` with `name` (`ClawEh`), `version`, `build`, `platform`, `generated_at`, and a non-empty `assessment` array of `{action: bool, item, status}` — the PDF's security assessment rows in order. No credential-shaped value (`sk-…`, `xoxb-`, `xapp-`) anywhere in the body |
| K9 | `curl -b jar $BASE/api/voice/stt`, then load `/voice` and read it **before clicking anything** | One backend row per `stt[]` entry, each with its provider selected, and no *No transcription backends configured* line; with none configured, that line and no rows. The page used to seed its rows from an empty list when the query was already cached, said nothing was configured, and the first **Add backend** then saved that empty list over the real configuration |
| K10 | Open `/agent/bindings`, then click through the sidebar: **Services** → **MCP** → **Servers**, then **Config**, then **Network**, then **System**, reading each page before clicking anything on it | `/mcp/servers` lists every server in `tools.mcp.servers` (from `GET /api/config`) and shows *No external servers configured.* only when there are none; `/mcp/config` shows `mcp_host.listen` in **Listen Address** and `mcp_host.enabled` on **Enabled**; `/system` shows `backup.dest` in the backup destination and `agents.defaults.max_tokens` in **Max tokens**. Reached this way the config query is answered from the browser cache, and the three pages used to keep their empty defaults, which the next edit then saved over the real configuration |

## L. Setup wizard

| ID | Process | Expected |
|---|---|---|
| L1 | Load `/setup` and walk Welcome → Network → Provider → Model → Agent → Review. Set a port, pick a CLI provider, name the agent `Alice` | Each step advances. Choosing a CLI provider hides the API-key field and enables **Next**. The Review step reports the provider, model and agent name chosen |
| L2 | Do **not** click Finish | No new agent appears in the configuration |

## M. API surface

| ID | Process | Expected |
|---|---|---|
| M1–M11 | `curl -b jar -o /dev/null -w '%{http_code}' $BASE<path>` for `/api/system/version`, `/api/config`, `/api/models`, `/api/providers`, `/api/agents/tools`, `/api/skills`, `/api/devices`, `/api/devices/pending`, `/api/auth/status`, `/health`, `/ready` | All `200`. `/ready` returning 503 after startup means the readiness flag was never set |
| M90 | `curl -b jar $BASE/api/config` and search for credentials | Every `api_key` is masked. The login gates `/api/*`, but a credential in this response would still sit in the browser's memory, devtools and any proxy log for the signed-in session — masking is defence in depth, not a substitute for the login |

## N. Memory curation

Creates a domain called `e2e-probe` in the first memory store, works inside it,
and deletes it at the end. Nothing outside that domain is touched.

| ID | Process | Expected |
|---|---|---|
| N1 | `curl $BASE/api/memory` | At least one cognitive-memory database is listed. An install with none skips the rest of the group |
| N2 | `POST /api/memory/{id}/domains` with `{"name":"e2e-probe"}` | `201`, and the response carries the new domain id |
| N3 | `POST /api/memory/{id}/domains/{domain}/memories` with a fact | `201`, `origin: "user"` and `confidence: 1`. That origin is the one piece of provenance that is verifiable rather than the model's self-report, and nothing could write it before this existed |
| N4 | Post a memory with `"type":"observation"` | `400`. Type decides whether a memory is in the prompt at all, so an unrecognised one must be refused, not coerced |
| N5 | `PATCH` the memory to `{"type":"event"}` | `200` and the type changes. This is the correction the page exists for |
| N6 | `PATCH` it to `{"status":"retired"}`, then read the store with and without `?include_retired=1` | Absent by default, present with the flag. If a retired memory cannot be seen it can never be restored |
| N7 | `PATCH` it back to `{"status":"active"}` | `200`, status active |
| N8 | Add two more, then `POST /api/memory/{id}/bulk` with `retype` to `operational` over all three | `200` and `changed` equals the number sent. Bulk is on the critical path: a production store can hold hundreds of near-identical recurring notes |
| N9 | `POST` a bulk `retire` over one good id and `hNOPE` | `200`, `changed: 1`, and `failed` names `hNOPE`. One bad id must not abort a batch of hundreds |
| N10 | `GET /api/memory/{id}/export`, then `POST` the body back to `/import?mode=merge` | The export is YAML carrying `format_version`, and merge-importing it creates **0** memories — the same document imported twice must change nothing |
| N11 | Load `/memory`, click the probe's store in the sidebar | The `e2e-probe` domain renders with its 3 memories, no console errors |
| N12 | Open the type dropdown on the first row and pick `preference` | The **stored** type changes, read back from the API. Steps 2–10 drive the API; from here the checks drive the page, because a control can be wired correctly and still not work — the type picker is a portalled listbox and jsdom is not a browser |
| N13 | Tick the checkbox in the **domain header**, then use **Change type…** on the bar that appears | The bulk bar is absent with nothing selected; the header checkbox selects every memory in the domain, and the bar retypes them all. Retyping a domain of several hundred entries one row at a time is not a job anyone starts, which is what made the bulk actions much less useful than they looked |
| N14 | **Add memory** on the domain, type some text, **Add** | The memory is in the store with `origin: "user"` |
| N15 | Retire a row, then click **Show retired** | It disappears from the default view and comes back behind the toggle. Without that it could never be restored |
| N16 | `DELETE /api/memory/{id}/domains/{domain}` | `204`, and the probe domain is gone even with `include_retired=1` |

## O. Status page

| ID | Process | Expected |
|---|---|---|
| O1 | `curl $BASE/api/system/status` | `200`, carrying `version`, `uptime`, `pid`, `memory_bytes` and `agents`. `memory_bytes` is resident set size — between 1 MB and 2 GB. A gigabyte-scale figure means `VmSize` was read instead of `VmRSS`, which for a Go process counts over a gigabyte of reserved address space |
| O2 | Compare the counts against `GET /api/config` | `agents` matches what the configuration lists. `models` counts only **enabled** models, and `providers` only **configured** ones, so neither equals the length of its config list |
| O3 | Load `/`, click **Status** in the sidebar | The link is in the sidebar **footer** — below the collapsible groups, so it is reachable without opening a disclosure — and navigates to `/status` with no console errors |
| O4 | Load `/status` | The memory, assistants and uptime tiles all render live figures; memory reads as a size with a unit |
| O5 | Load `/status` and read the lower detail box | It carries a **Compiler** line (`go1.27.1`) and an **Environment** line (`Ubuntu 24.04.4 LTS on amd64`, falling back to `linux on amd64` on a host that publishes no name). The memory tile shows one figure — no Go heap: the heap is a subset of RSS and a diagnostic detail, and this page reports how big the process is |
| O6 | Compare `providers` from `GET /api/system/status` against the `ready` flags in `GET /api/providers` | The two agree. Both read one backend rule — an API key for HTTP providers, a binary that actually resolves for CLI ones — so a mismatch means one surface went back to guessing from the config. Any CLI provider that is `ready` with no `command` set reports the binary it resolved to |

## P. Authentication

The login page, the refusals, the session, and sign-out. P1, P2, P5, P6 and P7
run in browser contexts of their own so the suite's session is untouched; P2
records one failed login, which the limiter forgets on the next success (P5).

Sessions live in memory on the server, so a restart of ClawEh forgets them all.
P6 and P7 stand in for that by deleting the session cookie in the browser
context: the page must return to the login page, not show an error, and the
chat socket must stop reconnecting. The banner shown while ClawEh itself
is down (*Connection lost. Reconnecting…*) cannot be exercised against the live
dev instance without stopping it; it is covered by unit tests only
(`connection-banner.test.tsx`, `auth-redirect.test.ts` and
`claw-chat-controller.test.ts` under `web/frontend/src`).

| ID | Process | Expected |
|---|---|---|
| P1 | In a fresh browser (no session), load `/agents` | Redirected to `/login?next=%2Fagents`. The card is headed **ClawEh** (the product name, not "Sign in"), shows the **Username** and **Password** fields and a **Sign in** button, and does **not** carry the line *Use the admin account created on the server*; no console errors. The frontend gate remembers where the visitor was going, so a bookmark still works after signing in. (With no admin account the same page instead reads **No admin account** and tells you to run `claw admin` on the server) |
| P2 | On `/login`, enter the right username with a wrong password, **Sign in** | The form stays on `/login` and shows **Invalid username or password.** — one message for a bad username and a bad password alike, so the form never confirms which half was right. `GET /api/auth/status` from that browser still says `authenticated: false` |
| P3 | **anonymous** `curl -i $BASE/api/config` | `401` with a JSON `error` (`authentication required`, or `no admin account` with a `claw admin` hint) and **no configuration in the body** |
| P4 | `curl -b jar $BASE/api/auth/status` | `{"configured":true,"authenticated":true,"username":"<admin>"}` — the username is the one that signed in |
| P5 | Sign in in a second browser, open `/agents`, click **Sign out** in the sidebar footer | The browser lands on `/login` with the form showing. From that browser `GET /api/auth/status` is `authenticated: false` and `GET /api/config` is `401`: the session ended on the server, not just in the browser |
| P6 | Sign in in a second browser, open `/agents`, delete the session cookie (DevTools → Application → Cookies, or `context.clearCookies()`), then open the **Models** group in the sidebar and click **Models** | The browser lands on `/login?next=%2Fmodels` with the login form showing; no error state (`data-testid=route-error`) is rendered and the only console entry is the browser's own note about the refused (401) request. Signing in returns to `/models` |
| P7 | Sign in in a second browser, open `/` (chat), wait for the socket to open, delete the session cookie as in P6, then close the socket (from the console: the page's WebSocket, or restart ClawEh) | The page goes to `/login` within a few seconds and stays there: at most one further `/webui/ws` attempt after the redirect, no retry storm, no console errors |

## Q. Audit page

`GET /api/audit?kind=…&agent=…&limit=…&before_id=…` returns
`{events, next_before_id, dropped}`, newest first. The page asks for 100 rows at
a time.

| ID | Process | Expected |
|---|---|---|
| Q1 | Open the **Services** group in the sidebar, click **Audit** | The link is under Services and navigates to `/audit` with no console errors |
| Q2 | Load `/audit` | The table renders with the columns **Time, Kind, Actor / agent, Channel, Tool / summary, Outcome, Duration**, no console errors. The suite's own login is an `auth` event, so the trail is never empty by now; an empty state or an error here means events are not being recorded |
| Q3 | Open the **Kind** filter and pick **Auth** | The page requests `/api/audit?…kind=auth…` and every row shown reads **Auth** — the filter is applied by the server, not by hiding rows |
| Q4 | `curl -b jar '$BASE/api/audit?limit=100'`, then load `/audit` | **Load more** is shown when, and only when, the API returned 100 events **and** `next_before_id > 0`. On a fresh instance it is 0 and the button is absent; either way the page and the API must agree |

---

## S. Keyboard

Every step in this group uses the keyboard only: Tab, Shift+Tab, Enter, Space
and Escape. Nothing is clicked.

| Step | Process | Expected result |
|---|---|---|
| S1 | In a fresh browser context open `/login`; Tab to the username field, type it, Tab, type the password, Enter | The session is created and the browser leaves `/login` |
| S2 | Open `/`; Tab until the sidebar entry **Agents** has focus; Enter (expands the group); Tab; Enter | The browser is at `/agents` |
| S3 | On every route in the suite's list, press Tab until the focus order wraps to its first stop | No stop is reached twice before the wrap (no focus trap); every stop paints a focus ring (outline or box-shadow); no stop has `opacity: 0` while focused; at least one control takes focus |
| S4 | On every route, Tab through the whole focus order and read each stop's accessible name (aria-label, aria-labelledby, an associated label, an image's alt, text, title or placeholder) | Every stop has a name. Icon-only controls carry `aria-label`; switches whose visible label sits beside them carry it too |
| S5 | Create a throwaway agent `e2e-kbd` through the API with `subagents.allow_agents: ["e2e-kbd"]` and `archive_days: 30`; on `/agents` Tab to it in the rail, Enter; Tab to the `time_now` checkbox under Internal tools; Space; wait ~2s | `tools` for `e2e-kbd` contains `time_now` in `GET /api/config`, its `subagents` and `archive_days` are unchanged, and every other agent's `workspace`, `subagents`, `memory`, `compression` and `context_eviction` are as before. The page re-sends the whole agent list, so a field it does not show must be written back, not dropped. The agent is removed through the API afterwards |
| S6 | On `/agents` Tab to **Add Agent**, Enter; then Escape | After Enter, focus is in the **Agent ID** field; after Escape the form is gone and nothing was created |

## Recording a run

Note the date, the version string from A2, and the pass/fail of each ID. The
runner prints exactly this and exits non-zero if anything failed.

## When a step fails

Decide first whether the fault is in the **plan** or the **product** — a test
that encodes a wrong assumption is worse than no test. Two examples from this
plan's own history:

- C3 originally clicked the sidebar entry and expected navigation. The entries
  are disclosure controls, so the step was wrong, not the WebUI.
- M11 expected `/ready` to answer 200 and it answered 503 forever. That one was
  the product: nothing ever set the readiness flag.

Fix whichever is genuinely wrong, and keep this document and
`tests/frontend-e2e.mjs` in step.
