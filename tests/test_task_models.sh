#!/usr/bin/env bash
# Task models end to end on a running gateway (run by test.sh after the
# classifier test; hermetic: no network, no real model, no operator alerts).
#
# Starts its own gateway in a temporary CLAW_HOME with a stub vendor
# (tests/testdata/taskmodelsllm: OpenAI-style chat, images, speech and
# transcription, and Gemini's generateContent TTS) and task models for every
# kind, one ordered list per use: two models in Generate text, the first of
# which answers 503 (fallback), a chat image model and an images-API model,
# an OpenAI-style and a Gemini speech model, and a transcription model.
# Alice may use every kind; Bob only Text. The agents area has non-default
# modes so the output's modes can be checked exactly.
#
# Checks, from outside the process, driving agent_spawn over the MCP host
# with each agent's service token:
#   - the lists load with no unknown task_models key;
#   - text (wait): the 503 model is tried, the next one answers; the text is
#     in the result and in a .md file under files/generated/<date>/;
#   - image (wait): chat image output, an images-API generation and an edit
#     of a workspace file, each saved as a PNG;
#   - tts (wait): OpenAI-style speech saved as .mp3, Gemini PCM as a WAV
#     file with the right header;
#   - stt (wait): a workspace audio file transcribed, the transcript inline
#     and saved;
#   - callback: a background text call finishes, its status and results file
#     say so;
#   - Bob's image call is refused, naming Bob and the kind;
#   - every call is an audit row (task_model), the failed model's as error;
#   - every call's result names at least one file under
#     files/generated/<today>/, each with the agents area's exact modes, and
#     no output is written outside the agents' workspaces (the isolated
#     folder a CLI text model runs in, removed afterwards, is checked by
#     agent's TestTaskModelHost_CLIIsolated; no CLI runs here).
#
# Usage: tests/test_task_models.sh <claw-binary>
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

TMP="$(mktemp -d -t claw-taskmodels.XXXXXX)"
HOME_DIR="$TMP/home"
WS="$HOME_DIR/agents/alice"
STUB_LOG="$TMP/stub.log"
GW_OUT="$TMP/gateway.out"
CLAW_LOG="$HOME_DIR/logs/claw.log"
AUDIT_DB="$HOME_DIR/internal/audit.db"
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

# audit_rows <where>: task_model rows of the audit database matching where,
# as "agent|tool|outcome|summary|details" lines.
audit_rows() {
    python3 -I - "$AUDIT_DB" "$1" <<'PY'
import sqlite3, sys
db = sqlite3.connect("file:" + sys.argv[1] + "?mode=ro", uri=True, timeout=5)
q = "SELECT agent, tool, outcome, summary, details FROM events WHERE kind='task_model' AND " + sys.argv[2] + " ORDER BY id"
for row in db.execute(q):
    print("|".join("" if v is None else " ".join(str(v).split()) for v in row))
PY
}

# mcp <token> <tool> <params-json>: one tool call over the bearer endpoint.
mcp() {
    "$PROBE" -url "http://127.0.0.1:$MCP_PORT/mcp" -transport http \
        -headers "Authorization:Bearer $1" -call "$2" -params "$3" 2>&1
}

mode_of() { stat -c '%a' "$1" 2>/dev/null; }

wait_for() {
    local limit=$(($1 * 4))
    shift
    for _ in $(seq 1 "$limit"); do
        "$@" >/dev/null 2>&1 && return 0
        sleep 0.25
    done
    return 1
}

# outputs <result>: the workspace paths of the files a result names under
# today's folder.
outputs() { echo "$1" | grep -oE "files/generated/$TODAY/[A-Za-z0-9._-]+" | sort -u; }

# named_outputs <what> <result>: sets PATHS to outputs, failing the check
# when the result names none (so the per-file checks never pass by looping
# over nothing).
PATHS=""
named_outputs() {
    PATHS="$(outputs "$2")"
    if [ -z "$PATHS" ]; then
        fail "$1: the result names no file under files/generated/$TODAY/"
    fi
}

