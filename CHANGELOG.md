# Changelog

All notable changes to ClawEh are recorded here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and
version numbers follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Entries describe what changed for someone **running or integrating with** ClawEh
— config keys, tool names, API shapes, protocol behaviour, defaults — not the
internal refactors behind them. A change nobody outside the repository can
observe does not need an entry.

## [0.5.0]

Cognitive memory redesign. The classification the model had to reason about at
every write shrinks to one field, the confirmation gate that was never used is
gone, and memory gains a type for things that happened — kept out of the prompt
and reachable by search.

### Removed

- **BREAKING: the `cogmem_memory_confirm` tool is gone.** Memories were written
  as `review` and waited for the assistant to ask you to confirm them. In
  practice it never asked: across seven agents, 244 memories were pending, the
  oldest 77 days, with zero confirmations. The gate is removed rather than left
  as dead weight. Nothing to migrate — confirmation was the only thing the tool
  did, and the memories it was gating are now active.
- **BREAKING: the config keys `memory.prompt.pending_surface` and
  `memory.prompt.pending_max` are gone.** They controlled the pending digest,
  which no longer exists. Remove them from your config; an unknown key is
  ignored, so nothing breaks if you leave them.
- **BREAKING: memories no longer have a `source` field.** It recorded whether
  the model believed a memory came from you or was inferred, gated nothing, and
  never reached the prompt. `origin` (`chat`, `consolidation`, `user`) records
  where a memory actually came from and is shown to both you and the assistant.
  Dropped from the database, `GET /api/memory/{id}` and the memory page.
- **BREAKING: memories no longer have a `priority` field.** It was stored and
  returned by the API and never read by anything.

### Added

- **A Local CLI agents section on the Providers page, with one switch each.**
  Claude, Codex, Antigravity and Cursor are listed whether or not their binary
  is installed — a CLI ClawEh supports but the host lacks is a different thing
  from one it does not support, so a missing one is greyed out and names the
  binary it looked for. The switch is the whole setup:

  - **On** creates the provider and a model if they are missing, with the
    protocol's own request timeout and a model id that lets the CLI choose its
    own model. An existing model is enabled in place, keeping whatever has been
    set on it — a switch is not a reset.
  - **Off** disables every model that runs through that CLI, not just one, so a
    switch carrying the CLI's name means the CLI. Nothing is deleted either way,
    so switching back on finds what it left behind.

  Setting one up by hand previously meant creating a provider on one page and a
  model on another, and knowing four values that appear in no form. One of them
  could not be set through the Web UI at all (see the `extra_args` entry under
  Fixed), so a CLI model created in the browser could not be made to work.

  CLI providers now appear only in this section, and the wire-protocol picker
  under **Add Provider** no longer offers `*-cli` protocols. Editing one — to
  pin an explicit binary path, say — is still available from its row.

- **Two new memory types.** `event` for something observed at a point in time —
  a trip, a delivery status, a scheduled run — and `operational` for the
  assistant's own housekeeping, such as where it files things and how it works.
  The full set is now `fact`, `preference`, `rule`, `event`, `operational`.
- **`event` memories stay out of the prompt.** They go stale and accumulate
  without bound, so they are never loaded automatically. Each domain reports how
  many it holds and names the call that reads them, and `cogmem_memory_search`
  reaches them with `include_events: true`.
- **Consolidation can tell which of two conflicting memories is newer.** Each
  memory it reviews now carries `age_days` — how long ago it was asserted — and
  they are listed oldest first. Previously it saw no time at all, so the rule
  that a newer instruction overrides an older one could not be applied to
  anything already stored: asked to resolve a contradiction, an assistant would
  correctly decline to guess which of two opposing instructions was current, and
  both stayed in its prompt indefinitely. Days rather than a timestamp, because
  the question is only which is newer.
