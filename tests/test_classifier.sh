#!/usr/bin/env bash
# Classifiers end to end on a running gateway (run by test.sh after the probe
# suite; hermetic: no network, no real model, no operator alerts).
#
# Starts its own gateway in a temporary CLAW_HOME with one agent, Alice, whose
# classifiers switch is on and whose classifier model is a local stub
# (tests/testdata/classifierllm: an OpenAI-compatible server answering a fixed
# category per marker in the item, and a minimal Telegram Bot API that Alice's
# default chat points at). Alice is granted shell_exec (the source lists
# files/index.json; the fetch reads one item file) and file_move (the
# action moves the file into its category's folder). The agents area has
# non-default modes so the modes of what the classifier store creates can be
# checked exactly.
#
# Checks, from outside the process:
#   - classifier_create over MCP saves a live and a dry-run classifier;
#   - the live run moves each file to its category's folder, leaves the
#     unclassified and the action-less item in place, and state.db
#     (processed, runs) and the activity log record every item;
#   - later ticks reprocess nothing (no model call, no new activity row);
#   - the dry-run classifier records its decision and the action's
#     substituted arguments, and moves nothing;
#   - the run with an unclassified item posts one summary to Alice's chat,
#     naming the agent and the classifier;
#   - a classifier with its rules in Markdown under files/ (instructions_file,
#     instructions_dir, no inline instructions) decides by a rule file, skips
#     a hidden one, shows the loaded text in classifier_test, picks up an
#     edit on the next run; a missing rules folder ends its runs in error,
#     posts the failure to Alice's chat once over two failing runs and
#     tells Alice herself (a system turn in her conversation), and a folder
#     outside files/ is refused;
#   - a hand-edited invalid definition is never run, is logged, and is
#     reported by the Check Up assessment API;
#   - the classifier folder, the definition files and state.db have exactly
#     the agents area's modes.
#
# Usage: tests/test_classifier.sh <claw-binary>
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
if ! command -v "$PROBE" >/dev/null 2>&1; then
    echo "FAIL: probe not found ($PROBE)" >&2
    exit 2
fi

PASS=0
FAIL=0
pass() { echo "  PASS: $1"; PASS=$((PASS + 1)); }
fail() { echo "  FAIL: $1"; FAIL=$((FAIL + 1)); }

TMP="$(mktemp -d -t claw-classifier.XXXXXX)"
HOME_DIR="$TMP/home"
WS="$HOME_DIR/agents/alice"
CLS_DIR="$WS/classifiers"
STUB_LOG="$TMP/stub.log"
GW_OUT="$TMP/gateway.out"
CLAW_LOG="$HOME_DIR/logs/claw.log"
STUB_PID=""
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
    [ -n "$STUB_PID" ] && kill "$STUB_PID" 2>/dev/null
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

# sql <query>: one query on Alice's state.db, read-only; rows as lines of
# "|"-joined values.
sql() {
    python3 -I - "$CLS_DIR/state.db" "$1" <<'PY'
import sqlite3, sys
db = sqlite3.connect("file:" + sys.argv[1] + "?mode=ro", uri=True, timeout=5)
for row in db.execute(sys.argv[2]):
    print("|".join("" if v is None else str(v) for v in row))
PY
}

# mcp <tool> <params-json>: one tool call as Alice over the bearer endpoint.
mcp() {
    "$PROBE" -url "http://127.0.0.1:$MCP_PORT/mcp" -transport http \
        -headers "Authorization:Bearer $TOKEN" -call "$1" -params "$2" 2>&1
}

# mode_of <path>: the octal permission bits.
mode_of() { stat -c '%a' "$1" 2>/dev/null; }

# wait_for <seconds> <command...>: until the command succeeds.
wait_for() {
    local limit=$(($1 * 4))
    shift
    for _ in $(seq 1 "$limit"); do
        "$@" >/dev/null 2>&1 && return 0
        sleep 0.25
    done
    return 1
}