# expect <what> <text> <needle...>: every needle is in text.
expect() {
    local what="$1" text="$2"
    shift 2
    for needle in "$@"; do
        if ! echo "$text" | grep -qF -- "$needle"; then
            fail "$what: no \"$needle\" in: $text"
            return 1
        fi
    done
    pass "$what"
}

# check_output <path> <what>: the file exists under today's folder with the
# agents area's modes, as does its folder.
check_output() {
    local f="$WS/$1"
    if [ ! -f "$f" ]; then
        fail "$2: $1 was not written"
        return 1
    fi
    case "$1" in
        "files/generated/$TODAY/"*) ;;
        *) fail "$2: $1 is not under files/generated/$TODAY/"; return 1 ;;
    esac
    if [ "$(mode_of "$f")" = 640 ] && [ "$(mode_of "$(dirname "$f")")" = 750 ]; then
        pass "$2: $1 written with modes 640/750"
    else
        fail "$2: $1 has mode $(mode_of "$f"), folder $(mode_of "$(dirname "$f")"), want 640/750"
    fi
}

stub_calls() { grep -c -- "$1" "$STUB_LOG" 2>/dev/null || true; }

echo "Task models end to end"

if ! go build -C "$ROOT" -o "$TMP/taskmodelsllm" ./tests/testdata/taskmodelsllm; then
    echo "FAIL: the stub vendor did not build"
    exit 1
fi
if ! go build -C "$ROOT" -o "$TMP/classifierllm" ./tests/testdata/classifierllm; then
    echo "FAIL: the admin-account helper did not build"
    exit 1
fi
STUB_PORT="$(free_port)"
GW_PORT="$(free_port)"
MCP_PORT="$(free_port)"
"$TMP/taskmodelsllm" -listen "127.0.0.1:$STUB_PORT" >"$STUB_LOG" 2>&1 &
STUB_PID=$!
if ! wait_for 10 grep -q "listening on" "$STUB_LOG"; then
    echo "FAIL: the stub vendor did not start"
    exit 1
fi

mkdir -p "$WS/files" "$HOME_DIR/agents/bob/files"
printf 'OggS stub voice note' >"$WS/files/clip.ogg"
python3 -I -c 'import base64,sys;open(sys.argv[1],"wb").write(base64.b64decode("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="))' "$WS/files/in.png"