- **Consolidation now tidies the domains it touches.** Where two memories say
  the same thing it retires the weaker one, and where a newer memory
  contradicts an older one it retires the older and records it in the conflict
  ledger — even when the conversation did not raise the topic. Nothing
  previously revisited a memory once written: de-duplication only ever ran
  against the current batch, so redundancy and stale contradictions
  accumulated indefinitely. One production agent had two active rules giving
  opposite instructions about the same notifications.

  It retires rather than rewrites, on purpose: several specific facts that
  merely share a topic are worth more than one vague paragraph, so only
  memories that genuinely say the same thing are collapsed. Automatic
  de-duplication by exact text match still runs as well.
- **A Status page in the Web UI**, reached from a **Status** entry at the bottom
  of the sidebar — outside the collapsible groups, since it describes the
  running process rather than a section of the configuration, and it is what you
  open when something feels wrong. It shows uptime, memory, and the number of
  assistants, channels, enabled models, configured providers and goroutines,
  over a detail box carrying the pid, version, build, Go toolchain, host OS and
  architecture, and whether the MCP host is running. Backed by a new
  `GET /api/system/status`, polled every five seconds so the figures stay live.

  Models are counted as *enabled* and providers as *configured*, not as the
  length of their config lists: an install carrying 40 model definitions of
  which 3 can run is described by the 3.
- **`/status` in chat now reports memory.** The command runs inside the process,
  so it reports on itself — which also means you can ask an assistant how much
  RAM it is using without shell access to the host.
- **`claw status` reports whether ClawEh is running, and its RAM.** It could
  previously answer neither: it reads configuration from disk and never looked
  at the process, so it described an installation rather than a running system.

  ```
  ClawEh:          running (pid 1690872), 40.4 MB RAM
  ```

  ClawEh writes `claw.pid` into its data directory at startup and removes
  it on clean shutdown. The data directory is the scope that matters — one
  binary runs several instances on a host, and the command has already resolved
  `CLAW_HOME` to find the config, so it reports on the instance you asked
  about. A stale file left by a hard kill reads as "not running": the process
  must still exist *and* still be claw, because pids are recycled.

  The figure is resident set size, the same number `ps` reports as RSS — not
  virtual size, which for a Go process includes a gigabyte of reserved address
  space and would suggest ClawEh is enormous when it is not.
- **Memory retention.** `event` memories are deleted after **30 days** and
  retired memories **90 days** after they were retired, both overridable per
  agent on the Agents page (blank = the default, `-1` = keep forever). Events
  stop being useful long before they stop accumulating — one agent recorded an
  hourly "nothing changed" note and reached 300 rows — and retiring leaves the
  row behind, so a store that retires steadily grows forever while showing
  nothing for it. **Only those two are ever deleted by age:** a `fact`,
  `preference`, `rule` or `operational` memory is permanent, so no retention
  policy can silently drop a standing instruction. The sweep runs as part of
  consolidation and logs what it removed.

  Retention is deliberately not something the model sets per memory. It already
  makes that judgement by choosing the type — "I drove to the KOA on 4 Sep" is
  an `event`, "we go to the KOA every Labour Day" is a `fact` — and a second
  knob would reopen the multi-field guesswork the type redesign closed.
- **The WebUI memory page is now a curation surface.** Change a memory's type,
  retire and restore it, show retired memories, and add a memory or a domain by
  hand. A memory you add yourself is recorded with `origin: user`, which the
  assistant sees.
- **Bulk curation.** Select many memories — or a whole domain at once from its
  header — and retype, retire, restore or delete them together. A domain that
  accumulated several hundred near-identical entries is the case the page exists
  for, and one row at a time is not a job anyone starts.
- **YAML export and import.** `GET /api/memory/{id}/export` downloads a full
  dump — domains, memories, every field, with a format version — and import
  loads one back in **merge** mode (add what is missing) or **replace** mode
  (wipe and load). IDs are re-minted on import, so a dump can be loaded into a
  different agent to seed it.
- **An automatic snapshot before every schema migration.** The database is
  copied to `<name>.pre-v<N>.db` beside itself before a migration runs, so an
  upgrade is recoverable without preparation. The snapshot is named for the
  version the store came *from*, which can differ between agents.
