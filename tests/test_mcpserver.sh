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
#   ENDPOINT        Session-token-parameter endpoint path (default: /internal)
#   BEARER_ENDPOINT Optional bearer endpoint path (e.g. /mcp); when set with
#                   SESSION_TOKEN, Tier 3 exercises Authorization: Bearer auth
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
ENDPOINT="${ENDPOINT:-/internal}"
BEARER_ENDPOINT="${BEARER_ENDPOINT:-}"   # optional: bearer endpoint path (e.g. /mcp)
PROBE_PATH="${PROBE_PATH:-probe}"
SESSION_TOKEN="${SESSION_TOKEN:-}"
SERVICE_TOKEN="${SERVICE_TOKEN:-}"   # optional: long-lived per-agent service token
CONFIG_FILE="${CONFIG_FILE:-}"     # optional: path to config file for reload test
GATEWAY_URL="${GATEWAY_URL:-}"     # optional: gateway base URL for /health and /ready checks
FULL_URL="${SERVER_URL}${ENDPOINT}"
BEARER_URL="${SERVER_URL}${BEARER_ENDPOINT}"

# All tools the test config exposes — the source of truth for count/catalogue checks.
# Note: find_tools_regex and find_tools_bm25 are omitted here because they only register
# when tools.mcp.discovery.enabled=true, which the test config does not set.
# Every tool the test config exposes that is guaranteed to register (no live
# model and no specific hardware required). agent_spawn/agent_status/agent_list
# (subagent capability) and hw_i2c/hw_spi (Linux + I2C/SPI devices) are also
# exposed but only probed when actually present in the catalogue, so this script
# stays portable.
EXPECTED_TOOLS="file_read file_write file_edit file_append file_list file_search file_copy web_fetch web_search msg_send msg_send_file session_messages session_search session_compact session_info session_summary_list session_summary_get session_clear shell_exec skill_find skill_install cron_schedule cogmem_domain_get cogmem_memory_search cogmem_domain_list cogmem_explain cogmem_memory_create cogmem_domain_update cogmem_memory_retire cogmem_memory_confirm cogmem_domain_create cogmem_domain_archive cogmem_domain_migrate cogmem_memory_forget cogmem_consolidate cogmem_status cogmem_export common_list common_get common_put common_delete"
EXPECTED_TOOL_COUNT=38

# Namespace prefixes that must have at least one tool in the catalogue.
# Covers every provider-owned namespace that is in the test config.
EXPECTED_NAMESPACES="file_ web_ session_ msg_ shell_ skill_ cron_ cogmem_ common_"

# Unique scratch file inside the agent workspace so repeated runs do not collide.
# Writes are confined to <workspace>/files by the read-only-workspace default
# (WorkspaceWriteSubdir), so the scratch file must live under files/.
SCRATCH_REL="files/claw_mcp_test_$$.txt"

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

print_section "0. Reachability and service health"

echo "  0.1 MCP server reachable — list tools"
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

if [ -n "$GATEWAY_URL" ]; then
    echo "  0.2 Gateway /health returns 200"
    HEALTH_CODE=$(curl -s -o /dev/null -w "%{http_code}" "$GATEWAY_URL/health" 2>/dev/null)
    if [ "$HEALTH_CODE" = "200" ]; then
        echo "    ${GREEN}PASS${NC}: /health returned 200"
        PASS_COUNT=$((PASS_COUNT + 1))
    else
        echo "    ${RED}FAIL${NC}: /health returned $HEALTH_CODE"
        FAIL_COUNT=$((FAIL_COUNT + 1))
    fi

    echo "  0.3 Gateway /ready responds"
    READY_CODE=$(curl -s -o /dev/null -w "%{http_code}" "$GATEWAY_URL/ready" 2>/dev/null)
    # 200 = ready, 503 = endpoint reachable but not ready (e.g. no model configured).
    # Both are valid responses from a running gateway; a connection failure would
    # produce an empty string or non-numeric code.
    if [ "$READY_CODE" = "200" ] || [ "$READY_CODE" = "503" ]; then
        echo "    ${GREEN}PASS${NC}: /ready responded with $READY_CODE (endpoint reachable)"
        PASS_COUNT=$((PASS_COUNT + 1))
    else
        echo "    ${RED}FAIL${NC}: /ready did not respond (got '$READY_CODE')"
        FAIL_COUNT=$((FAIL_COUNT + 1))
    fi