model_calls() { grep -c 'chat: stream=' "$STUB_LOG" 2>/dev/null || true; }

echo "Classifiers end to end"

if ! go build -C "$ROOT" -o "$TMP/classifierllm" ./tests/testdata/classifierllm; then
    echo "FAIL: the stub model server did not build"
    exit 1
fi
STUB_PORT="$(free_port)"
GW_PORT="$(free_port)"
MCP_PORT="$(free_port)"
"$TMP/classifierllm" -listen "127.0.0.1:$STUB_PORT" >"$STUB_LOG" 2>&1 &
STUB_PID=$!
if ! wait_for 10 grep -q "listening on" "$STUB_LOG"; then
    echo "FAIL: the stub model server did not start"
    exit 1
fi

# ---- Alice's items: an index the source lists and one file per item. ----
mkdir -p "$WS/files/inbox" "$WS/files/inbox-dry"
item() { # item <id> <folder>: one element of an index
    printf '{"id":"%s","path":"files/%s/%s.json","read":"cat files/%s/%s.json","to":{"invoice":"files/invoice/%s.json","newsletter":"files/newsletter/%s.json"}}' \
        "$1" "$2" "$1" "$2" "$1" "$1" "$1"
}
printf '{"subject":"Your bill","body":"<p>INVOICE 42 is due.</p>"}\n' >"$WS/files/inbox/a.json"
printf '{"subject":"Weekly news","body":"<p>NEWSLETTER for May.</p>"}\n' >"$WS/files/inbox/b.json"
printf '{"subject":"???","body":"GIBBERISH"}\n' >"$WS/files/inbox/c.json"
printf '{"subject":"Hello","body":"Just saying hello."}\n' >"$WS/files/inbox/d.json"
printf '{"subject":"Another bill","body":"INVOICE 43"}\n' >"$WS/files/inbox-dry/e.json"
printf '{"items":[%s,%s,%s,%s]}\n' "$(item a inbox)" "$(item b inbox)" "$(item c inbox)" "$(item d inbox)" >"$WS/files/index.json"
printf '{"items":[%s]}\n' "$(item e inbox-dry)" >"$WS/files/dry-index.json"

TG_TOKEN="123456789:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
cat >"$HOME_DIR/config.json" <<EOF
{
  "providers": [
    {"name": "stub", "protocol": "openai-chat", "base_url": "http://127.0.0.1:$STUB_PORT/v1", "api_key": "test"}
  ],
  "models": [
    {"model_name": "stub", "model": "stub-1", "provider": "stub", "enabled": true}
  ],
  "agents": {
    "defaults": {"model": "stub"},
    "list": [
      {"id": "alice", "name": "Alice", "default": true, "models": ["stub"],
       "tools": ["shell_exec", "file_read_bytes", "file_move"],
       "classifiers": {"enabled": true, "model": "stub"}}
    ]
  },
  "bindings": [
    {"agent_id": "alice", "default": true,
     "match": {"channel": "telegram", "peer": {"kind": "direct", "id": "4242"}}}
  ],
  "channels": {
    "telegram": [
      {"id": "default", "enabled": true, "token": "$TG_TOKEN", "base_url": "http://127.0.0.1:$STUB_PORT"}
    ]
  },
  "classifiers": {"limits": {"min_every_seconds": 1}},
  "task_models": {"classify": [{"model": "stub"}]},
  "security": {"permissions": {"agents": {"dir_mode": "0750", "file_mode": "0640"}}},
  "gateway": {"host": "127.0.0.1", "port": $GW_PORT, "tls": {"mode": "off"}},
  "mcp_host": {"enabled": true, "listen": "127.0.0.1:$MCP_PORT", "endpoint_path": "/mcp", "external_tools": ["*"]}
}
EOF
chmod 600 "$HOME_DIR/config.json"

