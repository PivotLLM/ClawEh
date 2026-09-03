# Frontend — update and cleanup tracker

Working list for bringing `web/frontend` up to date and clearing the issues found
in the 2026-09-02 audit. Tick items off here as they land; delete the file when
everything is done.

The stack itself is **not** in question. React 19 + Vite + TypeScript, TanStack
Router/Query, Tailwind 4, Radix via shadcn, jotai, i18next is the current
mainstream choice for this shape of app, and it is the right one here: the SPA is
embedded in the Go binary (`web/backend/embed.go`) and served from the gateway's
own mux, so anything needing a Node runtime in production — Next.js, Remix,
SvelteKit, Nuxt — would be a step backwards. Every item below is about how the
stack is *used*, not which stack it is.

Legend: `[ ]` todo · `[x]` done · `[~]` accepted, no action planned

---

## Done — 2026-09-02

- [x] **Security audit 87 → 1.** Was 6 low / 44 moderate / 36 high / 1 critical,
      all through build-time tooling, none reaching the shipped bundle.
      In-range `pnpm update` cleared them: `vite` 7.3.1 → 7.3.6 (`server.fs.deny`
      bypass, dev-server arbitrary file read), `@tanstack/router-plugin` (the
      critical `seroval` deserialization type confusion), plus `postcss` and
      `nanoid` transitively.
- [x] **Removed `wrap-ansi`** from `dependencies` — a terminal ANSI line-wrapper
      in a browser app, referenced nowhere but its own declaration.
- [x] **Moved to `devDependencies`:** `shadcn` (a scaffolding CLI, imported by
      nothing, and the entry point for 45 of the 87 advisories via `express` /
      `hono` / `@modelcontextprotocol/sdk`), `@tailwindcss/vite`, `tailwindcss`,
      `@tanstack/react-router-devtools`. Runtime deps 26 → 21.
- [x] **In-range minor/patch updates** for everything else: `radix-ui`
      1.4.3 → 1.6.7, `shadcn` 4.0.5 → 4.20.1, `jotai` 2.18 → 2.20.3, `react`
      19.2.0 → 19.2.8, `tailwindcss` 4.2.1 → 4.3.3, and others.

### Confirmed clean — no action needed

Checked during the audit; recorded so nobody re-checks them:

- No source maps in the shipped bundle.
- TanStack devtools is imported in `src/routes/__root.tsx` but **tree-shaken out**
  of production — zero occurrences in any built asset.
- No `dangerouslySetInnerHTML`, `eval`, or `new Function`.
- `react-markdown` runs without `rehype-raw`, so there is no raw-HTML injection
  path through chat content.
- No secrets in source. `localStorage` holds only the last session id and the
  theme; no tokens (`src/lib/claw-chat-state.ts`, `src/hooks/use-theme.ts`).
- All `target="_blank"` links carry `rel="noreferrer"`.

---

## 1. Lint — DONE, 35 → 0

`pnpm lint` was failing before this work started (7 problems);
`eslint-plugin-react-hooks` 7.0.1 → 7.1.1 added the `react-hooks/refs` rule and
tightened `set-state-in-effect`, surfacing the rest. All 35 were real findings,
and all 35 are fixed. What was found, and how each family was resolved:

| Rule | Count | What it means |
|---|---|---|
| `react-hooks/set-state-in-effect` | 24 | `setState` called synchronously in an effect body → cascading renders |
| `react-hooks/refs` | 10 | `ref.current = value` assigned during render |
| `react-hooks/exhaustive-deps` | 1 | missing effect dependency |

- [x] **`set-state-in-effect` — the mount-fetch family (majority).**
      `useEffect(() => { void loadData() }, [loadData])` in
      `bindings-page`, `devices-page`, `memory-page`, `mcp-page`,
      `mcp-servers-page`, `models-page`, `providers-page`, `voice-page`,
      `agents-page`, and the model/provider sheets and dialogs. These are
      hand-rolled data fetching, so they were fixed by item 2 rather than
      locally.
