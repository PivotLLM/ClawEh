#!/bin/sh
#
# set-mcp.sh — register (or refresh) the ClawEh MCP server in whichever local AI
# CLIs are installed: Gemini CLI (gemini), Codex CLI (codex), and Claude Code
# (claude). Run it after installing/updating any of those CLIs, or after changing
# the ClawEh MCP port. CLIs that are not on your PATH are skipped.
#
# ENDPOINT — ClawEh exposes two MCP endpoints (see docs/mcp.md):
#     /mcp       standard bearer auth (Authorization: Bearer <token>)
#     /internal  session_token parameter — ClawEh's multi-assistant routing
#   A local CLI that authenticates as MULTIPLE agents must use /internal: it
#   supplies a per-agent session_token on each call, so one CLI install can act
#   as several agents. That is what this script configures.
#
# PORT — the URL below must match the MCP host `listen` port in your ClawEh config
#   (tools → mcp_host → listen, e.g. 127.0.0.1:5911). If you change that port,
#   update CLAW_MCP_URL here, or export it before running:
#       CLAW_MCP_URL=http://127.0.0.1:6000/internal ./set-mcp.sh

CLAW_MCP_URL="${CLAW_MCP_URL:-http://127.0.0.1:5911/internal}"

set -u

have() { command -v "$1" >/dev/null 2>&1; }

echo "ClawEh MCP endpoint: $CLAW_MCP_URL"
echo ""

# Each CLI is refreshed one at a time (remove, then add, then list) and only when
# its binary is on the PATH.

if have gemini; then
    echo "== Gemini CLI =="
    gemini mcp remove claw --scope user 2>/dev/null || true
    gemini mcp add claw "$CLAW_MCP_URL" --scope user --transport http
    gemini mcp list
    echo ""
else
    echo "== Gemini CLI: 'gemini' not on PATH — skipping =="
    echo ""
fi

if have codex; then
    echo "== Codex CLI =="
    codex mcp remove claw 2>/dev/null || true
    codex mcp add claw --url "$CLAW_MCP_URL"
    codex mcp list
    echo ""
else
    echo "== Codex CLI: 'codex' not on PATH — skipping =="
    echo ""
fi

if have claude; then
    echo "== Claude Code =="
    claude mcp remove claw 2>/dev/null || true
    claude mcp add --transport http claw --scope user "$CLAW_MCP_URL"
    claude mcp list
    echo ""
else
    echo "== Claude Code: 'claude' not on PATH — skipping =="
    echo ""
fi

echo "Done."