ADMIN_USER="e2e"
ADMIN_PASSWORD="e2e-$(openssl rand -hex 16)"
if ! CLAW_ADMIN_USER="$ADMIN_USER" CLAW_ADMIN_PASSWORD="$ADMIN_PASSWORD" "$TMP/classifierllm" -admin-credentials "$HOME_DIR" >"$TMP/admin.out" 2>&1; then
    echo "FAIL: the admin account was not created"
    sed 's/^/    /' "$TMP/admin.out"
    exit 1
fi
TOKEN="$(CLAW_HOME="$HOME_DIR" "$CLAW_BIN" token issue alice | awk '/^  [^ ]+$/ {print $1; exit}')"
if [ -z "$TOKEN" ]; then
    echo "FAIL: no service token for alice"
    exit 1
fi

CLAW_HOME="$HOME_DIR" "$CLAW_BIN" gateway >"$GW_OUT" 2>&1 &
CLAW_PID=$!
if ! wait_for 30 curl -sf "http://127.0.0.1:$GW_PORT/ready"; then
    echo "FAIL: the gateway did not become ready"
    tail -n 40 "$GW_OUT" | sed 's/^/    /'
    exit 1
fi

# ---- 1. Create the classifiers over MCP. ----
SOURCE='{"tool":"shell_exec","args":{"command":"cat files/index.json"},"items":"items","id":"id"}'
FETCH='{"tool":"shell_exec","args":{"command":"{{item.read}}"},"fields":{"subject":"subject","body":"body"},"html":["body"]}'
CLASSIFY='{"instructions":"Sort the document.","categories":[{"name":"invoice","description":"A bill."},{"name":"newsletter","description":"News."},{"name":"other","description":"Anything else."}]}'
ACTIONS='{"invoice":{"tool":"file_move","args":{"source_path":"{{item.path}}","destination_path":"{{item.to.invoice}}"}},"newsletter":{"tool":"file_move","args":{"source_path":"{{item.path}}","destination_path":"{{item.to.newsletter}}"}}}'
LIVE="{\"id\":\"sort\",\"enabled\":true,\"trigger\":{\"every_seconds\":2},\"source\":$SOURCE,\"fetch\":$FETCH,\"classify\":$CLASSIFY,\"actions\":$ACTIONS}"
DRY="{\"id\":\"dry\",\"enabled\":true,\"dry_run\":true,\"trigger\":{\"every_seconds\":2},\"source\":${SOURCE/index.json/dry-index.json},\"fetch\":$FETCH,\"classify\":$CLASSIFY,\"actions\":$ACTIONS}"

OUT="$(mcp classifier_create "{\"definition\":$LIVE}")"
if echo "$OUT" | grep -qF "Classifier sort created."; then pass "classifier_create saves the live classifier"; else fail "classifier_create (sort): $OUT"; fi
OUT="$(mcp classifier_create "{\"definition\":$DRY}")"
if echo "$OUT" | grep -qF "Classifier dry created."; then pass "classifier_create saves the dry-run classifier"; else fail "classifier_create (dry): $OUT"; fi

# ---- 2. The first runs. ----
run_done() { [ -n "$(sql "SELECT ended_at FROM runs WHERE classifier='$1' AND ended_at IS NOT NULL")" ]; }
if wait_for 30 run_done sort && wait_for 30 run_done dry; then
    pass "both classifiers ran"
else
    fail "a classifier did not run within 30s"
    grep -i classifier "$CLAW_LOG" | tail -n 20 | sed 's/^/    /'
fi

