#!/usr/bin/env bash
################################################################################
# ClawEh Comprehensive Test Suite
# Runs all Go tests with race detector and coverage measurement, followed by
# MCP server integration tests (probe-driven, self-contained).
#
# Usage:
#   ./test.sh             Full suite: unit tests + MCP integration (default)
#   ./test.sh -f          Fast mode: no race detector, no coverage
#   ./test.sh -c          Coverage only (no race detector)
#   ./test.sh -s          Skip MCP integration tests
#   ./test.sh -n          Disable colour output
#   ./test.sh -x          Preserve test artifacts after completion
#   ./test.sh -h          Show help
#
# MCP integration tests require the 'probe' binary on PATH. If probe is not
# found, the integration section is skipped with a warning (not a failure).
# Set PROBE_PATH to override the binary location.
#
# Exit codes:
#   0  All tests passed and coverage gate met
#   1  One or more tests failed or coverage below minimum
################################################################################

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

#===============================================================================
# Configuration
#===============================================================================

COVERAGE_MIN=50          # Minimum overall coverage % required to pass
COVERAGE_FILE="coverage.out"
TIMEOUT="300s"

#===============================================================================
# Argument Parsing
#===============================================================================

FAST_MODE=false
COVERAGE_ONLY=false
NO_COLOR=false
PRESERVE_ARTIFACTS=false
SKIP_INTEGRATION=false

while getopts "fcsinxh" opt; do
    case $opt in
        f) FAST_MODE=true ;;
        c) COVERAGE_ONLY=true ;;
        s) SKIP_INTEGRATION=true ;;
        i) ;;  # kept for backward compat — integration now runs by default
        n) NO_COLOR=true ;;
        x) PRESERVE_ARTIFACTS=true ;;
        h)
            echo "Usage: $0 [-f] [-c] [-s] [-n] [-x] [-h]"
            echo "  -f  Fast mode: no race detector, no coverage (quickest feedback)"
            echo "  -c  Coverage mode: coverage measurement only, no race detector"
            echo "  -s  Skip MCP integration tests"
            echo "  -n  Disable colour output"
            echo "  -x  Preserve test artifacts after completion (for debugging)"
            echo "  -h  Show this help"
            exit 0
            ;;
        *)
            echo "Usage: $0 [-f] [-c] [-s] [-n] [-x] [-h]"
            exit 1
            ;;
    esac
done

#===============================================================================
# Colors
#===============================================================================

# Disable colours if -n flag given, NO_COLOR env var is set, or stdout is not a terminal
if $NO_COLOR || [ "${NO_COLOR+x}" = "x" ] || [ ! -t 1 ]; then
    RED='' GREEN='' YELLOW='' BLUE='' CYAN='' BOLD='' DIM='' NC=''
else
    RED='\033[0;31m'
    GREEN='\033[0;32m'
    YELLOW='\033[1;33m'
    BLUE='\033[0;34m'
    CYAN='\033[0;36m'
    BOLD='\033[1m'
    DIM='\033[2m'
    NC='\033[0m'
fi

#===============================================================================
# Pre-flight Checks
#===============================================================================

echo ""
echo "${BOLD}============================================${NC}"
echo "${BOLD}   ClawEh Test Suite${NC}"
if $FAST_MODE; then
    echo "${BOLD}   Mode: Fast (no race detector)${NC}"
elif $COVERAGE_ONLY; then
    echo "${BOLD}   Mode: Coverage (no race detector)${NC}"
else
    echo "${BOLD}   Mode: Full (race detector + coverage)${NC}"
fi
if $SKIP_INTEGRATION; then
    echo "${BOLD}   Integration: skipped (-s)${NC}"
fi
echo "${BOLD}============================================${NC}"
echo ""

if ! command -v go &>/dev/null; then
    echo "${RED}ERROR: 'go' not found in PATH${NC}"
    exit 1
fi

GO_VERSION=$(go version | awk '{print $3}')
echo "Go version:  ${CYAN}${GO_VERSION}${NC}"
echo "Module:      ${CYAN}$(go list -m 2>/dev/null || echo 'unknown')${NC}"
echo "Directory:   ${CYAN}${SCRIPT_DIR}${NC}"
echo ""

#===============================================================================
# Build Test Command
#===============================================================================

if $FAST_MODE; then
    TEST_CMD="go test -count=1 -timeout=${TIMEOUT}"
    RUN_COVERAGE=false
    RUN_RACE=false