- [x] **`set-state-in-effect` — the derived-state family.**
      `agents-page.tsx:1095` (`if (selectedId !== "") setSelectedId("")`) and the
      `channel-forms/*` reset effects. Resolved by deriving during render (the
      agent rail selection is now computed, not written back) or by React's
      documented render-phase adjustment, never by suppressing the rule.

      The `channel-forms/*` fix also removed a latent bug: those effects
      depended on the `config.allow_from` **array identity**, which typing
      rebuilt on every keystroke, so the draft was overwritten with the
      reparsed value and a trailing `,` or space vanished as it was typed. The
      replacement compares the joined string, so it only resyncs on a real
      change.
- [x] **`react-hooks/refs` — 10 sites.** `channel-config-page.tsx:174,176,178,305`,
      `secmsg-page.tsx:217,282`, `telegram-bots-page.tsx:201`,
      `agents-page.tsx:1116`, `config-page.tsx`. All are the same deliberate
      trick — *"refs so the debounced save reads current values without being
      re-created"*. The ref WRITES moved into an effect; the reads stayed at
      fire time, which is what makes the debounce save what the user actually
      typed. This mattered: the handler calls `setState()` immediately before
      `scheduleSave()`, so capturing the render variable instead would have
      saved the previous value. `useEffectEvent` (React 19.2) looks like the
      right tool and is not — it may only be called from effects and effect
      events, never from a `setTimeout`.
- [x] **`exhaustive-deps`** — `setup-wizard.tsx`: the `steps` array is now memoised, so the `useMemo` that depends on it is not defeated every render.
- [x] `pnpm lint` and `tsc -b --noEmit` are both clean; item 3 keeps them that way.

## 2. Adopt TanStack Query on the pages that hand-roll fetching — DONE

`@tanstack/react-query` is already a dependency and already used by some hooks,
but the large pages fetch with `useEffect` + `useState` instead. That is the root
cause of most of item 1, and it also means no caching, no request dedupe, no
retry, and hand-written loading/error state in every page.

- [x] Converted to `useQuery`: providers, models, agents, bindings, channel-config, secmsg, telegram-bots, mcp, mcp-servers, gateway-logs. The provider list is shared by cache key, so opening a model sheet costs no request.
- [x] The paired `loading`/`fetchError` state is gone from each; bindings lost three pieces of state entirely (bindings/channels/agents are now derived).
- [x] Verified in a real browser — see item 6.

## 3. Wire the frontend into the build gates — DONE

Nothing in `Makefile` or `test.sh` runs the frontend lint or typecheck, which is
why 7 lint errors sat unnoticed until a plugin update turned them into 35.

- [x] `make frontend-lint` (tsc + eslint) and `make frontend-test` (vitest).
- [x] `make check` runs both; `test.sh` has a FRONTEND section that fails the suite, and reports `Frontend: passed/failed/skipped` in the summary. It skips (not fails) without pnpm or node_modules, so a Go-only checkout still runs. It earned its keep immediately: the first run caught two type errors in the new tests.
- [x] Landed green.

## 4. Frontend tests — STARTED

Was 20,359 lines of TypeScript with zero test files. The chat controller and the
session/token handling are load-bearing, so they went first. Component tests are
still absent.

- [x] **Console output fails a test.** `src/test-setup.ts` traps
      `console.error`/`console.warn` and throws, because React reports most real
      problems that way — invalid hook usage, updates outside `act()`, updates
      to an unmounted component — and then carries on, so a test can be green
      while the component is complaining. Output that IS expected gets declared
      with `expectConsole(/…/)` rather than silently tolerated; the no-token
      controller test uses it. Verified it fails on a planted `console.warn`
      and a planted `console.error`.
