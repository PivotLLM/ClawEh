#!/bin/bash
################################################################################
# ClawEh MCP Server Functional Tests
#
# Validates the claw MCP server (claw acting as an MCP host) over Streamable
# HTTP transport using the `probe` binary. Exercises tool discovery and a
# representative subset of allowlisted tools (list_dir, write_file, read_file,
# edit_file, append_file).
#
# REQUIREMENTS:
#   - A running claw gateway with mcp_host.enabled=true (default port 5911)
#   - The 'probe' binary on PATH (override with PROBE_PATH env var)
#   - The configured workspace must be writable by the test
#
# Configuration via .env (optional, in this directory) or env vars:
#   SERVER_URL   Base URL of the MCP host (default: http://127.0.0.1:5911)
#   ENDPOINT     Endpoint path (default: /mcp)
#   PROBE_PATH   Path to probe binary (default: probe)
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
FULL_URL="${SERVER_URL}${ENDPOINT}"

# Unique scratch file inside the agent workspace so repeated runs do not collide.
SCRATCH_REL="claw_mcp_test_$$.txt"

# Colors
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
    RED='\033[0;31m'
    GREEN='\033[0;32m'
    YELLOW='\033[1;33m'
    BLUE='\033[0;34m'
    CYAN='\033[0;36m'
    BOLD='\033[1m'
    NC='\033[0m'
else
    RED='' GREEN='' YELLOW='' BLUE='' CYAN='' BOLD='' NC=''
fi

PASS_COUNT=0
FAIL_COUNT=0

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

# run_test_ok <name> <tool> <params> [expected_substring]
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

# run_test_err <name> <tool> <params>
# Expects an MCP-level error (probe reports failure or response includes isError).
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

#-------------------------------------------------------------------------------
# Header
#-------------------------------------------------------------------------------

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

#-------------------------------------------------------------------------------
# Reachability
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
# Section 1: list_dir
#-------------------------------------------------------------------------------

print_section "1. list_dir"

run_test_ok "1.1 List workspace root" \
    "list_dir" '{"path":"."}'

#-------------------------------------------------------------------------------
# Section 2: write_file then read_file (round-trip)
#-------------------------------------------------------------------------------

print_section "2. write_file / read_file round trip"

PAYLOAD="hello-from-mcp-test-$$"

run_test_ok "2.1 write_file creates a scratch file" \
    "write_file" "{\"path\":\"$SCRATCH_REL\",\"content\":\"$PAYLOAD\"}"

run_test_ok "2.2 read_file returns the written payload" \
    "read_file" "{\"path\":\"$SCRATCH_REL\"}" "$PAYLOAD"

#-------------------------------------------------------------------------------
# Section 3: append_file
#-------------------------------------------------------------------------------

print_section "3. append_file"

APPENDED="appended-line-$$"

run_test_ok "3.1 append_file adds a new line" \
    "append_file" "{\"path\":\"$SCRATCH_REL\",\"content\":\"\n$APPENDED\"}"

run_test_ok "3.2 read_file shows appended content" \
    "read_file" "{\"path\":\"$SCRATCH_REL\"}" "$APPENDED"

#-------------------------------------------------------------------------------
# Section 4: edit_file
#-------------------------------------------------------------------------------

print_section "4. edit_file"

run_test_ok "4.1 edit_file replaces a substring" \
    "edit_file" "{\"path\":\"$SCRATCH_REL\",\"old_text\":\"$PAYLOAD\",\"new_text\":\"replaced-$$\"}"

run_test_ok "4.2 read_file confirms replacement" \
    "read_file" "{\"path\":\"$SCRATCH_REL\"}" "replaced-$$"

#-------------------------------------------------------------------------------
# Section 5: error paths
#-------------------------------------------------------------------------------

print_section "5. Error handling"

run_test_err "5.1 read_file on missing path returns an error" \
    "read_file" '{"path":"definitely_not_a_real_file_'$$'_xyz.txt"}'

run_test_err "5.2 unknown tool is rejected" \
    "definitely_not_a_real_tool_$$" '{}'

#-------------------------------------------------------------------------------
# Summary
#-------------------------------------------------------------------------------

echo ""
echo "${BOLD}============================================${NC}"
echo "${BOLD}   Test Summary${NC}"
echo "${BOLD}============================================${NC}"
echo ""
echo "  ${GREEN}Passed: $PASS_COUNT${NC}"
echo "  ${RED}Failed: $FAIL_COUNT${NC}"
echo "  Total:  $((PASS_COUNT + FAIL_COUNT))"
echo ""

if [ "$FAIL_COUNT" -gt 0 ]; then
    echo "${RED}SOME TESTS FAILED${NC}"
    exit 1
fi
echo "${GREEN}ALL TESTS PASSED${NC}"
exit 0