fi

echo "  0.4 Minimum tool count (expected: $EXPECTED_TOOL_COUNT)"
# probe lists tools as "      NN: tool_name" — count those lines
TOOL_COUNT=$(echo "$LIST_OUT" | grep -cE "^[0-9]+:" || true)
if [ "$TOOL_COUNT" -ge "$EXPECTED_TOOL_COUNT" ]; then
    echo "    ${GREEN}PASS${NC}: found $TOOL_COUNT tools (minimum: $EXPECTED_TOOL_COUNT)"
    PASS_COUNT=$((PASS_COUNT + 1))
else
    echo "    ${RED}FAIL${NC}: found $TOOL_COUNT tools, expected at least $EXPECTED_TOOL_COUNT"
    FAIL_COUNT=$((FAIL_COUNT + 1))
fi

echo "  0.5 At least one tool present per namespace"
NS_OK=true
for ns in $EXPECTED_NAMESPACES; do
    if echo "$LIST_OUT" | grep -qF "$ns"; then
        echo "    OK: namespace '${ns%_}'"
    else
        echo "    ${RED}FAIL${NC}: no tool found for namespace '${ns%_}' (expected a tool starting with '$ns')"
        FAIL_COUNT=$((FAIL_COUNT + 1))
        NS_OK=false
    fi
done
if $NS_OK; then
    echo "    ${GREEN}PASS${NC}: all expected namespaces represented"
    PASS_COUNT=$((PASS_COUNT + 1))
fi

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

check_tool "1.1"  "file_read"
check_tool "1.2"  "file_write"
check_tool "1.3"  "file_edit"
check_tool "1.4"  "file_append"
check_tool "1.5"  "file_list"
check_tool "1.5b" "file_search"
check_tool "1.6"  "web_fetch"
check_tool "1.7"  "web_search"
check_tool "1.8"  "msg_send_file"
check_tool "1.8b" "msg_send"
check_tool "1.9"  "session_messages"
check_tool "1.10" "session_search"
check_tool "1.11" "session_compact"
check_tool "1.12" "session_info"
check_tool "1.13" "cogmem_domain_create"
check_tool "1.14" "cogmem_memory_create"
check_tool "1.15" "cogmem_status"
check_tool "1.16" "cogmem_memory_confirm"
check_tool "1.17" "cogmem_domain_migrate"
# find_tools_regex and find_tools_bm25 are only registered when
# tools.mcp.discovery.enabled=true — not set in the standard test config.

#-------------------------------------------------------------------------------
# Section 2: Unauthenticated rejection
#-------------------------------------------------------------------------------

print_section "2. Unauthenticated rejection"

run_test_err_contains "2.1 file_list without session_token returns session_token error" \
    "file_list" '{"path":"."}' "session_token"

run_test_err_contains "2.2 file_read without session_token returns session_token error" \
    "file_read" '{"path":"test.txt"}' "session_token"