elif $COVERAGE_ONLY; then
    TEST_CMD="go test -coverprofile=${COVERAGE_FILE} -count=1 -timeout=${TIMEOUT}"
    RUN_COVERAGE=true
    RUN_RACE=false
else
    TEST_CMD="go test -race -coverprofile=${COVERAGE_FILE} -count=1 -timeout=${TIMEOUT}"
    RUN_COVERAGE=true
    RUN_RACE=true
fi

#===============================================================================
# Run go generate
#===============================================================================

echo "${BOLD}Generating code...${NC}"
if ! go generate ./... 2>&1; then
    echo "${RED}ERROR: go generate failed${NC}"
    exit 1
fi
echo "${GREEN}Generate complete${NC}"
echo ""

#===============================================================================
# Run Tests
#===============================================================================

echo "${BOLD}Running tests...${NC}"
if $RUN_RACE; then
    echo "${DIM}(race detector enabled — this takes a moment)${NC}"
elif $FAST_MODE; then
    echo "${YELLOW}WARNING: fast mode — race detector disabled. Do not use this as a final verification.${NC}"
fi
echo ""

TMPOUT=$(mktemp)
if $PRESERVE_ARTIFACTS; then
    trap 'rm -f "$TMPOUT"' EXIT
else
    trap 'rm -f "$TMPOUT" "$COVERAGE_FILE"' EXIT
fi

# Run tests, capture output, stream to terminal simultaneously
set +e
$TEST_CMD ./... 2>&1 | tee "$TMPOUT"
TEST_EXIT=${PIPESTATUS[0]}
set -e

#===============================================================================
# Parse Results
#===============================================================================

PASS_COUNT=0
FAIL_COUNT=0
SKIP_COUNT=0
declare -a FAILED_PKGS=()
declare -a PASSED_PKGS=()
declare -a SKIPPED_PKGS=()

while IFS= read -r line; do
    # ok  	github.com/...	0.123s	coverage: 45.6% of statements
    if [[ "$line" =~ ^ok[[:space:]] ]]; then
        pkg=$(echo "$line" | awk '{print $2}')
        PASSED_PKGS+=("$line")
        PASS_COUNT=$((PASS_COUNT + 1))
    # FAIL	github.com/...	0.123s
    elif [[ "$line" =~ ^FAIL[[:space:]] ]] && [[ ! "$line" =~ ^FAIL$ ]]; then
        pkg=$(echo "$line" | awk '{print $2}')
        FAILED_PKGS+=("$pkg")
        FAIL_COUNT=$((FAIL_COUNT + 1))
    # ?   	github.com/...	[no test files]
    elif [[ "$line" =~ ^\? ]]; then
        pkg=$(echo "$line" | awk '{print $2}')
        SKIPPED_PKGS+=("$pkg")
        SKIP_COUNT=$((SKIP_COUNT + 1))
    fi
done < "$TMPOUT"

#===============================================================================
# Package Summary
#===============================================================================

echo ""
echo "${BOLD}============================================${NC}"
echo "${BOLD}   PACKAGE RESULTS${NC}"
echo "${BOLD}============================================${NC}"
echo ""

