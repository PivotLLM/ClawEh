#!/usr/bin/env bash
################################################################################
# ClawEh MCP Server Functional Tests
#
# Validates the claw MCP server (claw acting as an MCP host) over Streamable
# HTTP transport using the `probe` binary. Structured in two tiers:
#
# Tier 1 — Discovery and auth validation (always runs, no session_token needed):
#   Section 0: Reachability
#   Section 1: Tool catalogue
#   Section 2: Unauthenticated rejection
#
# Tier 2 — Authenticated tests (only when SESSION_TOKEN env var is set):
#   Section 3: File operations
#   Section 4: Session tool smoke tests
#
# REQUIREMENTS:
#   - A running claw gateway with mcp_host.enabled=true (default port 5911)
#   - The 'probe' binary on PATH (override with PROBE_PATH env var)
#   - The configured workspace must be writable by the test
#
# Configuration via tests/.env (optional) or env vars:
#   SERVER_URL      Base URL of the MCP host (default: http://127.0.0.1:5911)
#   ENDPOINT        Endpoint path (default: /mcp)
#   PROBE_PATH      Path to probe binary (default: probe)
#   SESSION_TOKEN   SST<64hex> token from an active claw session's system prompt
#
# Exit codes:
#   0   All tests passed
#   1   One or more tests failed
################################################################################

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [ -f "$SCRIPT_DIR/.env" ]; then
    # shellcheck disable=SC1091
    source "$SCRIPT_DIR/.env"
fi

SERVER_URL="${SERVER_URL:-http://127.0.0.1:5911}"
ENDPOINT="${ENDPOINT:-/mcp}"
PROBE_PATH="${PROBE_PATH:-probe}"
SESSION_TOKEN="${SESSION_TOKEN:-}"
FULL_URL="${SERVER_URL}${ENDPOINT}"

# Unique scratch file inside the agent workspace so repeated runs do not collide.
SCRATCH_REL="claw_mcp_test_$$.txt"

# Colors
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
    RED='\033[0;31m'
    GREEN='\033[0;32m'
    YELLOW='\033[1;33m'
    BLUE='\033[0;34m'
    BOLD='\033[1m'
    NC='\033[0m'
else
    RED='' GREEN='' YELLOW='' BLUE='' BOLD='' NC=''
fi

PASS_COUNT=0
FAIL_COUNT=0
TIER2_PASS=0
TIER2_FAIL=0

# Cleanup scratch files on exit.
cleanup() {
    if [ -n "${SCRATCH_REL:-}" ]; then
        # Best-effort: remove via write_file with empty content is not reliable
        # without auth, so only attempt if probe is available and SESSION_TOKEN set.
        :
    fi
}
trap cleanup EXIT

print_section() {
    echo ""
    echo "${BOLD}${BLUE}============================================${NC}"
    echo "${BOLD}${BLUE}   $1${NC}"
    echo "${BOLD}${BLUE}============================================${NC}"
    echo ""
}

# probe_call <tool> <params-json>
# Echoes raw output, returns probe's exit code.
probe_call() {
    local tool="$1"
    local params="$2"
    "$PROBE_PATH" -url "$FULL_URL" -transport http \
        -call "$tool" -params "$params" 2>&1
}