run_test_err_contains "2.3 session_info without session_token returns session_token error" \
    "session_info" '{}' "session_token"

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

    run_test_ok_auth "3.1 file_list working area (files/)" \
        "file_list" '{"path":"files"}'

    run_test_ok_auth "3.2 file_write creates a scratch file" \
        "file_write" "{\"path\":\"$SCRATCH_REL\",\"content\":\"$PAYLOAD\"}"

    run_test_ok_auth "3.3 file_read returns the written payload" \
        "file_read" "{\"path\":\"$SCRATCH_REL\"}" "$PAYLOAD"

    APPENDED="appended-line-$$"

    run_test_ok_auth "3.4 file_append adds a new line" \
        "file_append" "{\"path\":\"$SCRATCH_REL\",\"content\":\"\n$APPENDED\"}"

    run_test_ok_auth "3.5 file_read shows appended content" \
        "file_read" "{\"path\":\"$SCRATCH_REL\"}" "$APPENDED"

    run_test_ok_auth "3.5b file_read line mode returns numbered lines" \
        "file_read" "{\"path\":\"$SCRATCH_REL\",\"start_line\":1,\"line_count\":1}" "1: "

    run_test_ok_auth "3.5c file_search finds the appended content" \
        "file_search" "{\"query\":\"$APPENDED\",\"path\":\"files\"}" "$APPENDED"

    run_test_ok_auth "3.6 file_edit replaces a substring" \
        "file_edit" "{\"path\":\"$SCRATCH_REL\",\"old_text\":\"$PAYLOAD\",\"new_text\":\"replaced-$$\"}"

    run_test_ok_auth "3.7 file_read confirms replacement" \
        "file_read" "{\"path\":\"$SCRATCH_REL\"}" "replaced-$$"

    run_test_ok_auth "3.8 file_copy duplicates a file" \
        "file_copy" "{\"source_path\":\"$SCRATCH_REL\",\"destination_path\":\"${SCRATCH_REL}.copy\"}"

    run_test_err_auth "3.9 file_read on missing path returns an error" \
        "file_read" '{"path":"files/definitely_not_a_real_file_'$$'_xyz.txt"}'

    run_test_err_auth "3.10 unknown tool is rejected" \
        "definitely_not_a_real_tool_$$" '{}'

    #---------------------------------------------------------------------------
    # Section 4: Session tool smoke tests (Tier 2)
    #---------------------------------------------------------------------------

    print_section "4. Session tool smoke tests (authenticated)"

    run_test_not_auth_err "4.1 session_info — token accepted" \
        "session_info" '{}'

    run_test_not_auth_err "4.2 session_compact — token accepted" \
        "session_compact" '{}'

    run_test_not_auth_err "4.3 session_messages — token accepted" \
        "session_messages" '{"seq_start":1,"seq_end":10}'

    run_test_not_auth_err "4.4 session_search — token accepted" \
        "session_search" '{"query":"test"}'

    run_test_not_auth_err "4.5 session_summary_list — token accepted" \
        "session_summary_list" '{}'

    run_test_not_auth_err "4.6 session_summary_get — token accepted" \
        "session_summary_get" '{"id":"none"}'

    run_test_not_auth_err "4.7 session_clear — token accepted" \
        "session_clear" '{}'

    #---------------------------------------------------------------------------
    # Section 4b: Every remaining provider tool. These reach out to the network
    # (web, skill), need a live model (agent), or touch hardware (hw), so we use
    # graceful-error probes — the call must be accepted and the tool must respond
    # (success OR a clean tool-level error), not fail at the transport/auth layer.
    #---------------------------------------------------------------------------

    print_section "4b. Remaining provider tools (graceful probes)"

    # shell_exec is restricted to internal channels, so over MCP it returns a
    # clean "restricted" error rather than running — a graceful probe.
    run_test_not_auth_err "4b.1 shell_exec — token accepted" \
        "shell_exec" '{"command":"echo mcp-shell-ok"}'

    run_test_not_auth_err "4b.2 web_search — token accepted" \
        "web_search" '{"query":"hello"}'

    run_test_not_auth_err "4b.3 web_fetch — token accepted" \
        "web_fetch" '{"url":"https://example.com"}'

    run_test_not_auth_err "4b.4 msg_send_file — token accepted" \
        "msg_send_file" '{"path":"no-such-file"}'

    run_test_not_auth_err "4b.4b msg_send — token accepted (no longer hard-excluded)" \
        "msg_send" '{"content":"probe"}'

    run_test_not_auth_err "4b.5 skill_find — token accepted" \
        "skill_find" '{"query":"github"}'

    run_test_not_auth_err "4b.6 skill_install — token accepted" \
        "skill_install" '{"slug":"no-such-skill","registry":"clawhub"}'

    run_test_not_auth_err "4b.7 cron_schedule — token accepted" \
        "cron_schedule" '{}'

    # Tools that only register on certain hosts (agent tools need the subagent
    # capability; hw needs Linux + I2C/SPI devices). Probe them only when the
    # catalogue lists them. Empty args are fine — these probes assert the session
    # token is accepted, not that the call succeeds (agent_status needs a uuid).
    for opt_tool in agent_spawn agent_status agent_list hw_i2c hw_spi; do
        if echo "$LIST_OUT" | grep -qw "$opt_tool"; then
            run_test_not_auth_err "4b.* $opt_tool — token accepted" "$opt_tool" '{}'
        else
            echo "  4b.* $opt_tool not registered on this host (skipped)"
        fi
    done

    #---------------------------------------------------------------------------
    # Section 4c: Cognitive-memory tools (cogmem_*). Session-scoped, off by
    # default (DefaultAllow=false) — the test agent's allowlist enables them via
    # "cogmem_*". The store is a per-session SQLite DB created on demand, so the
    # deterministic, hermetic tools (create_domain, remember, list_domains,
    # search, forget, status) are asserted as full SUCCESS. Tools that need a
    # specific domain/hook id (get_domain, explain, update_domain, retire_hook,
    # archive_domain) are probed with representative args as graceful cases — the
    # harness has no facility to thread the generated id between probe calls, so
    # we assert the call is accepted (token honoured, no transport/auth failure)
    # rather than a specific id-dependent outcome. consolidate is non-blocking and
    # may touch an LLM in the background, so it is a graceful probe too.
    #---------------------------------------------------------------------------

    print_section "4c. Cognitive-memory tools (cogmem)"

    COGMEM_DOMAIN="mcp-cogmem-test-$$"
    COGMEM_HOOK_TEXT="cogmem probe note $$"

    # --- Hermetic, deterministic tools: assert full SUCCESS. ---

    # create_domain returns a freshly assigned domain id and reports success.
    # Includes a tool-trigger list (comma-delimited substrings) to exercise that param.
    run_test_ok_auth "4c.1 cogmem_domain_create creates a (non-sticky) topic domain" \
        "cogmem_domain_create" "{\"name\":\"$COGMEM_DOMAIN\",\"triggers\":\"google_gmail,system\"}" "Created domain"

    # remember with a domain_hint auto-creates/uses a topic domain and records
    # a durable hook — no pre-existing id required, so it succeeds deterministically.
    run_test_ok_auth "4c.2 cogmem_memory_create records a hook (domain_hint)" \
        "cogmem_memory_create" "{\"domain_hint\":\"$COGMEM_DOMAIN\",\"type\":\"fact\",\"text\":\"$COGMEM_HOOK_TEXT\"}"

    # list_domains must now report at least the domain(s) created above.
    run_test_ok_auth "4c.3 cogmem_domain_list lists domains" \
        "cogmem_domain_list" '{}' "domain(s)"

    # search over hook text — success whether or not a match is found.
    run_test_ok_auth "4c.4 cogmem_memory_search returns a result set" \
        "cogmem_memory_search" "{\"query\":\"$COGMEM_HOOK_TEXT\"}"

    # status reports store health deterministically.
    run_test_ok_auth "4c.5 cogmem_status reports memory health" \
        "cogmem_status" '{}' "Cognitive memory database"

    # forget retires matching hooks; succeeds whether 0 or N hooks match.
    run_test_ok_auth "4c.6 cogmem_memory_forget retires matching hooks" \
        "cogmem_memory_forget" "{\"query\":\"$COGMEM_HOOK_TEXT\"}"

    # --- id-dependent tools: graceful probes (token accepted). ---

    run_test_not_auth_err "4c.7 cogmem_domain_get — token accepted" \
        "cogmem_domain_get" '{"id":"dMCPtest"}'

    run_test_not_auth_err "4c.8 cogmem_explain — token accepted" \
        "cogmem_explain" '{"id":"dMCPtest"}'

    run_test_not_auth_err "4c.9 cogmem_domain_update — token accepted" \
        "cogmem_domain_update" '{"id":"dMCPtest","set_summary":"probe","set_sticky":true}'

    run_test_not_auth_err "4c.10 cogmem_memory_retire — token accepted" \
        "cogmem_memory_retire" '{"id":"hMCPtest","reason":"probe"}'

    run_test_not_auth_err "4c.11 cogmem_domain_archive — token accepted" \
        "cogmem_domain_archive" '{"id":"dMCPtest"}'

    run_test_not_auth_err "4c.11b cogmem_domain_migrate — token accepted" \
        "cogmem_domain_migrate" '{"from":"dMCPfrom","to":"dMCPto"}'

    # --- consolidate: non-blocking, may touch an LLM — graceful probe. ---

    run_test_not_auth_err "4c.12 cogmem_consolidate — token accepted (queued/accepted)" \
        "cogmem_consolidate" '{"scope":"probe"}'

    # export: dumps active memory to files/MEMORY_EXPORT.md — hermetic, succeeds with auth.
    run_test_ok_auth "4c.13 cogmem_export writes a Markdown dump" \
        "cogmem_export" '{}' "MEMORY_EXPORT.md"

    # Section 4d: Shared common-directory tools (common_*). Hermetic and
    # deterministic. common_put copies the scratch file (under files/, created in
    # Section 3) into the shared dir; list/get/delete round-trip it.
    print_section "4d. Common-directory tools (common)"

    COMMON_NAME="mcp_common_$$.txt"

    run_test_ok_auth "4d.1 common_put copies a workspace file to the shared dir" \
        "common_put" "{\"path\":\"$SCRATCH_REL\",\"as\":\"$COMMON_NAME\"}"

    run_test_ok_auth "4d.2 common_list shows the shared file" \
        "common_list" '{}' "$COMMON_NAME"

    run_test_ok_auth "4d.3 common_get copies it into the agent workspace" \
        "common_get" "{\"name\":\"$COMMON_NAME\"}"

    run_test_ok_auth "4d.4 common_delete removes the shared file" \
        "common_delete" "{\"name\":\"$COMMON_NAME\"}"

