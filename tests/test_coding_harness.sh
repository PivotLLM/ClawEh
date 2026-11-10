#!/usr/bin/env bash
# The coding harness end to end on a running gateway (run by test.sh after
# the task models test; hermetic: no network, no model, no operator alerts).
#
# Starts its own gateway in a temporary CLAW_HOME with one agent, Alice,
# whose tools are exactly the ten coding tools (what the Agents page's
# "Allow coding tools" grants) and one allowed folder outside the workspace
# that the account cannot write. Her files/ holds a git repository: a
# .gitignore at the root (vendor/, /build/, *.log), a nested one in internal/
# (*_gen.go), a nested package, and ignored copies of the function the agent
# looks for, so a search that does not honour the rules finds them.
#
# Drives the tools over the MCP host with Alice's service token, the way a
# model works on code:
#   - file_glob finds the Go files (with a path and from the workspace), the
#     ignored ones included and nothing under .git;
#   - file_search_lines with include and context finds the function in the
#     package only, a named ignored folder is still searched, and
#     output=files/count work;
#   - file_read_lines reads the function, file_edit changes it, and
#     file_search_lines output=count confirms the change;
#   - file_write, file_list, file_insert_lines, file_edit_lines and
#     file_delete_lines on a new file leave it as written;
#   - shell_exec runs git in the workspace (the default working dir): diff
#     --stat and status show exactly the edit and the new file;
#   - the allowed folder is read by absolute path (read, glob, search, and
#     from the shell), and file_write and file_edit there are refused as
#     read-only for this app, leaving it unchanged;
#   - a tool outside the ten (file_append) is not offered to Alice, and a
#     shell deny pattern still refuses.
#
# Usage: tests/test_coding_harness.sh <claw-binary>
# Environment: PROBE_PATH (default probe), KEEP=1 keeps the temporary folder.
set -uo pipefail

# A test gateway must never alert the operator: no ALERTER_* channel from ~/.alerter.
# shellcheck source=tests/lib/gateway_env.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/gateway_env.sh"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLAW_BIN="${1:-}"
PROBE="${PROBE_PATH:-probe}"
if [ -z "$CLAW_BIN" ] || [ ! -x "$CLAW_BIN" ]; then
    echo "FAIL: usage: $0 <claw-binary>" >&2
    exit 2
fi
for cmd in "$PROBE" git; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
        echo "FAIL: $cmd not found" >&2
        exit 2
    fi
done

# The fixture's git and the agent's git calls ignore the operator's git
# configuration (signing, pagers, status settings).
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1

PASS=0
FAIL=0
pass() { echo "  PASS: $1"; PASS=$((PASS + 1)); }
fail() { echo "  FAIL: $1"; FAIL=$((FAIL + 1)); }

TMP="$(mktemp -d -t claw-coding.XXXXXX)"
HOME_DIR="$TMP/home"
WS="$HOME_DIR/agents/alice"
REPO="$WS/files/repo"
EXT="$TMP/reference"
GW_OUT="$TMP/gateway.out"
CLAW_PID=""

cleanup() {
    if [ -n "$CLAW_PID" ] && kill -0 "$CLAW_PID" 2>/dev/null; then
        kill -TERM "$CLAW_PID" 2>/dev/null
        for _ in $(seq 1 20); do
            kill -0 "$CLAW_PID" 2>/dev/null || break
            sleep 0.25
        done
        kill -KILL "$CLAW_PID" 2>/dev/null || true
    fi
    chmod -R u+w "$EXT" 2>/dev/null
    if [ "${KEEP:-}" = "1" ]; then
        echo "  Artifacts kept at $TMP"
    else
        rm -rf "$TMP"
    fi
}
trap cleanup EXIT

free_port() {
    python3 -I -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()'
}

wait_for() {
    local limit=$(($1 * 4))
    shift
    for _ in $(seq 1 "$limit"); do
        "$@" >/dev/null 2>&1 && return 0
        sleep 0.25
    done
    return 1
}

# mcp <tool> <params-json>: one tool call as Alice over the bearer endpoint.
mcp() {
    "$PROBE" -url "http://127.0.0.1:$MCP_PORT/mcp" -transport http \
        -headers "Authorization:Bearer $ALICE" -call "$1" -params "$2" 2>&1
}

# json <key> <value> ...: a JSON object of string, number or boolean values
# (paths may hold any character).
json() {
    python3 -I - "$@" <<'PY'
import json, sys
a = sys.argv[1:]
out = {}
for k, v in zip(a[::2], a[1::2]):
    if v in ("true", "false"):
        out[k] = v == "true"
    elif v.lstrip("-").isdigit():
        out[k] = int(v)
    else:
        out[k] = v
print(json.dumps(out))
PY
}