check_file() { # check_file <path> <present|absent> <what>
    if { [ "$2" = present ] && [ -f "$WS/$1" ]; } || { [ "$2" = absent ] && [ ! -e "$WS/$1" ]; }; then
        pass "$3"
    else
        fail "$3 ($1 should be $2)"
    fi
}
check_file files/invoice/a.json present "the invoice was moved to files/invoice"
check_file files/inbox/a.json absent "the invoice left the inbox"
check_file files/newsletter/b.json present "the newsletter was moved to files/newsletter"
check_file files/inbox/b.json absent "the newsletter left the inbox"
check_file files/inbox/c.json present "the unclassified item stayed in the inbox"
check_file files/inbox/d.json present "the item of a category with no action stayed in the inbox"

PROCESSED="$(sql "SELECT item_id, status, IFNULL(category,'') FROM processed WHERE classifier='sort' ORDER BY item_id")"
WANT=$'a|acted|invoice\nb|acted|newsletter\nc|unclassified|\nd|acted|other'
if [ "$PROCESSED" = "$WANT" ]; then pass "state.db records every item's outcome"; else fail "state.db processed rows: $(echo "$PROCESSED" | tr '\n' ' ')"; fi

ACTIVITY="$(sql "SELECT item_id, outcome, IFNULL(category,''), IFNULL(action,'') FROM activity WHERE classifier='sort' ORDER BY item_id")"
WANT=$'a|acted|invoice|file_move\nb|acted|newsletter|file_move\nc|unclassified||\nd|recorded|other|'
if [ "$ACTIVITY" = "$WANT" ]; then pass "the activity log records every item"; else fail "activity rows: $(echo "$ACTIVITY" | tr '\n' ' ')"; fi

OUT="$(mcp classifier_activity '{"id":"sort"}')"
if echo "$OUT" | grep -q '"unclassified"' && echo "$OUT" | grep -q 'INVOICE 42 is due.' && ! echo "$OUT" | grep -q '<p>'; then
    pass "classifier_activity shows the items, with the HTML field reduced to text"
else
    fail "classifier_activity: $OUT"
fi

# ---- 3. Later ticks reprocess nothing. ----
CALLS="$(model_calls)"
# a, b, d one call each; c a call and the retry; e one call.
if [ "$CALLS" = 6 ]; then pass "one model call per item, plus the retry of the unclassified one"; else fail "model calls after the first runs: $CALLS, want 6"; fi
FIRST_START="$(sql "SELECT started_at FROM runs WHERE classifier='sort'")"
later_run() { [ "$(sql "SELECT started_at FROM runs WHERE classifier='sort' AND ended_at IS NOT NULL")" != "$FIRST_START" ]; }
if wait_for 15 later_run; then
    LAST="$(sql "SELECT status, listed, new_items FROM runs WHERE classifier='sort'")"
    if [ "$LAST" = "ok|4|0" ]; then pass "a later run lists the four items and takes none"; else fail "later run: $LAST, want ok|4|0"; fi
else
    fail "no later run of sort within 15s"
fi
sleep 3 # one more tick of each classifier
if [ "$(model_calls)" = "$CALLS" ]; then pass "later runs make no model call"; else fail "model calls grew from $CALLS to $(model_calls)"; fi
if [ "$(sql "SELECT COUNT(*) FROM activity")" = 5 ]; then pass "later runs add no activity"; else fail "activity rows: $(sql "SELECT COUNT(*) FROM activity"), want 5"; fi