# probe_call_auth <tool> <params-json>
# Injects SESSION_TOKEN into params before calling. Requires SESSION_TOKEN set.
probe_call_auth() {
    local tool="$1"
    local params="$2"
    local augmented
    augmented=$(printf '%s' "$params" | python3 -c "
import json, sys
p = json.loads(sys.stdin.read())
p['session_token'] = '${SESSION_TOKEN}'
print(json.dumps(p))
")
    "$PROBE_PATH" -url "$FULL_URL" -transport http \
        -call "$tool" -params "$augmented" 2>&1
}

# run_test_ok <name> <tool> <params> [expected_substring]
# Expects "Tool call succeeded" in output.
run_test_ok() {
    local test_name="$1"
    local tool="$2"
    local params="$3"
    local expected="${4:-}"

    echo "  ${test_name}"
    local result
    result=$(probe_call "$tool" "$params")

    if echo "$result" | grep -q "Tool call succeeded"; then
        if [ -n "$expected" ]; then
            if echo "$result" | grep -qF "$expected"; then
                echo "    ${GREEN}PASS${NC}: found expected substring"
                PASS_COUNT=$((PASS_COUNT + 1))
            else
                echo "    ${RED}FAIL${NC}: expected '$expected' not found"
                echo "    Output: $result"
                FAIL_COUNT=$((FAIL_COUNT + 1))
            fi
        else
            echo "    ${GREEN}PASS${NC}: tool call succeeded"
            PASS_COUNT=$((PASS_COUNT + 1))
        fi
    else
        echo "    ${RED}FAIL${NC}: tool call failed unexpectedly"
        echo "    Output: $result"
        FAIL_COUNT=$((FAIL_COUNT + 1))
    fi
}

# run_test_ok_auth <name> <tool> <params> [expected_substring]
# Like run_test_ok but injects SESSION_TOKEN and tracks tier-2 counts.
run_test_ok_auth() {
    local test_name="$1"
    local tool="$2"
    local params="$3"
    local expected="${4:-}"

    echo "  ${test_name}"
    local result
    result=$(probe_call_auth "$tool" "$params")

    if echo "$result" | grep -q "Tool call succeeded"; then
        if [ -n "$expected" ]; then
            if echo "$result" | grep -qF "$expected"; then
                echo "    ${GREEN}PASS${NC}: found expected substring"
                TIER2_PASS=$((TIER2_PASS + 1))
                PASS_COUNT=$((PASS_COUNT + 1))
            else
                echo "    ${RED}FAIL${NC}: expected '$expected' not found"
                echo "    Output: $result"
                TIER2_FAIL=$((TIER2_FAIL + 1))
                FAIL_COUNT=$((FAIL_COUNT + 1))
            fi
        else
            echo "    ${GREEN}PASS${NC}: tool call succeeded"
            TIER2_PASS=$((TIER2_PASS + 1))
            PASS_COUNT=$((PASS_COUNT + 1))
        fi
    else
        echo "    ${RED}FAIL${NC}: tool call failed unexpectedly"
        echo "    Output: $result"
        TIER2_FAIL=$((TIER2_FAIL + 1))
        FAIL_COUNT=$((FAIL_COUNT + 1))
    fi
}

# run_test_err <name> <tool> <params>
# Expects an MCP-level error in the output (no session_token).
run_test_err() {
    local test_name="$1"
    local tool="$2"
    local params="$3"

    echo "  ${test_name}"
    local result
    result=$(probe_call "$tool" "$params")

    if echo "$result" | grep -qiE "Tool call failed|Failed to call tool|isError|\"error\"|not found"; then
        echo "    ${GREEN}PASS${NC}: tool returned an error as expected"
        PASS_COUNT=$((PASS_COUNT + 1))
    else
        echo "    ${RED}FAIL${NC}: expected an error but tool succeeded"
        echo "    Output: $result"
        FAIL_COUNT=$((FAIL_COUNT + 1))
    fi
}

# run_test_err_contains <name> <tool> <params> <substring>
# Expects an error whose output contains <substring> (case-insensitive).
run_test_err_contains() {
    local test_name="$1"
    local tool="$2"
    local params="$3"
    local substr="$4"

    echo "  ${test_name}"
    local result
    result=$(probe_call "$tool" "$params")

    if echo "$result" | grep -qi "$substr"; then
        echo "    ${GREEN}PASS${NC}: error contains '$substr'"
        PASS_COUNT=$((PASS_COUNT + 1))
    else
        echo "    ${RED}FAIL${NC}: expected error containing '$substr' but not found"
        echo "    Output: $result"
        FAIL_COUNT=$((FAIL_COUNT + 1))
    fi
}

# run_test_not_auth_err <name> <tool> <params>
# Calls tool WITH session_token and verifies output does NOT contain the
# "invalid or missing session_token" auth error. Tracks tier-2 counts.
run_test_not_auth_err() {
    local test_name="$1"
    local tool="$2"
    local params="$3"

    echo "  ${test_name}"
    local result
    result=$(probe_call_auth "$tool" "$params")

    if echo "$result" | grep -qi "invalid or missing session_token"; then
        echo "    ${RED}FAIL${NC}: got auth error — session_token not accepted"
        echo "    Output: $result"
        TIER2_FAIL=$((TIER2_FAIL + 1))
        FAIL_COUNT=$((FAIL_COUNT + 1))
    else
        echo "    ${GREEN}PASS${NC}: no auth error (token accepted)"
        TIER2_PASS=$((TIER2_PASS + 1))
        PASS_COUNT=$((PASS_COUNT + 1))
    fi
}

# run_test_err_auth <name> <tool> <params>
# Expects an MCP-level error WITH session_token. Tracks tier-2 counts.
run_test_err_auth() {
    local test_name="$1"
    local tool="$2"
    local params="$3"

    echo "  ${test_name}"
    local result
    result=$(probe_call_auth "$tool" "$params")

    if echo "$result" | grep -qiE "Tool call failed|Failed to call tool|isError|\"error\"|not found"; then
        echo "    ${GREEN}PASS${NC}: tool returned an error as expected"
        TIER2_PASS=$((TIER2_PASS + 1))
        PASS_COUNT=$((PASS_COUNT + 1))
    else
        echo "    ${RED}FAIL${NC}: expected an error but tool succeeded"
        echo "    Output: $result"
        TIER2_FAIL=$((TIER2_FAIL + 1))
        FAIL_COUNT=$((FAIL_COUNT + 1))
    fi
}

################################################################################
# Header
################################################################################

echo ""
echo "${BOLD}============================================${NC}"
echo "${BOLD}   ClawEh MCP Server Tests${NC}"
echo "${BOLD}============================================${NC}"
echo "Server:  $FULL_URL"
echo "Probe:   $PROBE_PATH"
echo "Scratch: $SCRATCH_REL"
echo ""

if ! command -v "$PROBE_PATH" > /dev/null 2>&1; then
    echo "${RED}ERROR: probe not found at '$PROBE_PATH'${NC}"
    exit 1
fi

################################################################################
# TIER 1 — Discovery and auth validation (no session_token required)
################################################################################

echo "${BOLD}--- TIER 1: Discovery and auth validation ---${NC}"

#-------------------------------------------------------------------------------
# Section 0: Reachability
#-------------------------------------------------------------------------------

print_section "0. Reachability"

echo "  0.1 List tools"
LIST_OUT=$("$PROBE_PATH" -url "$FULL_URL" -transport http -list 2>&1)
if [ $? -ne 0 ]; then
    echo "${RED}FAIL: probe could not connect to $FULL_URL${NC}"
    echo "$LIST_OUT"
    echo ""
    echo "${YELLOW}Hint: ensure claw gateway is running and mcp_host.enabled=true.${NC}"
    exit 1
fi
echo "$LIST_OUT" | sed 's/^/      /'
echo "    ${GREEN}PASS${NC}: server reachable, returned tool list"
PASS_COUNT=$((PASS_COUNT + 1))

#-------------------------------------------------------------------------------
# Section 1: Tool catalogue
#-------------------------------------------------------------------------------

print_section "1. Tool catalogue"

check_tool() {
    local num="$1"
    local tool_name="$2"
    echo "  ${num} Tool '${tool_name}' present"
    if echo "$LIST_OUT" | grep -qF "$tool_name"; then
        echo "    ${GREEN}PASS${NC}: found"
        PASS_COUNT=$((PASS_COUNT + 1))
    else
        echo "    ${RED}FAIL${NC}: not found in tool list"
        FAIL_COUNT=$((FAIL_COUNT + 1))
    fi
}

check_tool "1.1"  "read_file"
check_tool "1.2"  "write_file"
check_tool "1.3"  "edit_file"
check_tool "1.4"  "append_file"
check_tool "1.5"  "list_dir"
check_tool "1.6"  "web_fetch"
check_tool "1.7"  "web_search"
check_tool "1.8"  "send_file"
check_tool "1.9"  "get_session_messages"
check_tool "1.10" "search_session_messages"
check_tool "1.11" "compact_session"
check_tool "1.12" "get_session_info"

#-------------------------------------------------------------------------------
# Section 2: Unauthenticated rejection
#-------------------------------------------------------------------------------

print_section "2. Unauthenticated rejection"

run_test_err_contains "2.1 list_dir without session_token returns session_token error" \
    "list_dir" '{"path":"."}' "session_token"

run_test_err_contains "2.2 read_file without session_token returns session_token error" \
    "read_file" '{"path":"test.txt"}' "session_token"

run_test_err_contains "2.3 get_session_info without session_token returns session_token error" \
    "get_session_info" '{}' "session_token"

################################################################################
# TIER 2 — Authenticated tests (SESSION_TOKEN required)
################################################################################

echo ""
echo "${BOLD}--- TIER 2: Authenticated tests ---${NC}"

if [ -z "$SESSION_TOKEN" ]; then
    echo ""
    echo "${YELLOW}TIER 2 SKIPPED: SESSION_TOKEN is not set.${NC}"
    echo ""
    echo "To run Tier 2, set SESSION_TOKEN to the SST<64hex> token found in"
    echo "an active claw session's system prompt, then re-run this script:"
    echo ""
    echo "  SESSION_TOKEN=SST<64hex> $0"
    echo ""
else

    PAYLOAD="hello-from-mcp-test-$$"

    #---------------------------------------------------------------------------
    # Section 3: File operations (Tier 2)
    #---------------------------------------------------------------------------

    print_section "3. File operations (authenticated)"

    run_test_ok_auth "3.1 list_dir workspace root" \
        "list_dir" '{"path":"."}'

    run_test_ok_auth "3.2 write_file creates a scratch file" \
        "write_file" "{\"path\":\"$SCRATCH_REL\",\"content\":\"$PAYLOAD\"}"

    run_test_ok_auth "3.3 read_file returns the written payload" \
        "read_file" "{\"path\":\"$SCRATCH_REL\"}" "$PAYLOAD"

    APPENDED="appended-line-$$"

    run_test_ok_auth "3.4 append_file adds a new line" \
        "append_file" "{\"path\":\"$SCRATCH_REL\",\"content\":\"\n$APPENDED\"}"

    run_test_ok_auth "3.5 read_file shows appended content" \
        "read_file" "{\"path\":\"$SCRATCH_REL\"}" "$APPENDED"

    run_test_ok_auth "3.6 edit_file replaces a substring" \
        "edit_file" "{\"path\":\"$SCRATCH_REL\",\"old_text\":\"$PAYLOAD\",\"new_text\":\"replaced-$$\"}"

    run_test_ok_auth "3.7 read_file confirms replacement" \
        "read_file" "{\"path\":\"$SCRATCH_REL\"}" "replaced-$$"

    run_test_err_auth "3.8 read_file on missing path returns an error" \
        "read_file" '{"path":"definitely_not_a_real_file_'$$'_xyz.txt"}'

    run_test_err_auth "3.9 unknown tool is rejected" \
        "definitely_not_a_real_tool_$$" '{}'

    #---------------------------------------------------------------------------
    # Section 4: Session tool smoke tests (Tier 2)
    #---------------------------------------------------------------------------

    print_section "4. Session tool smoke tests (authenticated)"

    run_test_not_auth_err "4.1 get_session_info — token accepted" \
        "get_session_info" '{}'

    run_test_not_auth_err "4.2 compact_session — token accepted" \
        "compact_session" '{}'

    run_test_not_auth_err "4.3 get_session_messages — token accepted" \
        "get_session_messages" '{"seq_start":1,"seq_end":10}'

    run_test_not_auth_err "4.4 search_session_messages — token accepted" \
        "search_session_messages" '{"query":"test"}'

fi  # end SESSION_TOKEN block

################################################################################
# Summary
################################################################################

TIER1_PASS=$((PASS_COUNT - TIER2_PASS))
TIER1_FAIL=$((FAIL_COUNT - TIER2_FAIL))

echo ""
echo "${BOLD}============================================${NC}"
echo "${BOLD}   Test Summary${NC}"
echo "${BOLD}============================================${NC}"
echo ""
echo "  Tier 1 — ${GREEN}Passed: $TIER1_PASS${NC}  ${RED}Failed: $TIER1_FAIL${NC}"
if [ -n "$SESSION_TOKEN" ]; then
    echo "  Tier 2 — ${GREEN}Passed: $TIER2_PASS${NC}  ${RED}Failed: $TIER2_FAIL${NC}"
else
    echo "  Tier 2 — ${YELLOW}SKIPPED${NC} (SESSION_TOKEN not set)"
fi
echo ""
echo "  Overall — ${GREEN}Passed: $PASS_COUNT${NC}  ${RED}Failed: $FAIL_COUNT${NC}  Total: $((PASS_COUNT + FAIL_COUNT))"
echo ""

if [ "$FAIL_COUNT" -gt 0 ]; then
    echo "${RED}SOME TESTS FAILED${NC}"
    exit 1
fi
echo "${GREEN}ALL TESTS PASSED${NC}"
exit 0
