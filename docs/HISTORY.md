# Project history

ClawEh began as a fork of [PicoClaw](https://github.com/sipeed/picoclaw), chosen for its performant, easy to deploy, and maintainable Go foundation. The original focus was fixing issues and contributing upstream; a growing PR backlog and the prioritisation of new features over core stability made it clear that upstream was unlikely to meet ClawEh's needs, so it became a separate, independently maintained project. This is not a criticism of the PicoClaw authors; it reflects different priorities.

Original code remains under the MIT License, and ClawEh continues under the MIT License as well. `LICENSE` carries both copyright notices. Nothing about this project is intended to hold work back from the community: if others find parts of ClawEh useful, they are welcome to reuse, adapt, and build on them under the same MIT terms.

## Upstream contributions before the fork

These fixes and improvements were contributed to PicoClaw before ClawEh started. They are kept here for context and to explain some of the technical and maintenance goals behind ClawEh.

| PR                                                    | Change                                                       |
| ----------------------------------------------------- | ------------------------------------------------------------ |
| [#1460](https://github.com/sipeed/picoclaw/pull/1460) | fix(openai_compat): fix tool call serialization for strict OpenAI-compatible providers |
| [#1479](https://github.com/sipeed/picoclaw/pull/1479) | fix(claude_cli): surface stdout in error when CLI exits non-zero |
| [#1480](https://github.com/sipeed/picoclaw/pull/1480) | docs: document claude-cli and codex-cli providers in README  |
| [#1625](https://github.com/sipeed/picoclaw/pull/1625) | feat(channels): support multiple named Telegram bots         |
| [#1633](https://github.com/sipeed/picoclaw/pull/1633) | feat(providers): add gemini-cli provider                     |
| [#1637](https://github.com/sipeed/picoclaw/pull/1637) | fix(agent): dispatch per-candidate provider in fallback chain |
| [#1810](https://github.com/sipeed/picoclaw/pull/1810) | fix(launcher): recognise gemini-cli as a credential-free CLI provider |
| [#1811](https://github.com/sipeed/picoclaw/pull/1811) | fix(launcher): detect and display externally-managed gateway as running |
| [#1812](https://github.com/sipeed/picoclaw/pull/1812) | fix(claude-cli): pass system prompt via stdin instead of CLI argument |
| [#1813](https://github.com/sipeed/picoclaw/pull/1813) | fix(providers): robust CLI tool call extraction and mixed response handling |
| [#1814](https://github.com/sipeed/picoclaw/pull/1814) | fix(subagent): dispatch subagents through per-agent provider; enforce allowlist on self-spawn; attribute responses |
| [#1816](https://github.com/sipeed/picoclaw/pull/1816) | fix(cron): show all payload fields in cron list output       |
| [#1839](https://github.com/sipeed/picoclaw/pull/1839) | fix(cron): route cron jobs to correct agent and publish response to channel |
| [#1842](https://github.com/sipeed/picoclaw/pull/1842) | fix(cron): reload store on external file change; only save when state changes |
| [#1847](https://github.com/sipeed/picoclaw/pull/1847) | fix(providers): honour request_timeout for CLI providers with clear timeout errors and fallback |