STUB="http://127.0.0.1:$STUB_PORT"
cat >"$HOME_DIR/config.json" <<EOF
{
  "providers": [
    {"name": "stub", "protocol": "openai-chat", "base_url": "$STUB/v1", "api_key": "test"},
    {"name": "gem", "protocol": "gemini", "base_url": "$STUB/v1beta", "api_key": "gemini-test"}
  ],
  "models": [
    {"model_name": "chat", "model": "chat-1", "provider": "stub", "enabled": true},
    {"model_name": "txt-down", "model": "down-1", "provider": "stub", "enabled": true},
    {"model_name": "txt", "model": "text-1", "provider": "stub", "enabled": true},
    {"model_name": "imgchat", "model": "image-chat-1", "provider": "stub", "enabled": true},
    {"model_name": "imgapi", "model": "gpt-image-1", "provider": "stub", "enabled": true},
    {"model_name": "tts", "model": "tts-1", "provider": "stub", "enabled": true},
    {"model_name": "gtts", "model": "gemini-tts-1", "provider": "gem", "enabled": true},
    {"model_name": "stt", "model": "whisper-1", "provider": "stub", "enabled": true}
  ],
  "task_models": {
    "result_text_chars": 5000,
    "text": [{"model": "txt-down"}, {"model": "txt"}],
    "image": [{"model": "imgchat"}, {"model": "imgapi", "image_api": "images"}],
    "tts": [
      {"model": "tts", "tts_defaults": {"voice": "alloy", "format": "mp3"}},
      {"model": "gtts", "tts_defaults": {"voice": "Kore"}}
    ],
    "stt": [{"model": "stt"}]
  },
  "agents": {
    "defaults": {"model": "chat"},
    "list": [
      {"id": "alice", "name": "Alice", "default": true, "models": ["chat"],
       "tools": ["agent_spawn", "agent_status", "file_read_bytes"],
       "task_models": {"text": true, "image": true, "tts": true, "stt": true}},
      {"id": "bob", "name": "Bob", "models": ["chat"],
       "tools": ["agent_spawn"], "task_models": {"text": true}}
    ]
  },
  "channels": {"webui": {"enabled": true, "token": "task-models-e2e"}},
  "security": {"permissions": {"agents": {"dir_mode": "0750", "file_mode": "0640"}}},
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
issue_token() { CLAW_HOME="$HOME_DIR" "$CLAW_BIN" token issue "$1" | awk '/^  [^ ]+$/ {print $1; exit}'; }
ALICE="$(issue_token alice)"
BOB="$(issue_token bob)"
if [ -z "$ALICE" ] || [ -z "$BOB" ]; then
    echo "FAIL: no service tokens"
    exit 1
fi

CLAW_HOME="$HOME_DIR" "$CLAW_BIN" gateway >"$GW_OUT" 2>&1 &
CLAW_PID=$!
if ! wait_for 30 curl -sf "http://127.0.0.1:$GW_PORT/ready"; then
    echo "FAIL: the gateway did not become ready"
    tail -n 40 "$GW_OUT" | sed 's/^/    /'
    exit 1
fi
TODAY="$(date +%F)"

# ---- 0. The per-use lists load as written. ----
if grep -q 'unknown config key: task_models' "$GW_OUT"; then
    fail "config: a task_models key was not recognised: $(grep 'unknown config key: task_models' "$GW_OUT" | tr '\n' ';')"
else
    pass "config: the task_models lists load with no unknown key"
fi

# ---- 1. Text, with fallback. ----
OUT="$(mcp "$ALICE" agent_spawn '{"kind":"text","task":"Say something.","mode":"wait","name":"t1"}')"
expect "text: the next model answers after the 503" "$OUT" "kind text): ok." "Model: txt" "Model txt-down failed" "TEXT-REPLY"
named_outputs text "$OUT"
for p in $PATHS; do
    check_output "$p" "text"
    if [ "$(cat "$WS/$p")" = "TEXT-REPLY" ]; then pass "text: the .md file holds the reply"; else fail "text: $p holds $(cat "$WS/$p")"; fi
done
if [ "$(stub_calls 'model=down-1')" = 1 ] && [ "$(stub_calls 'model=text-1 stream=false tools=0')$(stub_calls 'model=text-1 stream=true tools=0')" != "00" ]; then
    pass "text: the 503 model was called once, the next one with no tools"
else
    fail "text: stub calls: $(grep -E 'down-1|text-1' "$STUB_LOG" | tr '\n' ';')"
fi

# ---- 2. Images. ----
OUT="$(mcp "$ALICE" agent_spawn '{"kind":"image","task":"A cat.","mode":"wait","name":"i1"}')"
expect "image: chat image output" "$OUT" "kind image): ok." "Model: imgchat" ".png" "msg_send_file" "IMAGE-WORDS"
PNGS=0
named_outputs "image (chat)" "$OUT"
for p in $PATHS; do
    check_output "$p" "image (chat)"
    case "$p" in *.png) if head -c 8 "$WS/$p" | od -An -tx1 | grep -q '89 50 4e 47'; then PNGS=$((PNGS + 1)); fi ;; esac
done
if [ "$PNGS" = 1 ]; then pass "image (chat): one PNG saved"; else fail "image (chat): $PNGS PNGs saved"; fi
if echo "$OUT" | grep -q 'base64'; then fail "image (chat): the result carries image data"; else pass "image (chat): the result carries only paths"; fi