- **Cognitive-memory databases are migrated when the agent loads**, at startup
  and on config reload, rather than whenever each session next happens to be
  opened. Lazy migration spread a schema change across hours of ordinary use
  with no point an operator could call it done, and left a store belonging to an
  agent nobody talked to that day on the old schema indefinitely. Each upgrade
  is logged with its versions and the snapshot path, and a database that cannot
  be migrated is reported at startup instead of surfacing mid-conversation.

### Changed

- **BREAKING: the Gemini CLI provider is replaced by Antigravity (`agy`).**
  Google has deprecated the Gemini CLI. The `antigravity-cli` protocol takes its
  place, seeded as the **Antigravity CLI** provider and model, and the setup
  wizard detects `agy` alongside the other CLI agents.

  **`gemini-cli` remains accepted as an alias** and now runs `agy`, so an
  existing configuration keeps starting, keeps auto-starting the MCP host, and
  keeps working. New configurations should use `antigravity-cli`. The seeded
  Gemini model and its `GEMINI_CLI_TRUST_WORKSPACE` environment variable are
  gone; if you had customised that model, point it at the new provider.

  `set-mcp.sh` now registers claw with `agy mcp add` instead of `gemini mcp add`,
  and reports what to add for Cursor, which has no `mcp add` subcommand and is
  configured through `~/.cursor/mcp.json`.
- **A `retire` operation may omit its `evidence`.** Every memory operation had
  to cite a message in the current batch, which is right for anything that
  writes text — the rule exists to keep asserted memories anchored to something
  the user actually said. A retire asserts nothing and names a memory that must
  already exist, and housekeeping is by definition not raised by the current
  conversation, so the requirement made every tidy-up operation invalid. A
  single invalid operation rejects the whole payload, so an agent following the
  new rule above would have aborted entire consolidation runs.

- **BREAKING: the consolidation prompt is no longer overridable.** A workspace
  `COGMEM.md` used to replace it wholesale. Its contents are now **appended** to
  the built-in prompt instead, so the file holds instructions for that assistant
  — what to record, what to leave alone — while the rules and the output schema
  stay with the engine.

  The old arrangement made the machine contract operator-editable and froze it
  at whatever version each workspace was seeded with, so a change to the schema
  reached no existing agent. That survived by luck rather than design: a
  required field the old copy did not emit would have failed validation on every
  operation, and one invalid operation rejects the whole payload — silent, total
  consolidation failure across every agent, visible only in run records.

  **A `COGMEM.md` that is a copy of the built-in prompt is ignored**, with a
  warning naming the file, because appending one would show the model two
  contradictory output schemas. Every workspace seeded by an earlier version
  contains exactly that, so those agents fall back to the built-in prompt with
  no action needed: reduce the file to your own instructions, or delete it. New
  workspaces are seeded with a short commented stub instead of a copy of the
  prompt.
- **BREAKING: `cogmem_export` writes YAML, not Markdown.** The output moves from
  `files/MEMORY_EXPORT.md` to `files/MEMORY_EXPORT.yaml` and is the same format
  the WebUI exports — which means it can be read back. The Markdown projection
  could only be looked at.
- **Memory lines in the prompt now show their type**, so the assistant can tell
  a standing rule from a stale observation: `- (rule) Do not use the word
  "thuddy."` Previously only the text was shown.
- **The prompt tag `[source: …]` is now `[origin: …]`.** It always rendered
  `origin`; with no `source` field left, the old label was actively misleading.
- **BREAKING: existing agents keep their old consolidation prompt, and must be
  updated by hand.** Each agent workspace holds a `COGMEM.md` seeded from the
  shipped template and never overwritten afterwards — which is the point of it,
  but means an upgraded install keeps the prompt it was seeded with. That prompt
  still produces valid output, so nothing fails: the agent simply never records
  an `event` or `operational` memory, and the most useful part of this release
  never reaches it. Delete the seeded copies to pick up the current prompt:

  ```
  rm ~/.claw/agents/*/COGMEM.md      # or $CLAW_HOME/agents/*/COGMEM.md
  ```

  They are re-seeded from the new template on the next start. **If you have
  edited one, keep it and add the new types yourself** — the file is yours.
  ClawEh now logs a warning naming any agent whose prompt predates the new
  types, so a missed one is visible rather than silent.