# ---- 4. The dry run acts on nothing. ----
check_file files/inbox-dry/e.json present "the dry run left its item in place"
check_file files/invoice/e.json absent "the dry run moved nothing"
DRYROW="$(sql "SELECT item_id, category FROM dry_run WHERE classifier='dry'")"
if [ "$DRYROW" = "e|invoice" ]; then pass "the dry run recorded its decision"; else fail "dry_run rows: $DRYROW"; fi
if [ "$(sql "SELECT COUNT(*) FROM processed WHERE classifier='dry'")" = 0 ]; then pass "the dry run recorded nothing as processed"; else fail "the dry run has processed rows"; fi
if [ "$(sql "SELECT outcome, dry_run FROM activity WHERE classifier='dry'")" = "recorded|1" ]; then pass "the dry run's activity is marked dry"; else fail "dry activity: $(sql "SELECT outcome, dry_run FROM activity WHERE classifier='dry'")"; fi
DRYARGS="$(sql "SELECT action_args FROM activity WHERE classifier='dry'")"
if [ "$DRYARGS" = '{"destination_path":"files/invoice/e.json","source_path":"files/inbox-dry/e.json"}' ]; then pass "the dry run records the action's substituted arguments"; else fail "dry action_args: $DRYARGS"; fi
OUT="$(mcp classifier_activity '{"id":"dry","search":"invoice/e.json"}')"
if echo "$OUT" | grep -q 'destination_path' && echo "$OUT" | grep -qF 'files/invoice/e.json'; then
    pass "classifier_activity shows and searches the dry run's action arguments"
else
    fail "classifier_activity (dry): $OUT"
fi

# ---- 5. The problem summary reached Alice's chat, once. ----
SUMMARIES="$(grep 'telegram: chat=4242 ' "$STUB_LOG" | grep -c 'Classifier ' || true)"
if grep 'telegram: chat=4242 ' "$STUB_LOG" | grep -qF 'Alice: Classifier sort: 4 new items, 2 acted, 1 unclassified, 0 failed. Use classifier_activity for details.'; then
    pass "the summary of the run with an unclassified item reached Alice's chat"
else
    fail "no summary in Alice's chat"
    grep 'telegram:' "$STUB_LOG" | sed 's/^/    /'
fi
if [ "$SUMMARIES" = 1 ]; then pass "only the run with a problem was summarised"; else fail "$SUMMARIES summaries posted, want 1"; fi

# ---- 5b. Rules kept in Markdown under files/ reach the model. ----
# The stub answers a rule line "When the item says WORD, answer CATEGORY."
# found in the system prompt. Without the rule files, f would be "other".
MD="$WS/files/classifiers/md"
mkdir -p "$MD/rules" "$WS/files/md-inbox"
printf 'Sort the document by its rules.\n' >"$MD/guide.md"
printf 'When the item says ZEBRA, answer newsletter.\n' >"$MD/rules/00-zebra.md"
printf 'When the item says PANDA, answer invoice.\n' >"$MD/rules/10-panda.md"
printf 'When the item says ZEBRA, answer invoice.\n' >"$MD/rules/.hidden.md"
printf '{"subject":"Crossing","body":"A ZEBRA crossing."}\n' >"$WS/files/md-inbox/f.json"
printf '{"items":[%s]}\n' "$(item f md-inbox)" >"$WS/files/md-index.json"
MDCLASSIFY='{"instructions":"","instructions_file":"files/classifiers/md/guide.md","instructions_dir":"files/classifiers/md/rules","categories":[{"name":"invoice"},{"name":"newsletter"},{"name":"other"}]}'
MDDEF="{\"id\":\"md\",\"enabled\":true,\"dry_run\":true,\"trigger\":{\"every_seconds\":2},\"source\":${SOURCE/index.json/md-index.json},\"fetch\":$FETCH,\"classify\":$MDCLASSIFY,\"actions\":$ACTIONS,\"report\":{\"summary\":\"never\"}}"
OUT="$(mcp classifier_create "{\"definition\":$MDDEF}")"
if echo "$OUT" | grep -qF "Classifier md created."; then pass "classifier_create saves a classifier with Markdown rules and no inline instructions"; else fail "classifier_create (md): $OUT"; fi
md_decided() { [ "$(sql "SELECT category FROM dry_run WHERE classifier='md' AND item_id='$1'")" = "$2" ]; }
if wait_for 30 md_decided f newsletter; then
    pass "a rule file decided the category (hidden rule file skipped)"
else
    fail "md decision for f: $(sql "SELECT category FROM dry_run WHERE classifier='md'"), want newsletter"
