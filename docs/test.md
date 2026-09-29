# Testing ClawEh

How the test suite is organised, how to run it, and where to look when
something fails.

## The gate

`make test` is the one command that decides whether a change is acceptable.
`make` (or `make all`) runs it before building, so a failing suite never
produces a binary. It runs, in order:

- `go generate ./...`
- `gofmt`/`gofumpt` formatting check (`make fmt-check`)
- `go vet ./...`
- `golangci-lint` (`make lint`)
- `govulncheck ./...` (`make govulncheck`; installs a pinned binary into
  `bin/` when missing, needs network access to the vulnerability database, and
  fails the gate on a vulnerability reachable from the code)
- `./test.sh`, the suite proper (below)

The exit code is 0 only when every stage passes.

## What `test.sh` runs

The script has three sections, printed in this order, then a single summary.

**Go unit tests.** `go test -race -coverprofile -count=1 ./...` over every
package. The race detector is on by default, and overall coverage must be at
least 50% (`COVERAGE_MIN` in `test.sh`).

**Frontend.** For the SPA under `web/frontend`: TypeScript typecheck
(`tsc -b --noEmit`), unit tests (`pnpm run test`), and `oxlint`. A missing
`pnpm` or `node_modules` fails the stage (and the run), naming the fix: `make
frontend-deps`, or `pnpm install --frozen-lockfile` in `web/frontend`; the gate
does not install them itself. A missing `oxlint` skips only the lint step.

**MCP integration.** Builds the binary, starts a real ClawEh instance in a temporary
`CLAW_HOME` with the MCP host enabled and one Fusion service (the MCPFusion
module's `wxca` sample), drives it with the `probe` tool (MCPProbe) and checks
workspace, PID-file and restart behaviour, then tears everything down. Section 8
of `tests/test_mcpserver.sh` checks per-agent Fusion gating on the running
binary: the agent whose `mcp_tools` lists the service can call its tools, the
agent with Fusion on and nothing listed is refused. `probe` must be on `PATH`
(or set `PROBE_PATH`); if it is not found this section counts as a failure,
because the full suite did not run.

**Effective capabilities (part of the Go tests).** `tests/capabilities` loads a
production-shaped fixture and pins, per agent and per CLI model, what it may do:
native tools, suites, the Fusion tools the real engine registers, MCP grants,
CLI command lines and environment. It fails when any of that changes until
`testdata/effective.golden` is regenerated (`UPDATE_GOLDEN=1 go test
./tests/capabilities/`), which is the moment to write the BREAKING changelog
entry. See "Capability changes" in `CLAUDE.md`.

**CLI provider smoke (opt-in).** With `CLAW_TEST_CLI=1`, `test.sh` also runs
`tests/test_cli_provider.sh` against the built binary: one turn through a real
Claude CLI with "Bypass CLI restrictions" off, asking for a tool call the CLI
must approve. It passes when the tool ran or when the turn ended with the
"declined to use tools" error naming the setting; silence or a timeout fails.
It costs one model call on the CLI account, so it is off by default and the
summary prints `CLI smoke: skipped`. `CLAW_TEST_CLI_COMMAND` names another CLI
binary.

Useful flags:

| Flag | Effect |
|---|---|
| `-f` | Fast: no race detector, no coverage. For quick iteration only. |
| `-c` | Coverage only, no race detector. |
| `-s` | Skip the MCP integration section. |
| `-x` | Keep test artifacts (coverage file, integration home and ClawEh log). |
| `-n` | No colour. |

## Where to look when it fails

Read the output from the bottom up.

**TEST SUMMARY** (last block on screen) gives the verdict per section:
Go package counts, frontend, MCP integration, coverage, and finally
`All tests passed!` or `FAILURES DETECTED`.

**Failed Go tests** appears inside the summary whenever a Go package failed.
It lists each failed test with its package, source file and line, and a
paste-ready rerun command, for example:

```
✗ github.com/PivotLLM/ClawEh/agent  TestRecordLastChannel  loop_test.go:82
    go test -race -count=1 -run '^TestRecordLastChannel$' ./agent
```

A compile error shows as `(build failed)` and a crash outside any test as
`(panic)`, each with a package-level rerun command.

**FAILURE DETAILS** is printed after the package results, before the frontend
section. It re-prints, per failed package, exactly what `go test` emitted for
that package: the `--- FAIL:` lines, the `t.Error` messages, panic stack
traces, data-race reports and compiler errors. The same text is written to
`.test-failures.log` in the repository root (gitignored, rewritten on every run,
absent after a clean run), so it can be opened after a long run instead of
scrolling.

**Frontend** failures print the tool's own output directly above the
`typecheck FAILED`, `unit tests FAILED` or `lint FAILED` line.

**MCP integration** failures print one `FAIL:` line per check in that section.
ClawEh's log for the run is in the temporary directory, which is deleted
unless you pass `-x`; the path is printed when artifacts are kept.

**Coverage** below the minimum fails the run with a one-line message in the
summary. `make test-coverage` prints per-package percentages and
`make test-cover-html` writes `coverage.html`.

## Other test targets

These are deliberately outside `make test` because they bind ports, need extra
tools, or need a running instance:

- `make test-maestro-host`: runs Maestro's MCP regression suite against a live
  ClawEh with Maestro embedded (needs `probe`, `jq`, `zip`).
- `make check-webui`: the browser end-to-end plan in
  `tests/frontend-e2e.mjs`, following `docs/webui-test-plan.md`, against a
  running WebUI.
- `make lint`: `golangci-lint` with the repository configuration. The binary
  must be built with the current Go toolchain
  (`go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@<version>`);
  the Makefile finds it in `$(go env GOPATH)/bin` when it is not on `PATH`.
- `make test-race`, `make test-coverage`, `make test-cover-html`,
  `make frontend-test`: individual pieces of the suite for focused runs.

## Conventions

- Go tests use the standard library `testing` package, `-race`, and
  `-count=1` (no cache), per `~/.claude/standards/go-tests.md`.
- Tests must not modify the source tree; use `t.TempDir()` for files.
- A known bug is never pinned by a test that asserts the buggy behaviour. Write
  the test for the correct behaviour and fix the bug, or leave it untested and
  report it.