- **Consolidation states a memory's type and nothing else.** It no longer sets
  `status` (there is no longer a choice) or `source` (gone), and an operation
  that omits a required field is rejected rather than silently defaulted.

### Fixed

- **BREAKING: CLI models no longer need `extra_args` set, and the flags are now
  supplied automatically.** Every CLI agent needs a flag to run unattended
  (`--dangerously-skip-permissions`, `--yolo`, and so on). It was stored on each
  model, could not be set anywhere in the Web UI, and left out it failed
  silently: the CLI auto-denies the tool call it cannot prompt for and reports a
  completed turn with an empty answer, so the assistant answers chat and goes
  mute the moment it tries to do anything. ClawEh now supplies each protocol's
  required flags itself and appends whatever `extra_args` adds, so a model
  written before this — or added through the Web UI — works without being
  edited. A model that already lists the flag does not get it twice.

  The breaking part is that the flags can no longer be removed by clearing
  `extra_args`. If you deliberately run a CLI sandboxed, say so and we will add
  the opt-out.

- **A CLI that refused a tool call reported success and said nothing.** The
  Antigravity CLI returns `status: SUCCESS` with an empty response and a
  `denied_actions` list when it is not allowed to act; ClawEh did not know the
  field existed and handed the empty answer on with no error and nothing in the
  log. It now fails with the refused action and the flag that fixes it.

- **BREAKING: deleting a provider silently changed the settings of others.**
  Configuration is loaded by overlaying the file onto the built-in defaults, and
  deleting a provider shifted every later entry onto a different default, which
  it then inherited unset fields from. Deleting `OpenAI` gave `OpenRouter Chat`
  Groq's `no_parallel_tool_calls` and `NVIDIA` OpenRouter Strict's
  `strict_compat` — wire-behaviour changes to providers nobody touched, made
  permanent by the next save. **Check your `providers` for `strict_compat` or
  `no_parallel_tool_calls` on an endpoint that should not have them** if you
  have ever deleted a provider; remove the key and restart.

- **The Providers page called a CLI provider configured whenever a path was
  filled in, without checking that the binary was there.** A provider pinned to
  a CLI that had since been upgraded or uninstalled showed a green dot and
  "Configured" while every request through it failed. The reverse was worse: a
  CLI provider with the **Command** field left blank — which is how the seeded
  config ships, and the more robust choice, since it follows the CLI across
  upgrades — showed as unconfigured even though it worked. The dot and the label
  now mean the binary actually resolves, decided in the ClawEh process against
  the `PATH` its subprocesses are launched with, and the card names the binary a
  blank command resolved to. CLI cards also carry a **Configured** /
  **Not configured** label at all: they previously rendered an empty space where
  every other card said what it was.
- **Adding a CLI provider no longer demands a command.** The field was
  mandatory, which forced a hard-coded path on every new CLI provider — the one
  configuration that breaks when the CLI is upgraded. Leave it blank and ClawEh
  runs the protocol's default binary from `PATH`. The field also no longer shows
  `/usr/local/bin/claude` as a placeholder, which suggested a path was expected
  and named the wrong CLI for three of the four protocols.
- **The wire-protocol picker offered `gemini-cli` and not `antigravity-cli`,**
  so there was no way to add an Antigravity provider through the Web UI. It now
  offers `antigravity-cli`; a provider already configured as `gemini-cli` keeps
  working — the backend treats it as an alias for the same binary — and still
  renders as a CLI provider.
- **`systemctl stop` and `systemctl restart` now shut ClawEh down gracefully.**
  Only `SIGINT` was handled, and systemd sends `SIGTERM`, whose default
  disposition kills the process outright — so every stop and restart skipped
  shutdown entirely: channels were never stopped cleanly, in-flight work was
  never drained, and the 15-second graceful-shutdown timeout was dead code on
  the one path production actually uses. Pressing Ctrl-C in a foreground run
  always worked, which is why it went unnoticed.