# expect <what> <text> <needle...>: the call succeeded and every needle is in
# text.
expect() {
    local what="$1" text="$2"
    shift 2
    if ! echo "$text" | grep -q "Tool call succeeded"; then
        fail "$what: the call failed: $text"
        return 1
    fi
    for needle in "$@"; do
        if ! echo "$text" | grep -qF -- "$needle"; then
            fail "$what: no \"$needle\" in: $text"
            return 1
        fi
    done
    pass "$what"
}

# refuse <what> <text> <needle>: the call did not succeed and says needle.
refuse() {
    if echo "$2" | grep -q "Tool call succeeded"; then
        fail "$1: not refused: $2"
    elif ! echo "$2" | grep -qF -- "$3"; then
        fail "$1: refusal lacks \"$3\": $2"
    else
        pass "$1"
    fi
}

# absent <what> <text> <needle...>: no needle is in text.
absent() {
    local what="$1" text="$2"
    shift 2
    for needle in "$@"; do
        if echo "$text" | grep -qF -- "$needle"; then
            fail "$what: \"$needle\" in: $text"
            return 1
        fi
    done
    pass "$what"
}

tree_sum() { (cd "$1" && find . -print0 | sort -z | xargs -0 ls -ld --time-style=+%s; find . -type f -print0 | sort -z | xargs -0 sha256sum) 2>&1 | sha256sum; }

echo "Coding harness end to end"

if ! go build -C "$ROOT" -o "$TMP/classifierllm" ./tests/testdata/classifierllm; then
    echo "FAIL: the admin-account helper did not build"
    exit 1
fi

# ---- The repository in Alice's files/. ----
mkdir -p "$REPO/internal/greet" "$REPO/vendor/dep" "$REPO/build" "$WS/files"
printf 'module example.com/demo\n\ngo 1.22\n' >"$REPO/go.mod"
printf 'vendor/\n/build/\n*.log\n' >"$REPO/.gitignore"
printf '*_gen.go\n' >"$REPO/internal/.gitignore"
cat >"$REPO/main.go" <<'EOF'
package main

import "example.com/demo/internal/greet"

func main() {
	println(greet.Hello())
}
EOF
cat >"$REPO/internal/greet/greet.go" <<'EOF'
// Package greet says hello.
package greet

// Hello returns the greeting.
func Hello() string {
	return "hello"
}
EOF
printf 'package greet\n\nfunc Hello() string { return "generated" }\n' >"$REPO/internal/greet/zz_gen.go"
printf 'package dep\n\nfunc Hello() string { return "vendored" }\n' >"$REPO/vendor/dep/dep.go"
printf 'package build\n\nfunc Hello() string { return "built" }\n' >"$REPO/build/out.go"
printf 'func Hello() in a log\n' >"$REPO/run.log"
git -C "$REPO" init -q
git -C "$REPO" add -A
git -C "$REPO" -c user.name=Alice -c user.email=alice@example.com commit -q -m "Initial" || {
    echo "FAIL: the fixture repository could not be committed"
    exit 1
}
printf 'package stray\n' >"$REPO/.git/stray.go"

# ---- The allowed folder: a second tree the account cannot write. ----
mkdir -p "$EXT/lib"
cat >"$EXT/lib/util.go" <<'EOF'
package lib

// Util is shared reference code.
func Util() int { return 42 }
EOF
chmod -R a-w "$EXT"
READ_ONLY=true
if touch "$EXT/lib/.probe" 2>/dev/null; then
    rm -f "$EXT/lib/.probe"
    READ_ONLY=false # root: file permissions do not bind
fi
EXT_SUM="$(tree_sum "$EXT")"

GW_PORT="$(free_port)"
MCP_PORT="$(free_port)"
cat >"$HOME_DIR/config.json" <<EOF
{
  "agents": {
    "list": [
      {"id": "alice", "name": "Alice", "default": true,
       "tools": ["file_list", "file_glob", "file_read_lines", "file_search_lines", "file_edit_lines",
                 "file_insert_lines", "file_delete_lines", "file_edit", "file_write", "shell_exec"],
       "allowed_folders": [{"path": "$EXT", "note": "reference code"}]}
    ]
  },
  "channels": {"webui": {"enabled": true, "token": "coding-harness-e2e"}},
  "tools": {"exec": {"enable_deny_patterns": true}},
  "gateway": {"host": "127.0.0.1", "port": $GW_PORT, "tls": {"mode": "off"}},
  "mcp_host": {"enabled": true, "listen": "127.0.0.1:$MCP_PORT", "endpoint_path": "/mcp", "external_tools": ["*"]}
}
EOF
chmod 600 "$HOME_DIR/config.json"