- [x] Vitest + jsdom added (`pnpm test`). React Testing Library is NOT added yet — nothing renders components in a test so far, and an unused dependency is the thing this audit just spent effort removing. Add it with the first component test.
- [x] 23 tests across both. `claw-chat-state`: storage round-trip, whitespace-only and empty values, localStorage being unavailable, all three session-id generation paths (incl. the v4 bit-twiddling in the getRandomValues fallback), and the seconds/milliseconds threshold. `claw-chat-controller`: the token travels as a subprotocol and never appears in the URL, the `claw-token` marker matches the Go side, loopback ws_url rewriting on and off localhost, no socket without a token, no double-connect. Mutation-checked: reverting to `?token=` fails two tests.
- [x] Wired into `test.sh` and `make check`.
- [ ] Component tests: add React Testing Library with the first one. Nothing
      renders a component in a test yet, so RTL is deliberately not installed.

## 5. Decompose `agents-page.tsx` — DONE, 1619 → 625

Split by concern, no behaviour change:

| File | Lines | What |
|---|---|---|
| `agent-model.ts` | 210 | types, parsing, helpers — no React |
| `use-agent-autosave.ts` | 171 | edit buffers, debounce, save status |
| `agent-card.tsx` | 591 | one agent's editor |
| `skills-select.tsx` | 58 | skills picker |
| `agents-page.tsx` | 625 | list, rail, add/delete, config patching |

- [x] Split by concern, following the conventions already in the tree
      (`config/form-model.ts`, `models/model-card.tsx`).
- [x] **Nine parallel edit arrays collapsed into one `AgentEdits[]`.** They were
      `agentModelsEdits`, `agentSkillsEdits`, `agentToolsEdits`,
      `agentMessageEdits`, `agentTemperatureEdits`, `agentSummarizationEdits`,
      `agentShareCommonEdits`, `agentMountsEdits`, `agentMCPToolsEdits` — each
      indexed by agent position, each with its own setState, all nine mirrored
      into a nine-field ref and threaded through an eleven-parameter save
      function. Nothing but care kept them the same length and order.
- [x] The nine-field `latestRef` is gone; one ref holds one array.
- [x] The card wiring dropped from ~90 lines of near-identical
      `setX(prev => {...}); scheduleSaveAgent(i)` blocks to ~20 lines of
      `edit(i, { field: value })`.
- [x] The save/status cycle is broken properly: the hook owns save status and
      reseed suppression, so the page's save handler needs nothing back from the
      hook. The first attempt used a hoisted function declaration and the React
      Compiler lint rejected it — correctly, since hoisting does not make a
      value update over time.

Verified in a browser, not just by types: edited temperature and MCP access on
one agent and confirmed both persisted to `/api/config`; added a second agent,
which sorts ahead of the first and shifts every index, and confirmed the buffers
realigned — the moved agent kept its values and the new one showed none. Console
clean throughout.

### The other two oversized files — also done

`config-sections.tsx` (959) → nine files under `config/sections/`, largest 272:
`section-card.tsx` (ConfigSectionCard + the shared `UpdateCoreField` type),
`model-fields.tsx` (the three model pickers), and one file per exported section.
`config-page.tsx` imports them directly; no barrel was left behind. Verified as a
pure move: all eleven declarations present, and the normalised content differs
from the original by one character — a prettier line-wrap where adding `export`
pushed a signature past 80 columns.

`setup-wizard.tsx` (856 → 570) → one presentational component per step under
`setup/steps/`, plus `wizard-model.ts` for the shared constants, types and
`slugify`. `SetupWizard` still owns every piece of state and all the async work
(loading, provider test, finish); the steps only receive what they render.

Verified by walking the whole wizard in a browser, which is the only way to catch
a mis-threaded setter — types cannot: set the port and toggled network access,
navigated forward and back and confirmed both survived; selected a CLI provider
and confirmed the API-key field disappeared, Next enabled, and the model step
switched to the CLI default (all four state writes in the extracted
`handleProviderChange`); named the agent and confirmed the review step showed
provider, model and agent name correctly. Console clean.

Still oversized but not urgent: nothing above 600 lines outside `agent-card.tsx`
(591).

## 6. Browser verification — DONE