- **An operation that omitted both `status` and `source` was accepted and then
  defaulted to a combination the rules forbid** — `assistant_inferred` with
  `active`. Every guard tested for the fields being *wrong*, not missing. The
  fields are gone and the remaining ones are checked for presence.
- **Memories in `review` were unreachable.** They were excluded from the prompt,
  excluded from `cogmem_memory_search`, and excluded from the WebUI, and the
  digest that was meant to surface them showed a fixed top-eight by confidence —
  so 236 of the 244 had never been seen by anything. They are now active and
  visible.

## [0.4.72]

First release under the stable-compatibility policy: config schemas, tool names,
API shapes, and the device-gateway protocol are now things other installs depend
on, and breaking one is a deliberate decision rather than a free move.

### Security

- **`GET /api/config` no longer returns credentials.** It previously returned the
  whole configuration unmasked — provider API keys, bot tokens, device tokens,
  the WebUI channel token — on a surface with no operator authentication. Values
  are now masked (`sk-****cdef`). `PUT` and `PATCH` restore masked values from
  disk, so reading the config, editing it and writing it back does not destroy
  the credentials you never saw. Setting a genuinely new credential still writes
  through. Masking is driven by the JSON field name, so a credential added later
  is covered from the day it appears; list entries are matched by identity
  (`id`, `name`, `model_name`, `account`) so deleting or reordering bots,
  providers or models cannot move a credential onto the wrong entry.
- **The WebUI chat socket no longer disables its origin check, and no longer
  puts the token in the URL.** Setup wrote `channels.webui.allow_origins: ["*"]`
  into every install so a frontend dev server on port 5173 could connect — a
  development convenience that shipped to production and switched off the only
  defence against cross-site WebSocket hijacking, since CORS does not apply to
  WebSockets. It also set `allow_token_query: true`, putting the token in the
  URL where proxies, access logs, `Referer` headers and browser history record
  it. Setup no longer writes either.

  Empty `allow_origins` now means **same origin** (the `Origin` host must match
  the `Host` requested) rather than "allow any", which works however the
  operator reaches the UI — localhost, a LAN address, or a proxied hostname. An
  explicit list is still honoured verbatim, `"*"` included, which is what a
  frontend dev server needs.

  The browser now sends the token as a WebSocket subprotocol
  (`["claw-token", "<token>"]`) instead of a query parameter, because the
  browser WebSocket API cannot set an `Authorization` header. The server echoes
  only the `claw-token` marker, never the token. `Authorization: Bearer` still
  works for non-browser clients, and `allow_token_query` still works if set
  explicitly.

  **This affects `/webui/ws` only** — the browser console's chat socket. The
  device gateway on port 18791, which ClawToTalk and other OpenClaw-compatible
  apps use, authenticates inside the protocol rather than at the HTTP handshake
  and is untouched.

  **Existing installs carry `allow_origins: ["*"]` from a previous setup run.**
  Clear it (`"allow_origins": []` under `channels.webui`, or the Channels page)
  to pick up same-origin checking; nothing clears it for you.

- **The device gateway compares shared tokens in constant time with respect to
  length.** `subtle.ConstantTimeCompare` returns early when lengths differ, so
  the comparison leaked the secret's length. Both sides are now hashed to a fixed
  width first.

### Added

- **`file_count` — line, word, character and byte counts for a file, like
  `wc`.** Useful for sizing a file before reading it, and cheap enough to use as
  a change signal. Counts match `wc` exactly, verified in the tests against the
  real binary: `lines` is the newline count, so a file whose last line is
  unterminated reports one fewer than you would count by eye — `final_newline`
  in the result says which case you are in. `characters` counts Unicode
  characters and `bytes` counts bytes, which differ for non-ASCII text. The file
  is streamed, so it works on files too large for the read tools, and a file
  that is not valid UTF-8 is measured rather than rejected, with `invalid_utf8`
  marking the character count as untrustworthy.

