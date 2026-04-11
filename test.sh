#!/bin/bash
################################################################################
# ClawEh Comprehensive Test Suite
# Runs all Go tests with race detector and coverage measurement.
#
# Usage:
#   ./test.sh             Full suite: race detector + coverage (default)
#   ./test.sh -f          Fast mode: no race detector, no coverage
#   ./test.sh -c          Coverage only (no race detector)
#   ./test.sh -n          Disable colour output
#   ./test.sh -x          Preserve test artifacts after completion
#   ./test.sh -h          Show help
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

while getopts "fcnxh" opt; do
    case $opt in
        f) FAST_MODE=true ;;
        c) COVERAGE_ONLY=true ;;
        n) NO_COLOR=true ;;
        x) PRESERVE_ARTIFACTS=true ;;
        h)
            echo "Usage: $0 [-f] [-c] [-n] [-x] [-h]"
            echo "  -f  Fast mode: no race detector, no coverage (quickest feedback)"
            echo "  -c  Coverage mode: coverage measurement only, no race detector"
            echo "  -n  Disable colour output"
            echo "  -x  Preserve test artifacts after completion (for debugging)"
            echo "  -h  Show this help"
            exit 0
            ;;
        *)
            echo "Usage: $0 [-f] [-c] [-n] [-x] [-h]"
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
