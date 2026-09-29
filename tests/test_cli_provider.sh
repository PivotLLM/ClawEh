#!/usr/bin/env bash
# CLI provider smoke test (opt-in: CLAW_TEST_CLI=1).
#
# Starts one turn through a real Claude CLI configured the way an operator gets
# it out of the box: "Bypass CLI restrictions" off, so whether the CLI's tool
# calls run depends on the CLI's own permission settings. The turn asks for a
# tool call that needs the CLI's permission. Two outcomes pass: the tool ran
# (the CLI allowed it), or the turn ended with claw's "declined to use tools"
# error naming the setting (the CLI refused and claw said so). Anything else —
# silence, an apology with no cause, a timeout — fails, because that is the
# failure mode this test exists to keep out (an agent's job failing with only
# the model's prose as evidence, 2026-09-28).
#
# Costs one model call on the operator's CLI account, hence opt-in.
#
#   CLAW_TEST_CLI=1 ./test.sh                # as part of the gate
#   tests/test_cli_provider.sh build/claw    # standalone, with a claw binary
#
# Environment:
#   CLAW_TEST_CLI_COMMAND  the CLI binary (default: claude)
set -uo pipefail

CLAW_BIN="${1:-}"
if [ -z "$CLAW_BIN" ] || [ ! -x "$CLAW_BIN" ]; then
    echo "usage: $0 <claw binary>" >&2
    exit 2
fi
CLI_CMD="${CLAW_TEST_CLI_COMMAND:-claude}"
if ! command -v "$CLI_CMD" >/dev/null 2>&1; then
    echo "FAIL: CLI '$CLI_CMD' not on PATH (set CLAW_TEST_CLI_COMMAND)"
    exit 1
fi

HOME_DIR=$(mktemp -d -t claw-cli-smoke.XXXXXX)
trap 'rm -rf "$HOME_DIR"' EXIT
cat > "$HOME_DIR/config.json" <<CFG
{
  "providers": [{"name": "Claude CLI", "protocol": "claude-cli", "command": "$(command -v "$CLI_CMD")"}],
  "models": [{"model_name": "Claude CLI", "model": "claude-cli", "provider": "Claude CLI", "enabled": true, "request_timeout": 180}],
  "agents": {
    "defaults": {"models": ["Claude CLI"]},
    "list": [{"id": "main", "name": "main", "default": true, "tools": ["*"]}]
  }
}
CFG
chmod 600 "$HOME_DIR/config.json"
# The CLI runs in <CLAW_HOME>/cli; the service creates it at start, this
# one-shot command does not.
mkdir -p "$HOME_DIR/cli"

PROMPT="Use the Write tool to create a file named probe.txt in the current directory containing the single word hello, then reply with exactly: wrote probe.txt"
echo "CLI provider smoke: $CLI_CMD, Bypass CLI restrictions off"
OUT=$(CLAW_HOME="$HOME_DIR" timeout 240 "$CLAW_BIN" agent -m "$PROMPT" 2>"$HOME_DIR/stderr.log")
RC=$?
LAST=$(printf '%s\n' "$OUT" | grep -v '^\s*$' | tail -3)
echo "$LAST" | sed 's/^/    /'

if [ $RC -eq 124 ]; then
    echo "FAIL: the turn timed out"
    exit 1
fi
if printf '%s' "$OUT" | grep -q "declined to use tools"; then
    echo "PASS: the CLI refused the tool call and claw reported it, naming the setting"
    exit 0
fi
if printf '%s' "$OUT" | grep -qi "wrote probe.txt" && [ -f "$HOME_DIR/cli/probe.txt" ]; then
    echo "PASS: the CLI allowed the tool call and it ran"
    exit 0
fi
echo "FAIL: neither a completed tool call nor the declined-tools error (exit $RC)"
tail -n 20 "$HOME_DIR/stderr.log" | sed 's/^/    /'
exit 1