- [x] All 17 routes driven in headless Chromium against a real gateway: every page renders (content length + first line captured), no blank pages, no unhandled exceptions. NOTE: the Playwright **MCP** tools could not be used — they require an `SST…` session token that a Claude Code session does not have. Driven through the locally installed Playwright package instead, pointed at the `chromium-1200` build already in `~/.cache/ms-playwright`.
- [x] Console clean on every page except `/devices`, which intermittently (1 run in 3) gets a 500 from `GET /api/devices` with `{"error":"store open failed"}`. Pre-existing and **backend**, not frontend: `web/backend/api/devices.go` opens and closes its own SQLite handle to `state/gateway.db` per request, and the page fires several device requests at once. Sequential and `curl`-burst requests always succeed; a browser reproduces it. Not fixed here — separate issue.

## 7. Framework majors — DONE

Done one at a time, each with a build and a browser check, as intended.

- [x] `vite` 7.3.6 → 8.2.2 (with `@vitejs/plugin-react` 6 and `vitest` 5)
- [x] `eslint` 9.39.5 → 10.9.1 (+ `@eslint/js` 10, `globals` 17, `eslint-plugin-react-refresh` 0.5)
- [x] **`typescript` 5.9.3 → 7.0.2 — done, by removing eslint.** TS 7 removed
      `baseUrl` (the `paths` were already tsconfig-relative, so nothing else
      changed) and typechecks the whole project in 0.65s. The blocker was
      `typescript-eslint`, which refuses to load against TS 7 and has no release
      that does (upstream issue 10940). Resolved by removing eslint and moving
      to oxlint (7b) rather than by waiting.

- [x] `i18next` 25 → 26 and `react-i18next` 16 → 17. No code changes needed.
      A browser sweep for untranslated keys (i18next renders the key verbatim
      when lookup fails) found a PRE-EXISTING gap, not a regression: the tool
      catalog defines `common` and `memory` categories that `en.json` had no
      labels for, so the Tools page printed
      `pages.agent.tools.categories.common` as a heading. Both added.
      `discovery` is defined in `en.json` but matches no backend category —
      stale, left alone.
- [x] `@types/node` 24 → 26.4.1
- [x] `@types/react-dom` 19.2.5 → 19.2.7
- [x] `prettier-plugin-tailwindcss` 0.7.4 → 0.8.1 — no reformatting churn this time; `prettier --check` was clean immediately.

## 7a. Route components defined inline — 8 fast-refresh warnings

`eslint-plugin-react-refresh` 0.5 warns on eight route files that DEFINE their
component inline instead of importing it: `__root.tsx`, `agent.tsx`,
`skills.tsx`, `tools.tsx`, `channels/$name.tsx`, `channels/route.tsx`,
`config.tsx`, `mcp.tsx`. Fast refresh cannot reach a component the file does not
export, so editing one of them full-reloads the page instead of hot-swapping.

The other route files already do it right — `agents.tsx` is three lines that
import `AgentsPage` — so the fix is to follow the convention the codebase
already has, not to silence the rule. `allowExportNames: ["Route"]` does NOT
help; the warning is about the inline component, not the `Route` export.

Warnings only. NOTE: oxlint does not carry this rule, so these are no longer
reported at all — the finding stands on its own merits, not on a linter.

- [x] Done. All eight route files are now 7 lines (5 for `__root.tsx`), each
      importing its component. New components: `root-layout.tsx`,
      `agents/agent-layout.tsx`, `channels/channel-page.tsx`,
      `channels/channels-layout.tsx`, `config/config-layout.tsx`,
      `mcp/mcp-layout.tsx`. `agent/skills.tsx` and `agent/tools.tsx` had no
      component worth moving — their inline components were bare pass-throughs,
      so they now point at `SkillsPage` / `ToolsPage` directly.

      One non-verbatim change: `channels/$name.tsx` read its param through
      `Route.useParams()`, which is unavailable once the component leaves the
      route file (importing `Route` back would be circular). It now uses
      `useParams({ from: "/channels/$name" })` — the documented equivalent,
      still typed against the generated route tree. Verified in a browser
      against two different channels to confirm it is genuinely param-driven.

## 7b. Linter — oxlint, replacing eslint — DONE