fi  # end SESSION_TOKEN block

################################################################################
# TIER 3 — Bearer endpoint (/mcp): standard Authorization: Bearer auth with
# clean tool schemas (no session_token parameter). Runs only when both
# BEARER_ENDPOINT and SESSION_TOKEN are set. The same SST token authenticates
# here, just transported in the header.
################################################################################

if [ -n "$BEARER_ENDPOINT" ] && [ -n "$SESSION_TOKEN" ]; then
    print_section "6. Bearer endpoint ($BEARER_ENDPOINT)"

    # 6.1 — A valid bearer lists tools (clean schemas, header auth).
    echo "  6.1 tools/list over bearer endpoint with valid token"
    bl=$("$PROBE_PATH" -url "$BEARER_URL" -transport http \
        -headers "Authorization:Bearer ${SESSION_TOKEN}" -list 2>&1)
    if echo "$bl" | grep -q "file_read"; then
        echo "    ${GREEN}PASS${NC}: bearer tools/list returned the catalogue"
        TIER2_PASS=$((TIER2_PASS + 1)); PASS_COUNT=$((PASS_COUNT + 1))
    else
        echo "    ${RED}FAIL${NC}: bearer tools/list did not return tools"
        echo "    Output: $bl"
        TIER2_FAIL=$((TIER2_FAIL + 1)); FAIL_COUNT=$((FAIL_COUNT + 1))
    fi

    # 6.2 — A hermetic tool succeeds with NO session_token param (header auth).
    echo "  6.2 session_info via bearer header, no session_token param"
    bc=$("$PROBE_PATH" -url "$BEARER_URL" -transport http \
        -headers "Authorization:Bearer ${SESSION_TOKEN}" \
        -call "session_info" -params '{}' 2>&1)
    if echo "$bc" | grep -q "Tool call succeeded"; then
        echo "    ${GREEN}PASS${NC}: tool call succeeded over bearer transport"
        TIER2_PASS=$((TIER2_PASS + 1)); PASS_COUNT=$((PASS_COUNT + 1))
    else
        echo "    ${RED}FAIL${NC}: bearer tool call failed unexpectedly"
        echo "    Output: $bc"
        TIER2_FAIL=$((TIER2_FAIL + 1)); FAIL_COUNT=$((FAIL_COUNT + 1))
    fi

    # 6.3 — No bearer is rejected at the HTTP layer (401), so probe cannot init.
    echo "  6.3 missing bearer is rejected (401)"
    nb=$("$PROBE_PATH" -url "$BEARER_URL" -transport http \
        -call "session_info" -params '{}' 2>&1)
    if echo "$nb" | grep -qiE "401|unauthorized|bearer|failed|error"; then
        echo "    ${GREEN}PASS${NC}: request without bearer was rejected"
        TIER2_PASS=$((TIER2_PASS + 1)); PASS_COUNT=$((PASS_COUNT + 1))
    else
        echo "    ${RED}FAIL${NC}: missing bearer was not rejected"
        echo "    Output: $nb"
        TIER2_FAIL=$((TIER2_FAIL + 1)); FAIL_COUNT=$((FAIL_COUNT + 1))
    fi

    # 6.4 — An invalid bearer is rejected (401).
    echo "  6.4 invalid bearer is rejected (401)"
    ib=$("$PROBE_PATH" -url "$BEARER_URL" -transport http \
        -headers "Authorization:Bearer SSTdeadbeef" \
        -call "session_info" -params '{}' 2>&1)
    if echo "$ib" | grep -qiE "401|unauthorized|bearer|failed|error"; then
        echo "    ${GREEN}PASS${NC}: request with invalid bearer was rejected"
        TIER2_PASS=$((TIER2_PASS + 1)); PASS_COUNT=$((PASS_COUNT + 1))
    else
        echo "    ${RED}FAIL${NC}: invalid bearer was not rejected"
        echo "    Output: $ib"
        TIER2_FAIL=$((TIER2_FAIL + 1)); FAIL_COUNT=$((FAIL_COUNT + 1))
    fi