fi
OUT="$(mcp classifier_test '{"id":"md","item_id":"f"}')"
if echo "$OUT" | grep -qF '## 00-zebra.md' && echo "$OUT" | grep -qF 'Sort the document by its rules.' && echo "$OUT" | grep -qF '## 10-panda.md' && ! echo "$OUT" | grep -qF 'hidden'; then
    pass "classifier_test shows the system prompt with the Markdown loaded"
else
    fail "classifier_test (md): $OUT"
fi
# An edit applies on the next run, without a definition change; f is not reprocessed.
printf 'When the item says ZEBRA, answer invoice.\n' >"$MD/rules/00-zebra.md"
printf '{"subject":"Another","body":"One more ZEBRA."}\n' >"$WS/files/md-inbox/g.json"
printf '{"items":[%s,%s]}\n' "$(item f md-inbox)" "$(item g md-inbox)" >"$WS/files/md-index.json"
if wait_for 30 md_decided g invoice && md_decided f newsletter; then
    pass "an edited rule file applies on the next run and handled items are not reprocessed"
else
    fail "md decisions after the edit: $(sql "SELECT item_id, category FROM dry_run WHERE classifier='md'" | tr '\n' ' ')"
fi
# A missing rules folder is accepted on save, then ends every run in error;
# the failure is reported once (chat summary under on_problems, notice to
# Alice), not again on the next failing run.
MISSDEF="${MDDEF/\"id\":\"md\"/\"id\":\"md-missing\"}"
MISSDEF="${MISSDEF/files\/classifiers\/md\/rules/files/classifiers/none}"
MISSDEF="${MISSDEF/\"summary\":\"never\"/\"summary\":\"on_problems\"}"
OUT="$(mcp classifier_create "{\"definition\":$MISSDEF}")"
if echo "$OUT" | grep -qF "Classifier md-missing created."; then pass "a rules folder that does not exist yet is accepted"; else fail "classifier_create (md-missing): $OUT"; fi
if wait_for 30 run_done md-missing; then
    RUN="$(sql "SELECT status, new_items, error FROM runs WHERE classifier='md-missing'")"
    if [ "$RUN" = "error|0|classifier md-missing: classify.instructions_dir files/classifiers/none can't be read: it does not exist" ]; then
        pass "a missing rules folder ends the run in error, naming the classifier and the path"
    else
        fail "md-missing run: $RUN"
    fi
else
    fail "md-missing did not run within 30s"
fi
if [ "$(sql "SELECT COUNT(*) FROM dry_run WHERE classifier='md-missing'")" = 0 ]; then pass "the run with a missing rules folder classified nothing"; else fail "md-missing classified items"; fi
MISS_START="$(sql "SELECT started_at FROM runs WHERE classifier='md-missing'")"
miss_rerun() { [ "$(sql "SELECT started_at FROM runs WHERE classifier='md-missing' AND ended_at IS NOT NULL")" != "$MISS_START" ]; }
MISS_SUMMARY="Alice: Classifier md-missing (dry run): the run failed: classify.instructions_dir files/classifiers/none can't be read: it does not exist."
miss_summaries() { grep 'telegram: chat=4242 ' "$STUB_LOG" | grep -cF "Classifier md-missing" || true; }
if wait_for 15 miss_rerun && wait_for 15 grep -qF "text=\"$MISS_SUMMARY\"" "$STUB_LOG"; then
    sleep 1 # a second summary, if one were sent, has been delivered
    if [ "$(miss_summaries)" = 1 ]; then
        pass "the failure was posted to Alice's chat once over two failing runs"
    else
        fail "$(miss_summaries) failure summaries in Alice's chat, want 1"
        grep 'telegram:' "$STUB_LOG" | sed 's/^/    /'
    fi
else
    fail "no second md-missing run, or no failure summary in Alice's chat"
    grep 'telegram:' "$STUB_LOG" | sed 's/^/    /'