- **Watch jobs: cron that only wakes the agent when something changed.**
  `cron_schedule` accepts `watch_tool`, `watch_args` and `watch_fields`. The
  named tool is called on the schedule **with no model in the loop**; the values
  at `watch_fields` are fingerprinted and compared with the previous run, and
  `message` is delivered only when they move. Polling "is there new mail?" no
  longer costs an LLM turn per check to conclude nothing happened.

  `watch_fields` are dot-paths into the tool's result. A path crossing a list
  applies to every element and collects the results, so `messages.id` is the set
  of ids currently present. Naming fields rather than hashing the whole result is
  what stops a probe firing on unread counts, timestamps and reordering. Omitting
  them compares the entire result.

  Behaviour worth knowing: the first run records a baseline **silently**, so
  creating a watch does not immediately report everything that already exists.
  A failed probe leaves the fingerprint untouched — advancing it would swallow
  the change that happened while the probe was broken — and after five
  consecutive failures the agent is told once, because a probe that has gone
  blind otherwise looks exactly like a quiet one. Probes run against the owning
  agent's tool registry, resolved on every run so a tool revoked in config stops
  being probed, and session-scoped tools are refused since a probe has no
  conversation to act on. Each probe is bounded at 60 seconds.

- **`claw install` now creates the `openclaw` symlink.** The Rabbit R1's
  `rabbit-agent` spawns `openclaw acp`, so the binary has to exist under that
  name. Only `make install` did this before, which meant an install from a
  release binary silently lacked the R1 path. Also documented: the README now
  has an **External devices** section covering both transports — the device
  gateway that OpenClaw-compatible apps such as ClawToTalk connect to, and the
  stdio ACP bridge the R1 uses — and how the bridge reaches the running gateway
  over loopback.
- **`--allowed-cidrs` accepts `private` and `any` as shorthands** for the RFC1918
  ranges and for any address, so the common headless choices do not require
  remembering three prefixes.
- **`gateway.allowed_cidrs` accepts `"*"`, meaning any address in either
  family.** `0.0.0.0/0` is the obvious thing to reach for and does not mean
  that — it is an IPv4 prefix, so on a dual-stack host it still refuses IPv6
  clients, which reads as the allowlist simply not working. CIDRs continue to
  mean exactly what they say (`0.0.0.0/0` is all IPv4, `::/0` is all IPv6);
  `"*"` is the unambiguous way to open it to everything, spelled the same as the
  wildcards in `allow_from` and `allow_origins`.

- **`claw network` — set who may reach the WebUI/API without editing
  config.json.** The recovery path for an install that listens on the network
  and then refuses every connection from it, which is what an empty
  `gateway.allowed_cidrs` looks like from a browser. With no argument it allows
  the private LAN ranges; it also takes a comma-separated CIDR list or one of
  `private`, `any`, `none`. `claw network --show` prints the current allowlist
  and changes nothing.

  ```
  claw network                    # 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16
  claw network 192.168.1.0/24     # one subnet
  claw network any                # any address — the WebUI has no password
  claw network none               # back to loopback only
  ```

  It writes the config and exits, so it is safe to run against a running
  gateway.

### Changed

- **`claw install` refuses a non-loopback `--host` without `--allowed-cidrs`.**
  That combination produces an install which listens on the network and then
  refuses every connection from it — indistinguishable from a firewall problem.
  It now fails at install time with the flags to fix it. An install whose config
  already carries an allowlist is unaffected.