if [ ${#FAILED_PKGS[@]} -gt 0 ]; then
    echo "${RED}${BOLD}Failed packages:${NC}"
    for pkg in "${FAILED_PKGS[@]}"; do
        echo "  ${RED}✗${NC} ${pkg}"
    done
    echo ""
fi

echo "${GREEN}${BOLD}Passed: ${PASS_COUNT}${NC}  ${RED}${BOLD}Failed: ${FAIL_COUNT}${NC}  ${DIM}No tests: ${SKIP_COUNT}${NC}"

#===============================================================================
# Coverage Summary
#===============================================================================

if $RUN_COVERAGE && [ -f "$COVERAGE_FILE" ]; then
    echo ""
    echo "${BOLD}============================================${NC}"
    echo "${BOLD}   COVERAGE SUMMARY${NC}"
    echo "${BOLD}============================================${NC}"
    echo ""

    # Extract per-package coverage from test output (already captured in TMPOUT).
    # Lines look like: ok  	<pkg>	0.123s	coverage: 45.6% of statements
    # Sort lowest first so gaps are visible at a glance.
    printf "  ${BOLD}%-60s %8s${NC}\n" "Package" "Coverage"
    printf "  ${DIM}%-60s %8s${NC}\n" "------------------------------------------------------------" "--------"

    COVTMP=$(mktemp)
    while IFS= read -r line; do
        pkg=$(echo "$line" | awk '{print $2}')
        num=$(echo "$line" | sed 's/.*coverage: \([0-9.]*\)%.*/\1/')
        echo "$num $pkg"
    done < <(grep '^ok' "$TMPOUT" | grep 'coverage:') | sort -n > "$COVTMP"

    while IFS= read -r entry; do
        num=$(echo "$entry" | awk '{print $1}')
        pkg=$(echo "$entry" | awk '{print $2}')
        shortpkg=$(echo "$pkg" | sed 's|github.com/PivotLLM/ClawEh/||')
        pct="${num}%"
        if (( $(echo "$num < $COVERAGE_MIN" | bc -l) )); then
            color="${RED}"
        elif (( $(echo "$num < 70" | bc -l) )); then
            color="${YELLOW}"
        else
            color="${GREEN}"
        fi
        printf "  %-60s ${color}%8s${NC}\n" "$shortpkg" "$pct"
    done < "$COVTMP"
    rm -f "$COVTMP"

    echo ""

    TOTAL_PCT=$(go tool cover -func="$COVERAGE_FILE" | grep "^total:" | awk '{print $3}')
    TOTAL_NUM=$(echo "$TOTAL_PCT" | tr -d '%')

    if (( $(echo "$TOTAL_NUM < $COVERAGE_MIN" | bc -l) )); then
        TOTAL_COLOR="${RED}"
    elif (( $(echo "$TOTAL_NUM < 70" | bc -l) )); then
        TOTAL_COLOR="${YELLOW}"
    else
        TOTAL_COLOR="${GREEN}"
    fi

    echo "  ${BOLD}Overall coverage: ${TOTAL_COLOR}${BOLD}${TOTAL_PCT}${NC}${BOLD} (minimum: ${COVERAGE_MIN}%)${NC}"
fi

#===============================================================================
# Optional: MCP Server Integration Tests
#
# Fully self-contained: builds claw, starts a fresh gateway in a temporary
# CLAW_HOME with mcp_host enabled, runs the probe-driven test, then tears
# everything down. No assumptions about an already-running claw.
#===============================================================================

INTEGRATION_RAN=false
INTEGRATION_PASSED=true
INTEGRATION_PASS_COUNT=0
INTEGRATION_FAIL_COUNT=0

if ! $SKIP_INTEGRATION; then
    echo ""
    echo "${BOLD}============================================${NC}"
    echo "${BOLD}   MCP SERVER INTEGRATION TESTS${NC}"
    echo "${BOLD}============================================${NC}"
    echo ""

    INTEGRATION_SCRIPT="$SCRIPT_DIR/tests/test_mcpserver.sh"
    if [ ! -x "$INTEGRATION_SCRIPT" ]; then
        echo "${RED}ERROR: $INTEGRATION_SCRIPT not found or not executable${NC}"
        INTEGRATION_PASSED=false
    else
        # probe is a required external prerequisite for integration tests.
        PROBE_BIN="${PROBE_PATH:-probe}"
        if ! command -v "$PROBE_BIN" >/dev/null 2>&1; then
            echo "${RED}ERROR: 'probe' not found on PATH — MCP integration tests cannot run.${NC}"
            echo "${DIM}Set PROBE_PATH or install probe to run the full test suite.${NC}"
            INTEGRATION_PASSED=false
        else
            INTEGRATION_RAN=true

            # ---- Pick free ports so we don't collide with a running claw. ----
            pick_free_port() {
                python3 - <<'PY'
import socket
s = socket.socket()
s.bind(('127.0.0.1', 0))
print(s.getsockname()[1])
s.close()
PY
            }

            MCP_PORT=$(pick_free_port)
            GATEWAY_PORT=$(pick_free_port)

            # ---- Workspace + binary in a per-run tempdir. ----
            INTEG_TMP=$(mktemp -d -t claw-integ.XXXXXX)
            INTEG_HOME="$INTEG_TMP/home"
            INTEG_BIN="$INTEG_TMP/claw"
            INTEG_LOG="$INTEG_TMP/gateway.log"
            mkdir -p "$INTEG_HOME"

            cleanup_integration() {
                if [ -n "${INTEG_PID:-}" ] && kill -0 "$INTEG_PID" 2>/dev/null; then
                    kill -TERM "$INTEG_PID" 2>/dev/null || true
                    # Give the gateway a moment to release its lock + shut down cleanly.
                    for _ in 1 2 3 4 5 6 7 8 9 10; do
                        kill -0 "$INTEG_PID" 2>/dev/null || break
                        sleep 0.5
                    done
                    kill -KILL "$INTEG_PID" 2>/dev/null || true
                fi
                if $PRESERVE_ARTIFACTS; then
                    echo "${DIM}Integration artifacts preserved at: $INTEG_TMP${NC}"
                else
                    rm -rf "$INTEG_TMP"
                fi
                # Preserve the earlier EXIT trap's cleanup of TMPOUT/COVERAGE_FILE.
                if $PRESERVE_ARTIFACTS; then
                    rm -f "$TMPOUT"
                else
                    rm -f "$TMPOUT" "$COVERAGE_FILE"
                fi
            }
            trap cleanup_integration EXIT

            echo "${DIM}Building claw binary...${NC}"
            if ! go build -o "$INTEG_BIN" . 2>&1; then
                echo "${RED}ERROR: failed to build claw${NC}"
                INTEGRATION_PASSED=false
            else
                # Generate a random session token for Tier 2 integration tests.
                # Passed only via environment — never written to a config file.
                TEST_SESSION_TOKEN="SST$(openssl rand -hex 32)"

                # ---- Minimal config: enable MCP host on the chosen ports. ----
                cat > "$INTEG_HOME/config.json" <<EOF
{
  "agents": {
    "list": [
      {
        "id": "main",
        "name": "main",
        "default": true,
        "tools": ["*", "cogmem_*"]
      },
      {
        "id": "alice",
        "name": "alice",
        "tools": ["*", "cogmem_*"]
      }
    ]
  },
  "channels": {
    "webui": {
      "enabled": true,
      "token": "integration-test-token"
    }
  },
  "gateway": {
    "host": "127.0.0.1",
    "port": $GATEWAY_PORT
  },
  "tools": {
    "web": {
      "duckduckgo": {
        "enabled": true,
        "max_results": 5
      }
    },
    "skills": { "registry": { "enabled": true } },
    "subagent": { "enabled": true },
    "cron": { "enabled": true },
    "tool_overrides": {
      "shell_exec": true,
      "skill_find": true,
      "skill_install": true,
      "cron_schedule": true,
      "agent_spawn": true,
      "agent_status": true,
      "agent_list": true,
      "session_clear": true,
      "hw_i2c": true,
      "hw_spi": true,
      "cogmem_domain_get": true,
      "cogmem_memory_search": true,
      "cogmem_domain_list": true,
      "cogmem_explain": true,
      "cogmem_memory_create": true,
      "cogmem_domain_update": true,
      "cogmem_memory_retire": true,
      "cogmem_memory_confirm": true,
      "cogmem_domain_create": true,
      "cogmem_domain_archive": true,
      "cogmem_domain_migrate": true,
      "cogmem_memory_forget": true,
      "cogmem_consolidate": true,
      "cogmem_export": true,
      "cogmem_status": true
    }
  },
  "mcp_host": {
    "enabled": true,
    "listen": "127.0.0.1:$MCP_PORT",
    "endpoint_path": "/mcp",
    "tools": [
      "file_read",
      "file_write",
      "file_edit",
      "file_append",
      "file_list",
      "file_search",
      "file_copy",
      "web_fetch",
      "web_search",
      "msg_send",
      "msg_send_file",
      "session_messages",
      "session_search",
      "session_compact",
      "session_info",
      "session_summary_list",
      "session_summary_get",
      "session_clear",
      "shell_exec",
      "skill_find",
      "skill_install",
      "cron_schedule",
      "agent_spawn",
      "agent_status",
      "agent_list",
      "hw_i2c",
      "hw_spi",
      "cogmem_domain_get",
      "cogmem_memory_search",
      "cogmem_domain_list",
      "cogmem_explain",
      "cogmem_memory_create",
      "cogmem_domain_update",
      "cogmem_memory_retire",
      "cogmem_memory_confirm",
      "cogmem_domain_create",
      "cogmem_domain_archive",
      "cogmem_domain_migrate",
      "cogmem_memory_forget",
      "cogmem_consolidate",
      "cogmem_export",
      "cogmem_status",
      "common_list",
      "common_get",
      "common_put",
      "common_delete"
    ]
  }
}
EOF

                # Pre-seed a long-lived service token for the default agent so the
                # gateway loads it at boot (exercises loadServiceTokens + the
                # headless service session on /mcp + /internal).
                TEST_SERVICE_TOKEN="SST$(openssl rand -hex 32)"
                mkdir -p "$INTEG_HOME/state"
                printf '{"main":"%s"}\n' "$TEST_SERVICE_TOKEN" > "$INTEG_HOME/state/service-tokens.json"

                echo "${DIM}Starting gateway (CLAW_HOME=$INTEG_HOME, MCP=127.0.0.1:$MCP_PORT)...${NC}"
                CLAW_HOME="$INTEG_HOME" CLAW_MCP_TEST_TOKEN="$TEST_SESSION_TOKEN" "$INTEG_BIN" gateway >"$INTEG_LOG" 2>&1 &
                INTEG_PID=$!

                # ---- Wait for the MCP port to accept connections. ----
                READY=false
                for _ in $(seq 1 40); do
                    if ! kill -0 "$INTEG_PID" 2>/dev/null; then
                        break  # gateway died — fall through to failure path
                    fi
                    if (echo > "/dev/tcp/127.0.0.1/$MCP_PORT") 2>/dev/null; then
                        READY=true
                        break
                    fi
                    sleep 0.25
                done

                if ! $READY; then
                    echo "${RED}ERROR: MCP server did not start on 127.0.0.1:$MCP_PORT within 10s${NC}"
                    echo "${DIM}--- gateway log (tail) ---${NC}"
                    tail -n 40 "$INTEG_LOG" | sed 's/^/    /'
                    INTEGRATION_PASSED=false
                else
                    echo "${GREEN}Gateway ready on 127.0.0.1:$MCP_PORT/mcp${NC}"
                    echo ""

                    # ---- Workspace population: initial startup ----
                    echo "${BOLD}--- Workspace population (initial startup) ---${NC}"
                    echo ""
                    WORKSPACE_PASS=true
                    WORKSPACE_TEMPLATES="AGENTS.md SOUL.md USER.md IDENTITY.md MEMORY.md"
                    # The default agent (id=main) uses agents/default; named agents use agents/{id}
                    for agent_ws in "main:$INTEG_HOME/agents/default" "alice:$INTEG_HOME/agents/alice"; do
                        agent="${agent_ws%%:*}"
                        WS="${agent_ws#*:}"
                        for tpl in $WORKSPACE_TEMPLATES; do
                            if [ -f "$WS/$tpl" ]; then
                                echo "  ${GREEN}PASS${NC}: $agent/$tpl populated at startup"
                                INTEGRATION_PASS_COUNT=$((INTEGRATION_PASS_COUNT + 1))
                            else
                                echo "  ${RED}FAIL${NC}: $agent/$tpl missing after startup"
                                INTEGRATION_FAIL_COUNT=$((INTEGRATION_FAIL_COUNT + 1))
                                WORKSPACE_PASS=false
                            fi
                        done
                    done
                    echo ""

                    # Run the probe-driven test against this ephemeral instance.
                    if SERVER_URL="http://127.0.0.1:$MCP_PORT" \
                       ENDPOINT="/internal" \
                       BEARER_ENDPOINT="/mcp" \
                       PROBE_PATH="$PROBE_BIN" \
                       SESSION_TOKEN="$TEST_SESSION_TOKEN" \
                       SERVICE_TOKEN="$TEST_SERVICE_TOKEN" \
                       CONFIG_FILE="$INTEG_HOME/config.json" \
                       GATEWAY_URL="http://127.0.0.1:$GATEWAY_PORT" \
                       bash "$INTEGRATION_SCRIPT"; then
                        echo "${GREEN}MCP server integration tests passed.${NC}"
                    else
                        echo "${RED}MCP server integration tests failed.${NC}"
                        echo "${DIM}--- gateway log (tail) ---${NC}"
                        tail -n 40 "$INTEG_LOG" | sed 's/^/    /'
                        INTEGRATION_PASSED=false
                    fi

                    # ---- Workspace re-population: delete alice's dir and restart ----
                    echo ""
                    echo "${BOLD}--- Workspace population (restart after deletion) ---${NC}"
                    echo ""

                    # Stop the current gateway.
                    kill -TERM "$INTEG_PID" 2>/dev/null
                    for _ in $(seq 1 20); do
                        kill -0 "$INTEG_PID" 2>/dev/null || break
                        sleep 0.25
                    done
                    kill -KILL "$INTEG_PID" 2>/dev/null || true

                    # Delete alice's workspace — simulates adding a fresh agent.
                    rm -rf "$INTEG_HOME/agents/alice"
                    echo "  ${DIM}Deleted alice's workspace directory${NC}"

                    # Restart the gateway.
                    CLAW_HOME="$INTEG_HOME" CLAW_MCP_TEST_TOKEN="$TEST_SESSION_TOKEN" \
                        "$INTEG_BIN" gateway >>"$INTEG_LOG" 2>&1 &
                    INTEG_PID=$!

                    # Wait for MCP port — workspace population happens before the server
                    # starts listening, so once the port is up the files are on disk.
                    RESTART_READY=false
                    for _ in $(seq 1 40); do
                        if ! kill -0 "$INTEG_PID" 2>/dev/null; then
                            break
                        fi
                        if (echo > "/dev/tcp/127.0.0.1/$MCP_PORT") 2>/dev/null; then
                            RESTART_READY=true
                            break
                        fi
                        sleep 0.25
                    done

                    ALICE_WS="$INTEG_HOME/agents/alice"
                    if ! $RESTART_READY; then
                        echo "  ${RED}FAIL${NC}: gateway did not restart within 10s"
                        INTEGRATION_FAIL_COUNT=$((INTEGRATION_FAIL_COUNT + 1))
                        INTEGRATION_PASSED=false
                    else
                        for tpl in $WORKSPACE_TEMPLATES; do
                            if [ -f "$ALICE_WS/$tpl" ]; then
                                echo "  ${GREEN}PASS${NC}: alice/$tpl re-populated after restart"
                                INTEGRATION_PASS_COUNT=$((INTEGRATION_PASS_COUNT + 1))
                            else
                                echo "  ${RED}FAIL${NC}: alice/$tpl missing after restart"
                                INTEGRATION_FAIL_COUNT=$((INTEGRATION_FAIL_COUNT + 1))
                                INTEGRATION_PASSED=false
                            fi
                        done
                    fi
                fi
            fi
        fi
    fi
fi

#===============================================================================
# Final Summary
#===============================================================================

echo ""
echo "${BOLD}============================================${NC}"
echo "${BOLD}   TEST SUMMARY${NC}"
echo "${BOLD}============================================${NC}"
echo ""

TOTAL_PKGS=$((PASS_COUNT + FAIL_COUNT))
echo "Total Tests: ${BOLD}${TOTAL_PKGS}${NC}"
echo "Passed:      ${GREEN}${BOLD}${PASS_COUNT}${NC}"
echo "Failed:      ${RED}${BOLD}${FAIL_COUNT}${NC}"
echo "Skipped:     ${DIM}${SKIP_COUNT}${NC}"

OVERALL_PASS=true

if [ $FAIL_COUNT -gt 0 ]; then
    OVERALL_PASS=false
fi

if $RUN_COVERAGE && [ -f "$COVERAGE_FILE" ]; then
    echo "Coverage:    ${TOTAL_COLOR}${BOLD}${TOTAL_PCT}${NC}"
    if (( $(echo "$TOTAL_NUM < $COVERAGE_MIN" | bc -l) )); then
        echo ""
        echo "${RED}Coverage ${TOTAL_PCT} is below minimum ${COVERAGE_MIN}%${NC}"
        OVERALL_PASS=false
    fi
fi

if $RUN_RACE; then
    echo "Race:        ${GREEN}enabled${NC}"
fi

if $SKIP_INTEGRATION; then
    echo "MCP integ:   ${DIM}skipped (-s)${NC}"
elif $INTEGRATION_RAN; then
    if $INTEGRATION_PASSED; then
        echo "MCP integ:   ${GREEN}passed${NC} (workspace: ${INTEGRATION_PASS_COUNT}/${INTEGRATION_PASS_COUNT} checks)"
    else
        echo "MCP integ:   ${RED}failed${NC} (workspace: ${INTEGRATION_FAIL_COUNT} failure(s))"
        OVERALL_PASS=false
    fi
else
    # probe was not found — counts as a failure per test suite contract
    echo "MCP integ:   ${RED}failed (probe not found)${NC}"
    OVERALL_PASS=false
fi

echo ""

if $OVERALL_PASS; then
    echo "${GREEN}${BOLD}All tests passed!${NC}"
    echo ""
    exit 0
else
    echo "${RED}${BOLD}FAILURES DETECTED${NC}"
    echo ""
    exit 1
fi