fi
NOTICE_LOGS="$(grep -c 'classifier=md-missing agent=alice: run failure reported to the agent' "$CLAW_LOG" || true)"
if [ "$NOTICE_LOGS" = 1 ]; then pass "the failure notice to Alice is logged once"; else fail "$NOTICE_LOGS failure notice log lines, want 1"; fi
notice_in_session() { grep -rqF "[System: classifier] Classifier md-missing (dry run): the run failed:" "$WS/sessions"; }
if wait_for 15 notice_in_session; then
    pass "Alice's conversation received the failure notice"
else
    fail "no failure notice in Alice's conversation"
fi
# The stub model answers the notice's turn with "other"; the answer goes to
# Alice's default chat.
if wait_for 15 grep -qF 'telegram: chat=4242 text="other"' "$STUB_LOG"; then
    pass "Alice's answer to the notice reached her chat"
else
    fail "no answer to the notice in Alice's chat"
fi
BADDEF="${MDDEF/\"id\":\"md\"/\"id\":\"md-bad\"}"
BADDEF="${BADDEF/files\/classifiers\/md\/rules/../memory}"
OUT="$(mcp classifier_create "{\"definition\":$BADDEF}")"
if echo "$OUT" | grep -qF 'classify.instructions_dir' && echo "$OUT" | grep -qF 'must be a path inside files/'; then pass "a rules folder outside files/ is refused"; else fail "classifier_create (md-bad): $OUT"; fi

# ---- 6. A hand-edited invalid definition is not run and is reported. ----
printf '{"id":"broken","owner":"alice","enabled":true,"trigger":{"every_seconds":0},"source":%s,"classify":%s}\n' "$SOURCE" "$CLASSIFY" >"$CLS_DIR/broken.json"
if wait_for 15 grep -q "classifier file $CLS_DIR/broken.json (agent alice) is not run" "$CLAW_LOG"; then
    pass "the invalid definition is logged as not run"
else
    fail "no log line for the invalid definition"
fi
sleep 3
if [ -z "$(sql "SELECT classifier FROM runs WHERE classifier='broken'")" ]; then pass "the invalid definition never ran"; else fail "the invalid definition ran"; fi
COOKIES="$TMP/cookies"
LOGIN_CODE="$(curl -s -o /dev/null -w '%{http_code}' -c "$COOKIES" -H 'Content-Type: application/json' \
    -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASSWORD\"}" "http://127.0.0.1:$GW_PORT/api/auth/login")"
ASSESS="$(curl -s -b "$COOKIES" "http://127.0.0.1:$GW_PORT/api/report/assessment")"
if echo "$ASSESS" | grep -qF 'Classifier (Alice: broken)' && echo "$ASSESS" | grep -qF 'Invalid, not run:'; then
    pass "Check Up reports the invalid definition"
else
    fail "Check Up assessment (login $LOGIN_CODE) does not name the invalid definition: $ASSESS"
fi

# ---- 7. What the classifier store created has the agents area's modes. ----
check_mode() { # check_mode <path> <mode>
    local got
    got="$(mode_of "$1")"
    if [ "$got" = "$2" ]; then pass "${1#"$WS"/} is $2"; else fail "${1#"$WS"/} is ${got:-missing}, want $2"; fi
}
check_mode "$CLS_DIR" 750
check_mode "$CLS_DIR/sort.json" 640
check_mode "$CLS_DIR/dry.json" 640
check_mode "$CLS_DIR/state.db" 640
for side in -wal -shm; do
    [ -e "$CLS_DIR/state.db$side" ] && check_mode "$CLS_DIR/state.db$side" 640
done

echo "  Classifiers end to end: $PASS passed, $FAIL failed"
if [ "$FAIL" -ne 0 ]; then
    echo "  --- classifier log lines ---"
    grep -i classifier "$CLAW_LOG" | tail -n 30 | sed 's/^/    /'
    exit 1
fi
exit 0