eslint was removed because typescript-eslint blocks TypeScript 7. oxlint 1.81
replaces it. It is installed **globally**, not as a project dependency, so both
gates skip rather than fail when it is absent — but they say so out loud, because
a silent skip is how the old lint rotted from 7 problems to 35.

- [x] `make frontend-lint` (skips with a message if oxlint is missing;
      `OXLINT=/path/to/oxlint` overrides the binary), wired into `make check`.
- [x] `test.sh` runs it in the FRONTEND section and reports
      `passed` / `FAILED` / `skipped: oxlint not installed` in the summary.
- [x] `.oxlintrc.json` carries the plugins, rules and ignores, so a bare
      `oxlint src` matches exactly what the gates run.
- [x] `--deny-warnings` is on in both gates. oxlint exits 0 on warnings by
      default, so without it the check could never fail and was decorative.
      Both gates also lint the WHOLE project rather than `src` alone (134 files
      vs 132), which picks up `vite.config.ts` and anything else at the root.

      **How to check the current warning count:**

      ```
      cd web/frontend && oxlint            # human output, exit 1 on any warning
      cd web/frontend && oxlint --format=json | jq '.number_of_files, (.diagnostics|length)'
      make frontend-lint                   # what the gate runs
      ```

      Verified the gate can actually fail, not just pass: a file with one
      `setState`-in-effect was planted, `make frontend-lint` exited 1 and
      `test.sh` reported `lint FAILED` / `Frontend: failed`; removing it
      restored green.

**Coverage check — the rules that mattered are still enforced.** Verified
empirically against a file written to violate each one, not assumed from docs:

| Rule | Caught? |
|---|---|
| `react(set-state-in-effect)` | yes — found 24 of the original 35 |
| `react(refs)` — `ref.current` written during render | yes — the agents-page bug |
| `react-hooks(exhaustive-deps)` | yes |

Two caveats worth knowing. The react plugin is **off by default**: a bare
`oxlint src` with no config reports nothing at all on this codebase, which is
why the settings live in `.oxlintrc.json`. And oxlint does not appear to carry
the React Compiler rules that rejected the "accessed before it is declared"
cycle in the autosave hook — that specific class is no longer caught.

**Findings fixed while adopting it:**

- `react(no-children-prop)` ×2 — `logs-page.tsx` and `skills-page.tsx` passed
  `children` to `PageHeader` as a prop; every other caller nests it. Now nested.
- `jsx-a11y(control-has-associated-label)` — the bindings table's actions column
  was an empty `<th />`, announced as nothing. Now carries an `sr-only` label.
- `jsx-a11y(label-has-associated-control)` ×4 — **false positives**. Those
  labels do wrap their control; oxlint cannot see through `Switch`/`Input`
  because they are custom components. Resolved by listing them in
  `controlComponents`, not by changing correct code.
- `jsx-a11y(no-autofocus)` — kept. The input only exists because the user just
  clicked to edit that row, so focus belongs there; removing it would add a
  click to every edit. Disabled inline with that reasoning.
- `vitest(require-mock-type-parameters)` ×4 — rule turned off. The mocks are
  typed through `vi.mocked()`; type parameters on the `vi.fn()` factories would
  add noise and no safety.

## 8. Bundle size

The main chunk is 576 kB (190 kB gzipped); everything else is under 160 kB.

- [ ] Find what is landing in the entry chunk that should be route-split.
- [ ] Low priority — the bundle is served from localhost or a LAN, not the open
      internet.

---

## Accepted, no action planned

- [~] **`esbuild` advisory (low), via `vite → tsx`.** Arbitrary file read from
      the *dev server*, *on Windows only*. No in-range fix exists. ClawEh neither
      builds nor runs on Windows, and this cannot affect the shipped binary.
      Forcing it with a pnpm `override` would pin a transitive dependency
      indefinitely for a non-issue; revisit when `vite` pulls a patched `tsx`.
- [~] **`src/routeTree.gen.ts` churn.** The newer `@tanstack/router-plugin`
      emits imports in a different order, so the generated file shows ~173
      changed lines with no semantic change. It is regenerated on every build.