- **BREAKING — an empty `gateway.allowed_cidrs` now means loopback only.** It
  previously fell back to the RFC1918 private ranges, so an install bound to
  `0.0.0.0` served the whole local network by default. Binding address and
  allowlist are now two independent gates: binding off-box makes ClawEh *listen*
  there, and the allowlist decides who is *served*.

  If you reach the WebUI from another machine and have never set
  `gateway.allowed_cidrs`, set it now or you will lose access on upgrade:

  ```json
  { "gateway": { "allowed_cidrs": ["192.168.1.0/24"] } }
  ```

  Use the three RFC1918 ranges (`10.0.0.0/8`, `172.16.0.0/12`,
  `192.168.0.0/16`) to restore the previous behaviour exactly, or `"*"` to allow
  any address. Loopback is always allowed. `claw install --allowed-cidrs` sets
  the same field, and the gateway logs a warning at startup when it is bound
  off-box with an empty allowlist.

- **Built with Go 1.27.1, and all Go dependencies updated to their latest
  releases.** Notable bumps: `github.com/mark3labs/mcp-go` 0.58.0 → 1.0.0,
  `modernc.org/sqlite` 1.57.0 → 1.58.0, `github.com/anthropics/anthropic-sdk-go`
  1.68.0 → 1.69.0, and `golang.org/x/crypto` 0.55.0 → 0.56.0. No config, tool,
  API or protocol behaviour changes with it; the `go` directive in `go.mod`
  stays at 1.26.7, so building from source still works with an older toolchain.
- **The version now carries a build number: `0.4.72+d4812df7 [20260902155301]`.**
  The commit identifies which source a binary came from, but a hash has no
  order, so it cannot answer "is the copy I am running newer than the one I just
  built?" — and a rebuild of the same commit is indistinguishable without it. The
  build number is the UTC link time (`yyyymmddhhmmss`), which always increases,
  compares correctly as plain text, and needs no version bump to change. It
  appears everywhere the version does: `claw version`, `claw status`, the startup
  log, the WebUI footer, `GET /api/system/version`, and the agent's system
  prompt.

  The release number and commit stay one unbroken token before the space, so a
  version truncated on paste still identifies its source. `SemVer()` is
  unchanged — bare `0.4.72` — so MCP, ACP and device-gateway handshakes, and the
  release tag, are untouched. A plain `go build` with no ldflags still reports a
  bare version.
- **`gateway.allowed_cidrs` now applies without a restart.** The allowlist was
  fixed for the lifetime of the listener, so widening it after locking yourself
  out meant restarting the service. A running gateway now picks the change up
  on its next config reload — about 15 seconds with the default interval and
  debounce — and because the listener is not recreated, open WebUI WebSocket
  connections survive it. An allowlist that
  fails to parse is refused and the running one is kept, rather than the reload
  dropping access to loopback.
- **The startup warning for a network bind with an empty allowlist now names the
  fix.** It previously suggested `0.0.0.0/0` for "any address", which is an IPv4
  prefix — it still refuses IPv6 clients, which on a dual-stack host reads as the
  allowlist being broken. It now points at `claw network` and, for the
  allow-everything case, at `"*"`, which covers both families.

### Removed

- **The `hw_i2c` and `hw_spi` tools.** Inherited from the picoclaw fork, where
  they drove sensors over the Linux I2C/SPI buses on the original SBC. They were
  off by default and unused. Remove `tools.i2c` / `tools.spi` from your config if
  present; unknown keys are ignored, so this is not a breaking change.
- **`docs/config.example.json` and `docs/env-example`.** The example config had
  drifted so far it no longer loaded, and described picoclaw's model shape rather
  than ClawEh's. ClawEh writes a complete `~/.claw/config.json` on first run,
  generated from the config types, so it cannot drift — that file is now the
  reference. Every variable in `env-example` was dead, including a Feishu channel
  this codebase has never had.

### Fixed

- Billing failures now surface OpenRouter's top-up URL, which it sends in
  `error.metadata.remedy_hint` rather than a `billing_url` field, and the
  provider is rechecked every 30 minutes.
- `docs/remote-access.md` described the WebUI and device gateway as sharing port
  18790. The device gateway is a separate listener on `channels.device.port`
  (default 18791); following the old text published the wrong port and left the
  gateway unreachable.
- `docs/tools_configuration.md` documented `use_bm25` / `use_regex` discovery
  keys and `tool_search_tool_*` tools that do not exist. The meta-tools are
  `search_tools` and `get_tool_details`, and discovery config lives at
  `tools.discovery`, not `tools.mcp.discovery`.
