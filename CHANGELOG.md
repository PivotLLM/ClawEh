# Changelog

All notable changes to ClawEh are recorded here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and
version numbers follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Entries describe what changed for someone **running or integrating with** ClawEh
— config keys, tool names, API shapes, protocol behaviour, defaults — not the
internal refactors behind them. A change nobody outside the repository can
observe does not need an entry.

## [Unreleased]

Nothing yet.

## [0.4.71]

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

[Unreleased]: https://github.com/PivotLLM/ClawEh/compare/0.4.71...HEAD
[0.4.71]: https://github.com/PivotLLM/ClawEh/compare/0.4.70...0.4.71