fi  # end bearer tier

################################################################################
# TIER 4 — Service token (long-lived, headless). Exercises loadServiceTokens:
# a token pre-seeded into the state file must work as a bearer on /mcp and as a
# session_token parameter on /internal, resolving to the headless service
# session. Runs only when SERVICE_TOKEN is set.
################################################################################

if [ -n "$SERVICE_TOKEN" ]; then
    print_section "7. Service token (long-lived, headless)"

    if [ -n "$BEARER_ENDPOINT" ]; then
        echo "  7.1 service token works as a bearer on $BEARER_ENDPOINT"
        st_b=$("$PROBE_PATH" -url "$BEARER_URL" -transport http \
            -headers "Authorization:Bearer ${SERVICE_TOKEN}" \
            -call "session_info" -params '{}' 2>&1)
        if echo "$st_b" | grep -q "Tool call succeeded"; then
            echo "    ${GREEN}PASS${NC}: service token accepted on bearer endpoint"
            TIER2_PASS=$((TIER2_PASS + 1)); PASS_COUNT=$((PASS_COUNT + 1))
        else
            echo "    ${RED}FAIL${NC}: service token rejected on bearer endpoint"
            echo "    Output: $st_b"
            TIER2_FAIL=$((TIER2_FAIL + 1)); FAIL_COUNT=$((FAIL_COUNT + 1))
        fi
    fi

    echo "  7.2 service token works as session_token on $ENDPOINT"
    st_i=$("$PROBE_PATH" -url "$FULL_URL" -transport http \
        -call "session_info" -params "$(printf '{"session_token":"%s"}' "$SERVICE_TOKEN")" 2>&1)
    if echo "$st_i" | grep -q "Tool call succeeded"; then
        echo "    ${GREEN}PASS${NC}: service token accepted as session_token parameter"
        TIER2_PASS=$((TIER2_PASS + 1)); PASS_COUNT=$((PASS_COUNT + 1))
    else
        echo "    ${RED}FAIL${NC}: service token rejected as session_token parameter"
        echo "    Output: $st_i"
        TIER2_FAIL=$((TIER2_FAIL + 1)); FAIL_COUNT=$((FAIL_COUNT + 1))
    fi