if ! CLAW_ADMIN_USER="e2e" CLAW_ADMIN_PASSWORD="e2e-$(openssl rand -hex 16)" "$TMP/classifierllm" -admin-credentials "$HOME_DIR" >"$TMP/admin.out" 2>&1; then
    echo "FAIL: the admin account was not created"
    sed 's/^/    /' "$TMP/admin.out"
    exit 1
fi
ALICE="$(CLAW_HOME="$HOME_DIR" "$CLAW_BIN" token issue alice | awk '/^  [^ ]+$/ {print $1; exit}')"
if [ -z "$ALICE" ]; then
    echo "FAIL: no service token for Alice"
    exit 1
fi

CLAW_HOME="$HOME_DIR" "$CLAW_BIN" gateway >"$GW_OUT" 2>&1 &
CLAW_PID=$!
if ! wait_for 30 curl -sf "http://127.0.0.1:$GW_PORT/ready"; then
    echo "FAIL: the gateway did not become ready"
    tail -n 40 "$GW_OUT" | sed 's/^/    /'
    exit 1
fi

GREET="files/repo/internal/greet/greet.go"

# ---- 1. Find the Go files. ----
OUT="$(mcp file_glob '{"pattern":"**/*.go","path":"files/repo"}')"
expect "file_glob: the Go files of the repository, ignored ones included" "$OUT" \
    "files/repo/main.go" "$GREET" "files/repo/internal/greet/zz_gen.go" "files/repo/vendor/dep/dep.go" "files/repo/build/out.go"
absent "file_glob: nothing under .git" "$OUT" ".git/stray.go"
OUT="$(mcp file_glob '{"pattern":"files/repo/internal/**/*.go"}')"
expect "file_glob: no path matches from the workspace" "$OUT" "$GREET" "files/repo/internal/greet/zz_gen.go"
absent "file_glob: the pattern limits the match, allowed folders left out" "$OUT" "files/repo/main.go" "$EXT"

# ---- 2. Find the function. ----
OUT="$(mcp file_search_lines '{"query":"func Hello","path":"files/repo","include":"*.go","context":2}')"
expect "file_search_lines: include and context find the function" "$OUT" \
    "1 match(es)" "$GREET:5: func Hello() string {" "$GREET-4- // Hello returns the greeting." "$GREET-6- 	return \"hello\""
absent "file_search_lines: .gitignore (root, nested, dir-only, anchored, *.log) is honoured" "$OUT" \
    "zz_gen.go" "vendor/" "build/" "run.log"
OUT="$(mcp file_search_lines '{"query":"func Hello","path":"files/repo/vendor"}')"
expect "file_search_lines: a named ignored folder is searched" "$OUT" "files/repo/vendor/dep/dep.go:3:"
OUT="$(mcp file_search_lines '{"query":"hello","output":"files","include":"files/repo/**/*.go"}')"
expect "file_search_lines: output=files from the default folders with a path include" "$OUT" "$GREET" "files/repo/main.go"
absent "file_search_lines: output=files leaves out the ignored files" "$OUT" "zz_gen.go" "dep.go"

# ---- 3. Read, edit, confirm. ----
OUT="$(mcp file_read_lines "$(json path "$GREET" start_line 5 line_count 3)")"
expect "file_read_lines: reads the function" "$OUT" "5: func Hello() string {" "6: 	return \"hello\"" "7: }"
OUT="$(mcp file_edit "$(json path "$GREET" old_text 'return "hello"' new_text 'return "hello, world"')")"
expect "file_edit: changes the function" "$OUT" "File edited: $GREET"
OUT="$(mcp file_search_lines '{"query":"hello, world","path":"files/repo","output":"count"}')"
expect "file_search_lines output=count: confirms the change" "$OUT" "1 file(s) match" "$GREET: 1"