- `cmd/claw-auth/README.md` documented environment variables and a `-config`
  file that `claw-auth` has never read; OAuth client credentials come from the
  MCPFusion server. It also called the binary `fusion-oauth` throughout.
- A skills error lost its wrapped cause and read `skills directoryw %v`.
- **The gateway no longer crashes on the second config reload.** Any two config
  changes in the life of a process — two saves from the WebUI, two `claw network`
  runs, an edit to config.json followed by another — killed it with
  `panic: close of closed channel`. The mount watcher was stopped on every
  reload but only ever created at startup, so the second stop closed an
  already-closed channel. It is now rebuilt on reload like every other service,
  which also fixes the quieter half of the bug: after the first reload, external
  mount notifications had stopped firing for the rest of the process's life.
- **A dead MCP server is detected and reconnected again.** The liveness probe
  asked `Client.Ping`, which in `mark3labs/mcp-go` v1.0.0 returns success
  *without contacting the server* whenever the negotiated protocol is modern
  (2026-07-28 or later). Every MCP server therefore reported healthy forever,
  and a session that had gone away was never reconnected — the failure stayed
  invisible until a real tool call hit it. The probe now issues a `ListTools`
  round trip, which reaches the server on every protocol version.
- **`/ready` now reports ready.** It answered `503 not ready` for the entire
  life of every process. The readiness flag was only ever set by
  `health.Server.Start()`, which starts the health server's own listener — and
  the merged binary does not use it, registering the handlers on the shared mux
  instead. Readiness is now set once the listener is up and the channels have
  started, re-set after a config reload, and cleared on shutdown. `/health` is
  unchanged and still answers as soon as the port is open; the difference
  between the two is the point of having both.
- **The delete button on an agent card has an accessible name.** It was
  icon-only with no `aria-label`, so a screen reader announced nothing on the
  control that deletes an agent.
- **A successful memory consolidation no longer looks like a failure.** When the
  memory model proposed an inferred item as `active` instead of `review`, ClawEh
  corrected it, saved the memories correctly, and recorded a note saying so —
  but the note was stored in the run's `error` field, so the memory page showed
  it in red on a run that had actually succeeded. Runs now carry a separate
  `note`, shown as ordinary text; `error` means the run failed. Existing
  databases are migrated on open, and notes already stranded in `error` on
  successful runs are moved across, so the red text clears immediately rather
  than after the next run.
- **The WebUI devices page no longer fails intermittently with
  `store open failed`.** Roughly one gateway restart in three, `GET /api/devices`
  returned a 500 and the page showed an error. Opening the device pairing
  database re-set `journal_mode=WAL` on every call, and CONVERTING a database to
  WAL takes an exclusive lock that `busy_timeout` does not cover — SQLite
  returns `SQLITE_BUSY` immediately — so the admin API lost the race against the
  device channel opening the same file at startup. The mode is a property of the
  file and now it is only converted when it is not already WAL, with a short
  retry, and a database that stays on the rollback journal is logged rather than
  failing the open. The API also keeps one database handle for the process
  instead of opening and closing one per request.
- **The agent Tools page no longer shows a raw translation key as a heading.**
  Tools in the `common` and `memory` categories were grouped under
  `pages.agent.tools.categories.common` and `….memory`, because the catalog
  defines those categories and the UI had no labels for them. They now read
  "Common Directory" and "Memory".
- **Typing a channel's `allow_from` list no longer eats trailing separators.**
  The WebUI field resynced itself from the parsed value on every keystroke, so a
  `,` or space typed to start the next entry disappeared as soon as it was
  entered, and the entry had to be worked around rather than typed. Affects the
  Telegram, Slack and generic channel forms.

[0.5.0]: https://github.com/PivotLLM/ClawEh/compare/0.4.72...0.5.0
[0.4.72]: https://github.com/PivotLLM/ClawEh/compare/0.4.70...0.4.72