OUT="$(mcp "$ALICE" agent_spawn '{"kind":"image","model":"imgapi","task":"A dog.","mode":"wait","name":"i2"}')"
expect "image: images API generation" "$OUT" "kind image): ok." "Model: imgapi" ".png"
named_outputs "image (generation)" "$OUT"
for p in $PATHS; do check_output "$p" "image (generation)"; done
if [ "$(stub_calls '/v1/images/generations model=gpt-image-1')" = 1 ]; then pass "image: /v1/images/generations called"; else fail "image: no generation call"; fi

OUT="$(mcp "$ALICE" agent_spawn '{"kind":"image","model":"imgapi","task":"Make it red.","files":["files/in.png"],"mode":"wait","name":"i3"}')"
expect "image: images API edit of a workspace file" "$OUT" "kind image): ok." ".png"
named_outputs "image (edit)" "$OUT"
for p in $PATHS; do check_output "$p" "image (edit)"; done
if [ "$(stub_calls '/v1/images/edits model=gpt-image-1 images=1')" = 1 ]; then pass "image: /v1/images/edits called with the input image"; else fail "image: no edit call: $(grep edits "$STUB_LOG")"; fi

# ---- 3. Speech. ----
OUT="$(mcp "$ALICE" agent_spawn '{"kind":"tts","task":"Hello Bob.","mode":"wait","name":"s1"}')"
expect "tts: OpenAI-style speech" "$OUT" "kind tts): ok." "Model: tts" ".mp3"
named_outputs "tts (mp3)" "$OUT"
for p in $PATHS; do
    check_output "$p" "tts (mp3)"
    if [ "$(head -c 3 "$WS/$p")" = "ID3" ]; then pass "tts: the mp3 is the audio returned"; else fail "tts: $p is not the audio returned"; fi
done
if [ "$(stub_calls '/v1/audio/speech model=tts-1 voice=alloy format=mp3')" = 1 ]; then pass "tts: the model's voice and format defaults were sent"; else fail "tts: speech call: $(grep speech "$STUB_LOG")"; fi

OUT="$(mcp "$ALICE" agent_spawn '{"kind":"tts","model":"gtts","task":"Hello Bob.","mode":"wait","name":"s2"}')"
expect "tts: Gemini speech" "$OUT" "kind tts): ok." "Model: gtts" ".wav"
named_outputs "tts (wav)" "$OUT"
for p in $PATHS; do
    check_output "$p" "tts (wav)"
    HEADER="$(python3 -I -c '
import struct, sys
b = open(sys.argv[1], "rb").read()
print(b[0:4].decode(), b[8:12].decode(), struct.unpack("<H", b[22:24])[0], struct.unpack("<I", b[24:28])[0],
      struct.unpack("<H", b[34:36])[0], struct.unpack("<I", b[40:44])[0], len(b))' "$WS/$p")"
    if [ "$HEADER" = "RIFF WAVE 1 24000 16 8 52" ]; then pass "tts: Gemini PCM wrapped in a WAV header (mono, 24 kHz, 16-bit)"; else fail "tts: WAV header: $HEADER"; fi
done

# ---- 4. Transcription. ----
OUT="$(mcp "$ALICE" agent_spawn '{"kind":"stt","files":["files/clip.ogg"],"mode":"wait","name":"r1"}')"
expect "stt: a workspace audio file transcribed" "$OUT" "kind stt): ok." "Model: stt" "TRANSCRIPT-TEXT"
named_outputs stt "$OUT"
for p in $PATHS; do
    check_output "$p" "stt"
    if [ "$(cat "$WS/$p")" = "TRANSCRIPT-TEXT" ]; then pass "stt: the transcript is saved"; else fail "stt: $p holds $(cat "$WS/$p")"; fi
done