# ---- 4. A new file through the line tools. ----
TESTFILE="files/repo/internal/greet/greet_test.go"
BODY=$'package greet\n\nimport "testing"\n\nfunc TestHello(t *testing.T) {\n\tif Hello() != "hello, world" {\n\t\tt.Fatal(Hello())\n\t}\n}\n'
OUT="$(mcp file_write "$(json path "$TESTFILE" content "$BODY")")"
expect "file_write: writes a new file in the repository" "$OUT" "$TESTFILE"
OUT="$(mcp file_list '{"path":"files/repo/internal/greet"}')"
expect "file_list: lists the package" "$OUT" "greet.go" "greet_test.go" "zz_gen.go"
OUT="$(mcp file_insert_lines "$(json path "$TESTFILE" after_line 0 text '// Tests by Alice.')")"
expect "file_insert_lines: inserts at the top" "$OUT" "$TESTFILE"
OUT="$(mcp file_edit_lines "$(json path "$TESTFILE" start 1 replace '// Tests by Bob.' backup false)")"
expect "file_edit_lines: replaces the line" "$OUT" "$TESTFILE"
if [ "$(head -n 1 "$WS/$TESTFILE")" = "// Tests by Bob." ]; then pass "file_edit_lines: the line is on disk"; else fail "file_edit_lines: line 1 is $(head -n 1 "$WS/$TESTFILE")"; fi
OUT="$(mcp file_delete_lines "$(json path "$TESTFILE" start 1 backup false)")"
expect "file_delete_lines: removes the line" "$OUT" "$TESTFILE"
if [ "$(cat "$WS/$TESTFILE"; echo x)" = "${BODY}x" ]; then pass "line tools: the file is as written"; else fail "line tools: the file holds $(cat "$WS/$TESTFILE")"; fi

# ---- 5. git sees the change. ----
OUT="$(mcp shell_exec '{"command":"git -C files/repo diff --stat"}')"
expect "shell_exec: git diff --stat in the workspace shows the edit" "$OUT" \
    "internal/greet/greet.go | 2 +-" "1 file changed, 1 insertion(+), 1 deletion(-)"
OUT="$(mcp shell_exec '{"command":"git -C files/repo status --porcelain"}')"
expect "shell_exec: git status shows the edit and the new file" "$OUT" " M internal/greet/greet.go" "?? internal/greet/greet_test.go"
absent "shell_exec: git status shows nothing else" "$OUT" "zz_gen.go" "vendor" "build" "run.log" ".orig" ".0001"
OUT="$(mcp shell_exec "$(json command "git status" working_dir "$REPO")")"
expect "shell_exec: an absolute working_dir" "$OUT" "modified:   internal/greet/greet.go"

# ---- 6. The allowed folder outside the workspace. ----
UTIL="$EXT/lib/util.go"
OUT="$(mcp file_read_lines "$(json path "$UTIL")")"
expect "allowed folder: file_read_lines by absolute path" "$OUT" "4: func Util() int { return 42 }"
OUT="$(mcp file_glob "$(json pattern '**/*.go' path "$EXT")")"
expect "allowed folder: file_glob" "$OUT" "$UTIL"
OUT="$(mcp file_search_lines "$(json query 'func Util' path "$EXT")")"
expect "allowed folder: file_search_lines" "$OUT" "$UTIL:4: func Util() int { return 42 }"
OUT="$(mcp file_search_lines '{"query":"func Util"}')"
absent "allowed folder: not in the default search folders" "$OUT" "$UTIL"
OUT="$(mcp shell_exec "$(json command "cat $UTIL")")"
expect "shell_exec: no path refusal outside the workspace" "$OUT" "func Util() int"
if $READ_ONLY; then
    OUT="$(mcp file_write "$(json path "$EXT/lib/new.go" content 'package lib')")"
    refuse "allowed folder: file_write is refused as read-only" "$OUT" "permission denied: $EXT/lib/new.go is read-only for this app"
    OUT="$(mcp file_edit "$(json path "$UTIL" old_text 'return 42' new_text 'return 7')")"
    refuse "allowed folder: file_edit is refused as read-only" "$OUT" "permission denied: $UTIL is read-only for this app"
    if [ "$(tree_sum "$EXT")" = "$EXT_SUM" ]; then pass "allowed folder: unchanged"; else fail "allowed folder: changed"; fi
else
    echo "  SKIP: read-only allowed folder (running as root)"
fi

# ---- 7. Only the granted tools; the deny patterns. ----
OUT="$(mcp file_append "$(json path "$GREET" content x)")"
refuse "a tool outside the coding tools is not offered" "$OUT" "Tool 'file_append' not found"
OUT="$(mcp shell_exec "$(json command "rm -rf $REPO")")"
refuse "shell_exec: a deny pattern still refuses" "$OUT" "dangerous pattern"
if [ -f "$REPO/main.go" ]; then pass "shell_exec: the denied command did not run"; else fail "shell_exec: the denied command ran"; fi

echo "  Coding harness end to end: $PASS passed, $FAIL failed"
if [ "$FAIL" -ne 0 ]; then
    echo "  --- gateway output ---"
    tail -n 30 "$GW_OUT" | sed 's/^/    /'
    exit 1
fi
exit 0
