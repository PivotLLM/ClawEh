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
  is covered from the day it appears.
- **The device gateway compares shared tokens in constant time with respect to
  length.** `subtle.ConstantTimeCompare` returns early when lengths differ, so
  the comparison leaked the secret's length. Both sides are now hashed to a fixed
  width first.

### Added

- **`gateway.allowed_cidrs` accepts `"*"`, meaning any address in either
  family.** `0.0.0.0/0` is the obvious thing to reach for and does not mean
  that — it is an IPv4 prefix, so on a dual-stack host it still refuses IPv6
  clients, which reads as the allowlist simply not working. CIDRs continue to
  mean exactly what they say (`0.0.0.0/0` is all IPv4, `::/0` is all IPv6);
  `"*"` is the unambiguous way to open it to everything, spelled the same as the
  wildcards in `allow_from` and `allow_origins`.

### Changed

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