fi  # end service-token tier

################################################################################
# SECTION 5 — Config reload: MCP server recovers after config file change
# Requires CONFIG_FILE env var (set automatically by test.sh).
################################################################################

if [ -n "$CONFIG_FILE" ] && [ -f "$CONFIG_FILE" ]; then
    print_section "5. Config reload (MCP server restart)"

    # 5.0 — Establish baseline before triggering reload.
    echo "  5.0 Baseline: tool count and namespace coverage before reload"
    BASELINE_COUNT=$(echo "$LIST_OUT" | grep -cE "^[0-9]+:" || true)
    BASELINE_NS_OK=true
    for ns in $EXPECTED_NAMESPACES; do
        if ! echo "$LIST_OUT" | grep -qF "$ns"; then
            echo "    ${RED}FAIL${NC}: namespace '${ns%_}' missing before reload — baseline invalid"
            FAIL_COUNT=$((FAIL_COUNT + 1))
            BASELINE_NS_OK=false
        fi
    done
    if $BASELINE_NS_OK; then
        echo "    ${GREEN}PASS${NC}: baseline established — $BASELINE_COUNT tools, all namespaces present"
        PASS_COUNT=$((PASS_COUNT + 1))
    fi

    # 5.1 — Touch config to trigger the watcher.
    echo "  5.1 Touch config file to trigger reload"
    touch "$CONFIG_FILE"
    echo "    ${GREEN}PASS${NC}: config file touched (mtime updated)"
    PASS_COUNT=$((PASS_COUNT + 1))

    # 5.2 — Poll until MCP server responds again (config watcher polls every 5 s).
    echo "  5.2 MCP server recovers after reload"
    RELOAD_DEADLINE=$(($(date +%s) + 30))
    RELOAD_OK=false
    # First wait briefly for the server to go away (reload shuts it down).
    sleep 2
    while [ "$(date +%s)" -lt "$RELOAD_DEADLINE" ]; do
        if "$PROBE_PATH" -url "$FULL_URL" -transport http -list >/dev/null 2>&1; then
            RELOAD_OK=true
            break
        fi
        sleep 1
    done

    if $RELOAD_OK; then
        echo "    ${GREEN}PASS${NC}: MCP server reachable after reload"
        PASS_COUNT=$((PASS_COUNT + 1))
    else
        echo "    ${RED}FAIL${NC}: MCP server did not recover within 30s after reload"
        FAIL_COUNT=$((FAIL_COUNT + 1))
    fi

    if $RELOAD_OK; then
        LIST_RELOAD=$("$PROBE_PATH" -url "$FULL_URL" -transport http -list 2>&1)

        # 5.3 — Full tool catalogue check — every expected tool must be present.
        echo "  5.3 Full tool catalogue intact after reload"
        RELOAD_TOOLS_OK=true
        for tool in $EXPECTED_TOOLS; do
            if ! echo "$LIST_RELOAD" | grep -qF "$tool"; then
                echo "    ${RED}FAIL${NC}: '$tool' missing after reload"
                FAIL_COUNT=$((FAIL_COUNT + 1))
                RELOAD_TOOLS_OK=false
            fi
        done
        if $RELOAD_TOOLS_OK; then
            echo "    ${GREEN}PASS${NC}: all $EXPECTED_TOOL_COUNT expected tools present after reload"
            PASS_COUNT=$((PASS_COUNT + 1))
        fi

        # 5.4 — Tool count matches baseline (no silent additions or drops).
        echo "  5.4 Tool count matches pre-reload baseline"
        RELOAD_COUNT=$(echo "$LIST_RELOAD" | grep -cE "^[0-9]+:" || true)
        if [ "$RELOAD_COUNT" -eq "$BASELINE_COUNT" ]; then
            echo "    ${GREEN}PASS${NC}: tool count unchanged ($RELOAD_COUNT tools)"
            PASS_COUNT=$((PASS_COUNT + 1))
        else
            echo "    ${RED}FAIL${NC}: tool count changed — was $BASELINE_COUNT, now $RELOAD_COUNT"
            FAIL_COUNT=$((FAIL_COUNT + 1))
        fi

        # 5.5 — All namespaces still present after reload.
        echo "  5.5 All namespaces present after reload"
        RELOAD_NS_OK=true
        for ns in $EXPECTED_NAMESPACES; do
            if ! echo "$LIST_RELOAD" | grep -qF "$ns"; then
                echo "    ${RED}FAIL${NC}: namespace '${ns%_}' missing after reload"
                FAIL_COUNT=$((FAIL_COUNT + 1))
                RELOAD_NS_OK=false
            fi
        done
        if $RELOAD_NS_OK; then
            echo "    ${GREEN}PASS${NC}: all namespaces present after reload"
            PASS_COUNT=$((PASS_COUNT + 1))
        fi
    fi
fi

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
