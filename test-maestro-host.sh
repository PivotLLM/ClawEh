#!/usr/bin/env bash
# test-maestro-host.sh — run Maestro's MCP regression suite against a live
# ClawEh gateway (Maestro embedded, host mode).
#
# It builds claw and a deterministic stub model server, starts a gateway in a
# temporary CLAW_HOME with one Maestro-enabled agent, issues a service token,
# runs Maestro's test.sh (from the pinned Maestro module) with MODE=host over
# the /mcp endpoint, then stops everything and cleans up.
#
# Prerequisites (not installed by this script): probe (MCPProbe), jq, zip.
# Usage: ./test-maestro-host.sh [-k]   (-k keeps the temporary directory)
set -u

ROOT="$(cd "$(dirname "$0")" && pwd)"
KEEP=false
while getopts "k" opt; do
    case $opt in
        k) KEEP=true ;;
        *) echo "usage: $0 [-k]"; exit 2 ;;
    esac
done

: "${PROBE:=probe}"
for bin in "$PROBE" jq zip go; do
    if ! command -v "$bin" >/dev/null 2>&1; then
        echo "ERROR: required tool '$bin' not found in PATH" >&2
        exit 2
    fi
done

TMP="$(mktemp -d "${TMPDIR:-/tmp}/claw-maestro-host.XXXXXX")"
HOME_DIR="$TMP/home"
AGENT="maestro-test"
CLAW_PID=""
STUB_PID=""

cleanup() {
    [ -n "$CLAW_PID" ] && kill "$CLAW_PID" 2>/dev/null && wait "$CLAW_PID" 2>/dev/null
    [ -n "$STUB_PID" ] && kill "$STUB_PID" 2>/dev/null && wait "$STUB_PID" 2>/dev/null
    if [ "$KEEP" = true ]; then
        echo "Temporary directory kept: $TMP"
    else
        rm -rf "$TMP"
    fi
}
trap cleanup EXIT INT TERM

free_port() {
    python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1])'
}

echo "== Building claw and the stub model server"
(cd "$ROOT" && go build -o "$TMP/claw" . && go build -o "$TMP/stubllm" ./tools/maestro/testdata/stubllm) || exit 1

STUB_PORT="$(free_port)"
GW_PORT="$(free_port)"
MCP_PORT="$(free_port)"

echo "== Starting stub model server on :$STUB_PORT"
"$TMP/stubllm" -listen "127.0.0.1:$STUB_PORT" > "$TMP/stubllm.log" 2>&1 &
STUB_PID=$!

mkdir -p "$HOME_DIR/agents/$AGENT"
cat > "$HOME_DIR/config.json" <<EOF
{
  "gateway": {"host": "127.0.0.1", "port": $GW_PORT},
  "providers": [
    {"name": "stub", "protocol": "openai-chat", "base_url": "http://127.0.0.1:$STUB_PORT/v1", "api_key": "test"}
  ],
  "models": [
    {"model_name": "stub", "model": "stub-1", "provider": "stub", "enabled": true}
  ],
  "channels": {"webui": {"enabled": true, "token": "maestro-host-test-token"}},
  "agents": {
    "defaults": {"model": "stub", "restrict_to_workspace": true},
    "list": [
      {"id": "$AGENT", "default": true, "models": ["stub"], "workspace": "$HOME_DIR/agents/$AGENT",
       "maestro": {"enabled": true}}
    ]
  },
  "mcp_host": {"enabled": true, "listen": "127.0.0.1:$MCP_PORT", "endpoint_path": "/mcp"}
}
EOF

# Issue the service token before the gateway starts: it loads the token file
# at startup and only re-reads it on its reload interval.
echo "== Issuing a service token for $AGENT"
TOKEN="$(CLAW_HOME="$HOME_DIR" "$TMP/claw" token issue "$AGENT" | awk '/^  [^ ]+$/ {print $1; exit}')"
if [ -z "$TOKEN" ]; then
    echo "ERROR: could not obtain a service token" >&2
    exit 1
fi

echo "== Starting claw (CLAW_HOME=$HOME_DIR, gateway :$GW_PORT, mcp :$MCP_PORT)"
CLAW_HOME="$HOME_DIR" "$TMP/claw" > "$TMP/claw.out" 2>&1 &
CLAW_PID=$!

for i in $(seq 1 60); do
    if curl -s -o /dev/null "http://127.0.0.1:$MCP_PORT/mcp"; then
        break
    fi
    if ! kill -0 "$CLAW_PID" 2>/dev/null; then
        echo "ERROR: claw exited early; output:" >&2
        cat "$TMP/claw.out" >&2
        exit 1
    fi
    sleep 1
done
if ! curl -s -o /dev/null "http://127.0.0.1:$MCP_PORT/mcp"; then
    echo "ERROR: MCP host did not come up on :$MCP_PORT" >&2
    cat "$TMP/claw.out" >&2
    exit 1
fi

# MAESTRO_TEST_SH overrides the script location (e.g. a Maestro checkout when
# iterating on the suite); the default is the pinned Maestro module.
if [ -z "${MAESTRO_TEST_SH:-}" ]; then
    MAESTRO_DIR="$(cd "$ROOT" && go list -m -f '{{.Dir}}' github.com/PivotLLM/Maestro)"
    MAESTRO_TEST_SH="$MAESTRO_DIR/test.sh"
fi
if [ ! -f "$MAESTRO_TEST_SH" ]; then
    echo "ERROR: Maestro test.sh not found at $MAESTRO_TEST_SH" >&2
    exit 1
fi
cp "$MAESTRO_TEST_SH" "$TMP/maestro-test.sh"
chmod +x "$TMP/maestro-test.sh"

echo "== Running Maestro test.sh in host mode against http://127.0.0.1:$MCP_PORT/mcp"
MODE=host \
MCP_URL="http://127.0.0.1:$MCP_PORT/mcp" \
MCP_TOKEN="$TOKEN" \
HOST_DATA="$HOME_DIR/agents/$AGENT/maestro" \
PROBE="$PROBE" \
"$TMP/maestro-test.sh"
RC=$?

echo "== Checking that Maestro's log reached claw.log"
if grep -q '"component":"maestro"\|\[maestro\]\|maestro' "$HOME_DIR/logs/claw.log" 2>/dev/null; then
    echo "PASS: maestro log lines present in claw.log"
else
    echo "FAIL: no maestro log lines in claw.log" >&2
    RC=1
fi
if [ -f "$HOME_DIR/agents/$AGENT/maestro/maestro.log" ]; then
    echo "FAIL: maestro.log was written although the log is routed to claw.log" >&2
    RC=1
fi

exit $RC
