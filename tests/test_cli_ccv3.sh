#!/usr/bin/env bash
# A model's tool call over MCP (opt-in: CLAW_TEST_CLI=1, run by test.sh while
# the integration gateway is up).
#
# The probe suite calls the ccv3 tool itself (tests/test_mcpserver.sh, 4h), and
# the CLI smoke (tests/test_cli_provider.sh) runs a real CLI turn with the CLI's
# own tools. Neither shows a *model* reaching one of claw's tools over MCP from
# a CLI provider: the system prompt's session token, the CLI's MCP connection
# back into the gateway, the MCP host's gating of the agent's suite, and the
# tool itself. This check does. The probe asks main's agent_spawn to run a turn
# on the agent "cli" (a Claude CLI model whose mcp.json names this gateway's
# MCP host and whose only allowed tool is mcp__claw__ccv3; "Allow CLI to bypass
# restrictions" off), telling it to create a card. It passes when the card file
# exists in the gateway's library and the gateway logged the tool's write; the
# spawn's reply alone is not evidence (the model may say it did something it
# did not), and a turn that ends with no file fails.
#
# Costs one model call on the operator's CLI account, hence opt-in.
#
# Environment (set by test.sh): PROBE_PATH, MCP_URL (…/internal), SESSION_TOKEN
# (main's), CLAW_HOME (the gateway's), GATEWAY_LOG.
set -uo pipefail

for v in PROBE_PATH MCP_URL SESSION_TOKEN CLAW_HOME GATEWAY_LOG; do
    if [ -z "${!v:-}" ]; then
        echo "FAIL: $v is not set" >&2
        exit 2
    fi
done

CARD_ID="probe-cli-$(printf '%06d' "$$")"
CARD_FILE="$CLAW_HOME/cards/$CARD_ID.json"
TASK="Call the ccv3 tool with action create, id $CARD_ID and card {\"name\": \"Probe\", \"description\": \"Created by the CLI smoke test.\"}. Then reply with exactly: created $CARD_ID"
PARAMS=$(python3 -c '
import json, sys
print(json.dumps({"task": sys.argv[1], "mode": "wait", "agent_id": "cli", "session_token": sys.argv[2]}))
' "$TASK" "$SESSION_TOKEN")

echo "CLI tool call over MCP: agent_spawn → cli (Claude CLI) → mcp__claw__ccv3 create $CARD_ID"
OUT=$("$PROBE_PATH" -url "$MCP_URL" -transport http -call-timeout 4m -call "agent_spawn" -params "$PARAMS" 2>&1)
RC=$?
printf '%s\n' "$OUT" | grep -v '^\s*$' | tail -4 | sed 's/^/    /'

if [ $RC -ne 0 ]; then
    echo "FAIL: the probe call failed (exit $RC)"
    exit 1
fi
if [ ! -f "$CARD_FILE" ]; then
    echo "FAIL: the model's turn ended without creating $CARD_FILE"
    grep -i "ccv3\|declined\|mcp" "$GATEWAY_LOG" | tail -n 12 | sed 's/^/    /'
    exit 1
fi
if ! grep -q '"name": "Probe"' "$CARD_FILE"; then
    echo "FAIL: $CARD_FILE does not hold the card the model was asked for"
    sed 's/^/    /' "$CARD_FILE" | head -20
    exit 1
fi
if ! grep -q "Card created.*$CARD_ID" "$GATEWAY_LOG"; then
    echo "FAIL: the gateway log has no ccv3 create for $CARD_ID (the file exists, so something else wrote it)"
    exit 1
fi
echo "PASS: the CLI called ccv3 over MCP and the card is in the library"
exit 0
