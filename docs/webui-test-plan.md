# WebUI test plan

Regression coverage for the ClawEh web interface. Every step below has an ID, a
process, and an expected result, so it can be followed by hand — and every one is
also automated in `tests/frontend-e2e.mjs`, which prints the same IDs.

```
node tests/frontend-e2e.mjs                      # whole plan
node tests/frontend-e2e.mjs --only F,J           # selected groups
node tests/frontend-e2e.mjs --base http://host:port
```

## Before you start

**Run it against a dev instance, never production.** Groups F and G write
configuration. Both revert what they change — the agent created in F is deleted,
and the field edited in G is restored to the value read beforehand — but a
crash mid-run would leave the change behind. The runner refuses port 18790
unless `--allow-prod` is passed.

| Requirement | Notes |
|---|---|
| A running gateway | `make build && cp build/claw ~/bin/claw && sudo systemctl restart claw-dev` |
| At least one agent, model and provider | The plan asserts against live data; an empty install fails A3 |
| Playwright + Chromium | Override with `PLAYWRIGHT_MODULE` / `CHROME_PATH` |

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

**Process.** Load each of the 17 routes in a browser with the console open:
`/`, `/agents`, `/agent/bindings`, `/agent/tools`, `/agent/skills`, `/channels`,
`/config`, `/config/raw`, `/devices`, `/logs`, `/mcp`, `/mcp/servers`, `/memory`,
`/models`, `/providers`, `/voice`, `/setup`.

**Expected.** Each renders substantive content (>40 characters of text) and logs
**no console errors**. A blank page or a red console entry is a failure.

## C. Shell and navigation

| ID | Process | Expected |
|---|---|---|
| C1 | Look at the sidebar | Contains Chat, Agents, Models, Channels, Services |
| C2 | Look at the sidebar footer | Shows `ClawEh v<version>` |
| C3 | Click the **Models** sidebar entry, then the **Models** link beneath it | The entry is a disclosure control (`aria-expanded`), not a link. It expands, and the link navigates to `/models` without a full page load |
| C4 | `curl -o /dev/null -w '%{http_code}' $BASE/no-such-route` | `200` — unknown paths fall through to the SPA |

## D. i18n integrity

| ID | Process | Expected |
|---|---|---|
| D1 | Load all 17 routes; scan the rendered text for anything shaped like a translation key (`pages.…`, `navigation.…`) | None found. i18next renders the key verbatim when a lookup fails, so a leaked key is the only visible symptom of a broken locale |
| D2 | Load `/agent/tools` | No heading reads `…categories.<name>`. Tool categories come from the backend catalog; a category with no label in `en.json` shows as a raw key |

## E. Chat and WebSocket auth

| ID | Process | Expected |
|---|---|---|
| E1 | `curl $BASE/api/webui/token` | `200`, with a non-empty `token` and a `ws_url` beginning `ws://` or `wss://` |
| E2 | Load `/`, and inspect the WebSocket the page opens | Constructed with subprotocols `["claw-token", "<token>"]`. **The token must not appear in the URL** — a token in a query string is recorded by proxies, access logs, Referer headers and browser history |
| E3 | Load `/` and wait ~2s | Chat does not report "disconnected" or a connection error |

## F. Agents — autosave and list realignment

Creates an agent called `e2e-probe` and deletes it at the end.

| ID | Process | Expected |
|---|---|---|
| F1 | `/agents` → **Add Agent** → ID `e2e-probe` → **Add** | The agent appears in `GET /api/config` |
| F2 | Select it, set **Temperature** to `0.77`, wait ~2s | `temperature: 0.77` persisted. Saves are debounced ~600 ms; reading back immediately will race |
| F3 | Set **MCP access** to `fusion, trello`, wait ~2s | `mcp_tools: ["fusion","trello"]` persisted **and** `temperature` is still `0.77`. The second save must not clobber the first |
| F4 | Select `e2e-probe`, note its temperature; select another agent, note its temperature | `e2e-probe` shows `0.77`; the other agent does not. Adding an agent re-sorts the list and shifts every index, so the edit buffers must follow. **Select agents by their displayed name** — the rail shows `name`, falling back to `id` |
| F5 | With `e2e-probe` selected, click the trash button in the card header, accept the confirmation | The agent is gone from `GET /api/config` |

## G. Config page

| ID | Process | Expected |
|---|---|---|
| G1 | Load `/config` | Sections Service, Runtime, Backup and Devices all render |
| G2 | Note the current **External URL**, change it, wait ~2s | The new value is in `gateway.external_url` |
| G3 | Restore the original value, wait ~2s | `gateway.external_url` matches what G2 recorded |
| G4 | Load `/config/raw` | The configuration document renders |

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
| I1 | Load `/models` | Lists the models from `GET /api/models` |
| I2 | Load `/providers` | Lists the providers from `GET /api/providers` |
| I3 | `/models` → **Add Model**, then Escape | The sheet opens and closes with no console error |

## J. Devices

| ID | Process | Expected |
|---|---|---|
| J1 | Load `/devices` | Renders, no console errors |
| J2 | Request `/api/devices`, `/api/devices/pending` and `/api/devices/pair` concurrently, 12 times | No `5xx`. These share one SQLite store; opening it per request used to lose a WAL-conversion race and return an intermittent 500 |

## K. Logs, MCP, memory, voice

| ID | Process | Expected |
|---|---|---|
| K1 | Load `/logs` | Shows log lines |
| K2 | Load `/mcp` and `/mcp/servers` | Both render, no console errors |
| K3 | Load `/memory` and `/voice` | Both render, no console errors |

## L. Setup wizard

| ID | Process | Expected |
|---|---|---|
| L1 | Load `/setup` and walk Welcome → Network → Provider → Model → Agent → Review. Set a port, pick a CLI provider, name the agent `Alice` | Each step advances. Choosing a CLI provider hides the API-key field and enables **Next**. The Review step reports the provider, model and agent name chosen |
| L2 | Do **not** click Finish | No new agent appears in the configuration |

## M. API surface

| ID | Process | Expected |
|---|---|---|
| M1–M11 | `curl -o /dev/null -w '%{http_code}' $BASE<path>` for `/api/system/version`, `/api/config`, `/api/models`, `/api/providers`, `/api/agents/tools`, `/api/skills`, `/api/devices`, `/api/devices/pending`, `/api/webui/token`, `/health`, `/ready` | All `200`. `/ready` returning 503 after startup means the readiness flag was never set |
| M90 | `curl $BASE/api/config` and search for credentials | Every `api_key` is masked. `/api/*` has no operator authentication, so an unmasked credential here is readable by anything that can reach the port |

---

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