# ---- 5. Callback mode. ----
OUT="$(mcp "$ALICE" agent_spawn '{"kind":"text","model":"txt","task":"In the background.","name":"bg"}')"
UUID="$(echo "$OUT" | grep -oE 'uuid: [0-9a-f-]{36}' | head -n1 | cut -d' ' -f2)"
if [ -n "$UUID" ]; then pass "callback: the call started in the background ($UUID)"; else fail "callback: no uuid in: $OUT"; fi
task_done() { grep -q '"status": "done"' "$WS/tasks/$UUID-status.json"; }
if [ -n "$UUID" ] && wait_for 20 task_done; then
    pass "callback: the task finished"
    if grep -q 'TEXT-REPLY' "$WS/tasks/$UUID-results.json" && grep -q 'kind text): ok.' "$WS/tasks/$UUID-results.json"; then
        pass "callback: the results file holds the result"
    else
        fail "callback: results file: $(cat "$WS/tasks/$UUID-results.json")"
    fi
    if [ ! -e "$WS/tasks/$UUID.run" ]; then pass "callback: the run marker is cleared"; else fail "callback: the run marker is left"; fi
    STATUS="$(mcp "$ALICE" agent_status "{\"uuid\":\"$UUID\"}")"
    expect "callback: agent_status reports it done" "$STATUS" '"status":"done"'
else
    fail "callback: the task did not finish within 20s"
fi

# ---- 6. The grant. ----
OUT="$(mcp "$BOB" agent_spawn '{"kind":"image","task":"A cat.","mode":"wait","name":"b1"}')"
expect "grant: Bob's image call is refused" "$OUT" "Bob is not allowed to use task models for images."
OUT="$(mcp "$BOB" agent_spawn '{"kind":"text","model":"txt","task":"Hi.","mode":"wait","name":"b2"}')"
expect "grant: Bob's text call runs" "$OUT" "kind text): ok." "TEXT-REPLY"
if find "$HOME_DIR/agents/bob/files/generated" -name 'text-*.md' 2>/dev/null | grep -q .; then pass "grant: Bob's output is in Bob's workspace"; else fail "grant: no output in Bob's workspace"; fi

# ---- 7. Audit rows. ----
audit_ready() { [ "$(audit_rows "agent='alice'" | wc -l)" -ge 8 ]; }
wait_for 10 audit_ready
ROWS="$(audit_rows "agent='alice'")"
if echo "$ROWS" | grep -q '^alice|text|error|.*"model":"txt-down"'; then pass "audit: the failed model's attempt is an error row"; else fail "audit: no error row for txt-down: $ROWS"; fi
for kind in text image tts stt; do
    if echo "$ROWS" | grep -q "^alice|$kind|ok|files/generated/$TODAY/"; then pass "audit: a $kind call is an ok row naming its file"; else fail "audit: no ok $kind row: $ROWS"; fi
done
if [ "$(audit_rows "agent='bob' AND tool='image'" | wc -l)" = 0 ]; then pass "audit: the refused call made no row"; else fail "audit: a row for Bob's refused call"; fi

# ---- 8. Nothing outside the workspaces. ----
STRAY="$(find "$HOME_DIR" "$TMP" -type f \( -name 'text-*' -o -name 'image-*' -o -name 'speech-*' -o -name 'transcript-*' \) \
    ! -path "$WS/files/generated/*" ! -path "$HOME_DIR/agents/bob/files/generated/*" 2>/dev/null)"
if [ -z "$STRAY" ]; then pass "no output outside the agents' files/generated"; else fail "output outside the workspaces: $STRAY"; fi
if [ -z "$(find "$HOME_DIR/internal/temp" -mindepth 1 -maxdepth 1 -name 'task-*' 2>/dev/null)" ]; then pass "no task folder left in internal/temp"; else fail "a task folder is left in internal/temp"; fi

echo "  Task models end to end: $PASS passed, $FAIL failed"
if [ "$FAIL" -ne 0 ]; then
    echo "  --- task model log lines ---"
    grep -iE 'task model|Task model' "$CLAW_LOG" | tail -n 30 | sed 's/^/    /'
    echo "  --- stub log ---"
    tail -n 30 "$STUB_LOG" | sed 's/^/    /'
    exit 1
fi
exit 0
