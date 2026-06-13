# Dispatch Logging

## Goal

After every LLM dispatch, ClawEh writes a single INFO-level **finish event**
containing the model that ran, token counts (input, output, cache read,
cache creation), number of turns the provider used, stop reason, USD cost
(when known), wall-clock duration, and a `success` boolean. The finish event
is written for **every** dispatch — success or failure — so log scrapers can
reconstruct usage and cost without crawling the rest of the log.

Each dispatch is paired with a **dispatch event** written immediately before
the provider call. The dispatch event is intentionally minimal: it ties the
finish event back to the agent loop iteration and records what was requested.

## DispatchStatus struct

Added to `pkg/providers/protocoltypes/types.go` alongside `UsageInfo`:

```go
// DispatchStatus is populated by each provider on every Chat() return
// (success or error) and surfaced in the LLMResponse so the agent loop
// can write a uniform finish event.
type DispatchStatus struct {
    Success              bool    `json:"success"`
    Model                string  `json:"model"`                  // exact model id the provider actually ran (e.g. "claude-sonnet-4-5-20250929")
    NumTurns             int     `json:"num_turns"`              // 1 for non-agentic single-shot providers
    InputTokens          int     `json:"input_tokens"`
    OutputTokens         int     `json:"output_tokens"`
    CacheReadTokens      int     `json:"cache_read_tokens"`      // 0 when unreported
    CacheCreationTokens  int     `json:"cache_creation_tokens"`  // 0 when unreported
    StopReason           string  `json:"stop_reason"`            // "success" / "error" / "max_turns" / native API stop_reason / "" if unknown
    CostUSD              float64 `json:"cost_usd"`               // 0 when unreported
    DurationMs           int64   `json:"duration_ms"`            // wall-clock for this dispatch
    BytesSent            int64   `json:"bytes_sent"`             // raw bytes written to wire/stdin after marshalling
    BytesReceived        int64   `json:"bytes_received"`         // raw bytes read from wire/stdout before unmarshalling
}
```

`LLMResponse` gains `Status *DispatchStatus`. `Usage *UsageInfo` is kept as-is
for compatibility with existing call sites and tests; `DispatchStatus` is the
authoritative source for the finish event.

Providers populate `Status` for **both** success and error returns. On error,
providers return `(response, err)` where `response.Status` is set with
`Success: false` and best-effort fields. The agent-loop call-site wrapper
synthesizes a fallback `DispatchStatus` if a provider returns `nil` on error,
so the finish event always fires.

### Byte counting rules

`BytesSent` and `BytesReceived` are **raw transport bytes**, captured around
the wire format — i.e. after the request is marshalled and before the
response is unmarshalled. They are never derived from token counts.

- HTTP providers (`anthropic`, `anthropic_messages`, `openai_compat`, `azure`,
  `http_provider`, `legacy_provider`): `BytesSent = len(requestBodyBytes)`
  written to the HTTP request; `BytesReceived = len(responseBodyBytes)`
  read off the response, summed across all chunks for streaming.
- CLI providers (`claude_cli`, `codex_cli`, `gemini_cli`): `BytesSent` is the
  total bytes written to the child process's stdin (after JSON marshalling
  any structured input); `BytesReceived` is the total bytes read from the
  child's stdout. stderr is not counted.
- Bedrock: AWS SDK Converse abstracts the wire format; instrument via a
  smithy HTTP middleware (`stack.Finalize.Add`) that wraps the response body
  in a counting reader and captures the request body length from
  `request.GetBody()`. If middleware instrumentation is not feasible in this
  pass, set both fields to 0 and document the gap — do not synthesise from
  token counts.
- On error, populate whatever was successfully measured (e.g. `BytesSent`
  may be set even when the response never arrived).

## CLI provider mapping (validated)

The three CLIs were exercised on the development host with their JSON-output
modes. The captured shapes below are the source of truth for each provider's
adapter.

### claude-cli

Command: `claude -p --output-format json --model haiku "<prompt>"`

```json
{
  "type":"result","subtype":"success","is_error":false,
  "duration_ms":2753,"duration_api_ms":4206,
  "num_turns":1,
  "result":"hi",
  "stop_reason":"end_turn",
  "session_id":"...",
  "total_cost_usd":0.104551,
  "usage":{
    "input_tokens":10,
    "cache_creation_input_tokens":83112,
    "cache_read_input_tokens":0,
    "output_tokens":47
  },
  "modelUsage":{
    "claude-haiku-4-5-20251001":{
      "inputTokens":356,"outputTokens":61,
      "cacheReadInputTokens":0,"cacheCreationInputTokens":83112,
      "costUSD":0.104551
    }
  }
}
```

Mapping:

| DispatchStatus field   | Source                                              |
| ---------------------- | --------------------------------------------------- |
| `Success`              | `!is_error`                                         |
| `Model`                | first key of `modelUsage` (exact, dated id)         |
| `NumTurns`             | `num_turns`                                         |
| `InputTokens`          | `usage.input_tokens`                                |
| `OutputTokens`         | `usage.output_tokens`                               |
| `CacheReadTokens`      | `usage.cache_read_input_tokens`                     |
| `CacheCreationTokens`  | `usage.cache_creation_input_tokens`                 |
| `StopReason`           | `stop_reason` ("end_turn", "tool_use", …)           |
| `CostUSD`              | `total_cost_usd`                                    |
| `DurationMs`           | `duration_ms` (CLI-reported wall time)              |
| `BytesSent`            | bytes written to claude-cli stdin                   |
| `BytesReceived`        | bytes read from claude-cli stdout                   |

### codex-cli

Command: `codex exec --json --skip-git-repo-check --sandbox read-only --dangerously-bypass-approvals-and-sandbox "<prompt>"`

Emits JSONL; last `turn.completed` event carries usage. Captured sample:

```
{"type":"thread.started","thread_id":"019e1d91-..."}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"hi"}}
{"type":"turn.completed","usage":{"input_tokens":13707,"cached_input_tokens":7552,"output_tokens":5,"reasoning_output_tokens":0}}
```

Mapping:

| DispatchStatus field   | Source                                              |
| ---------------------- | --------------------------------------------------- |
| `Success`              | no `error` or `turn.failed` event observed          |
| `Model`                | model passed to the CLI via `-m` (provider knows it)|
| `NumTurns`             | count of `turn.completed` events                    |
| `InputTokens`          | `usage.input_tokens` from final `turn.completed`    |
| `OutputTokens`         | `usage.output_tokens`                               |
| `CacheReadTokens`      | `usage.cached_input_tokens`                         |
| `CacheCreationTokens`  | 0 (not reported)                                    |
| `StopReason`           | `"success"` on completion, `"error"` on failure     |
| `CostUSD`              | 0 (not reported)                                    |
| `DurationMs`           | wall-clock measured by the provider wrapper         |
| `BytesSent`            | bytes written to codex-cli stdin                    |
| `BytesReceived`        | bytes read from codex-cli stdout (full JSONL stream)|

### gemini-cli

Command: `gemini -p "<prompt>" -o json --skip-trust --yolo`

```json
{
  "session_id":"df620c66-...","response":"hi",
  "stats":{
    "models":{
      "gemini-3-flash-preview":{
        "api":{"totalRequests":1,"totalErrors":0,"totalLatencyMs":3205},
        "tokens":{"input":2470,"prompt":64881,"candidates":1,"total":65054,"cached":62411,"thoughts":172,"tool":0},
        "roles":{"main":{"totalRequests":1,"totalErrors":0,"totalLatencyMs":3205,"tokens":{...}}}
      },
      "gemini-2.5-flash-lite":{ "...":"utility_router" }
    },
    "tools":{"totalCalls":0,...},
    "files":{"totalLinesAdded":0,"totalLinesRemoved":0}
  }
}
```

The CLI runs auxiliary models (`utility_router`) alongside the user's main
model. The adapter picks the model whose `roles` map contains `"main"`; if
no `main` role exists (older CLI), it falls back to the largest-token model.

Mapping:

| DispatchStatus field   | Source                                              |
| ---------------------- | --------------------------------------------------- |
| `Success`              | `stats.models[main].api.totalErrors == 0`           |
| `Model`                | key of the model whose `roles` contains `main`      |
| `NumTurns`             | `stats.models[main].api.totalRequests`              |
| `InputTokens`          | `stats.models[main].tokens.prompt`                  |
| `OutputTokens`         | `stats.models[main].tokens.candidates`              |
| `CacheReadTokens`      | `stats.models[main].tokens.cached`                  |
| `CacheCreationTokens`  | 0 (not reported)                                    |
| `StopReason`           | `"success"` or `"error"`                            |
| `CostUSD`              | 0 (not reported)                                    |
| `DurationMs`           | `stats.models[main].api.totalLatencyMs`             |
| `BytesSent`            | bytes written to gemini-cli stdin                   |
| `BytesReceived`        | bytes read from gemini-cli stdout                   |

## API provider mapping

### anthropic / anthropic_messages / bedrock (Anthropic Messages API)

Response payload already parsed in
`pkg/providers/anthropic_messages/provider.go` and the streaming-aware
`pkg/providers/anthropic/provider.go`. Anthropic's response object includes
`stop_reason`, `model`, and an enriched `usage` block.

Mapping:

| DispatchStatus field   | Source                                              |
| ---------------------- | --------------------------------------------------- |
| `Success`              | HTTP 2xx and no top-level `error` payload           |
| `Model`                | `model` field of the response (echoed dated id)     |
| `NumTurns`             | 1                                                   |
| `InputTokens`          | `usage.input_tokens`                                |
| `OutputTokens`         | `usage.output_tokens`                               |
| `CacheReadTokens`      | `usage.cache_read_input_tokens` (if present)        |
| `CacheCreationTokens`  | `usage.cache_creation_input_tokens` (if present)    |
| `StopReason`           | `stop_reason` ("end_turn", "tool_use", "max_tokens", "stop_sequence", "refusal") |
| `CostUSD`              | 0 (Anthropic API does not return cost)              |
| `DurationMs`           | wall-clock measured in the provider                 |
| `BytesSent`            | HTTP request body length                            |
| `BytesReceived`        | HTTP response body length (sum across SSE chunks for streaming) |

The existing `usageInfo` struct in `anthropic_messages/provider.go` must be
extended with `CacheCreationInputTokens` and `CacheReadInputTokens` fields.

### openai_compat / azure / http (`pkg/providers/common/ParseResponse`)

Mapping:

| DispatchStatus field   | Source                                              |
| ---------------------- | --------------------------------------------------- |
| `Success`              | HTTP 2xx and choices[0] present                     |
| `Model`                | top-level `model` field (added to the inner anon struct) |
| `NumTurns`             | 1                                                   |
| `InputTokens`          | `usage.prompt_tokens`                               |
| `OutputTokens`         | `usage.completion_tokens`                           |
| `CacheReadTokens`      | `usage.prompt_tokens_details.cached_tokens` (if present) |
| `CacheCreationTokens`  | 0 (OpenAI does not split cache creation)            |
| `StopReason`           | `choices[0].finish_reason` ("stop", "length", "tool_calls", "content_filter") |
| `CostUSD`              | 0 (not in API)                                      |
| `DurationMs`           | wall-clock measured in the provider                 |
| `BytesSent`            | HTTP request body length                            |
| `BytesReceived`        | HTTP response body length                           |

`ParseResponse` will be amended to also parse `model` and (where present)
`usage.prompt_tokens_details.cached_tokens` and to attach a `DispatchStatus`
to the returned `LLMResponse`. The wall-clock and `Success` fields are filled
by the calling provider (which knows the HTTP outcome).

### claude_provider.go / legacy_provider.go

`claude_provider.go` is a small wrapper around `anthropic_messages` — it
inherits the mapping above. `legacy_provider.go` (older completion-style API)
populates what it can and leaves cache fields zero.

## Logging events

Both events use the `agent` log facility at INFO level via
`logger.InfoCF("agent", ..., fields)`. They are emitted by the **agent loop
call-site wrapper** that wraps the `callLLM` closure in
`pkg/agent/loop.go` (around line 1451), and by the equivalent loop in
`pkg/tools/toolloop.go`. Providers themselves do **not** emit dispatch /
finish events — they only populate `DispatchStatus`. This keeps the
single-source-of-truth at the call site and avoids double-logging when
fallback retries through multiple providers.

### Dispatch event (before call)

Message: `"LLM dispatch"`

Fields:

- `agent_id`
- `iteration`
- `provider` — protocol name, e.g. "anthropic_messages", "claude_cli"
- `model` — model id requested
- `num_messages`
- `num_tools`
- `max_tokens`

### Finish event (after call, success or failure)

Message: `"LLM finish"`

Fields:

- `agent_id`
- `iteration`
- `success` — bool
- `provider`
- `model` — the model the provider *actually* used (from `DispatchStatus.Model`); falls back to the request model
- `num_turns`
- `input_tokens`
- `output_tokens`
- `cache_read_tokens`
- `cache_creation_tokens`
- `stop_reason`
- `cost_usd`
- `duration_ms`
- `bytes_sent`
- `bytes_received`
- `error` — present only on failure, truncated to 500 chars

### Existing log removal

The current "LLM call succeeded" log in `loop.go:1473` and the per-provider
"claude-cli response" / "codex-cli response" / "gemini-cli response" INFO
logs become redundant. They are removed to avoid duplicate noise. The
per-provider parser may still emit a `DebugCF` line with raw parser
diagnostics — that's fine.

## Test plan

Unit tests, per provider:

- `claude_cli_provider_test.go` — assert `DispatchStatus` is populated from
  the captured JSON fixture (use the sample in this doc as a baseline) for
  both success and `is_error: true` cases.

- `codex_cli_provider_test.go` — assert `DispatchStatus` is populated from
  the JSONL stream, including `NumTurns` from event count and `Success: false`
  on `turn.failed` / `error` events.

- `gemini_cli_provider_test.go` — assert the `main` role is preferred over
  `utility_router`; assert fallback to largest-token model when no `main`
  role exists.

- `anthropic_messages/provider_test.go` — extend existing fixtures with
  `cache_creation_input_tokens` and `cache_read_input_tokens`, assert the
  new fields propagate.

- `openai_compat/provider_test.go` — add a fixture with
  `prompt_tokens_details.cached_tokens` and `model` set, assert propagation.

- `agent/loop_test.go` — table-driven test asserting both `"LLM dispatch"`
  and `"LLM finish"` records are written exactly once per dispatch, with all
  required fields present.

CLI validation:

- Run `claude -p --output-format json --model haiku "hi"`, `codex exec --json …`,
  and `gemini -p … -o json --skip-trust --yolo` against this host and confirm
  the captured shapes match the live output. (Already done while authoring
  this doc; capture the commands in a doc/test fixture.)

End-to-end:

- Start `claw` with `LOG_LEVEL=info`, send one message through each of the
  six providers we run (claude-cli, codex-cli, gemini-cli,
  anthropic_messages, openai_compat, bedrock), and grep the log for
  paired `"LLM dispatch"` / `"LLM finish"` lines with `success=true` and
  populated token counts.

## Files touched

- `pkg/providers/protocoltypes/types.go` — add `DispatchStatus`, add
  `Status *DispatchStatus` to `LLMResponse`.
- `pkg/providers/claude_cli_provider.go` — populate `Status`; remove the
  redundant "claude-cli response" INFO log (downgrade to DEBUG).
- `pkg/providers/codex_cli_provider.go` — same.
- `pkg/providers/gemini_cli_provider.go` — same.
- `pkg/providers/anthropic_messages/provider.go` — extend `usageInfo`; set
  `Status`.
- `pkg/providers/anthropic/provider.go` — set `Status` (including streaming
  path that aggregates `message_delta` usage).
- `pkg/providers/bedrock/provider_bedrock.go` — set `Status`.
- `pkg/providers/common/common.go` — extend `ParseResponse` to surface
  `model` and `cached_tokens`; partial `Status` population (caller fills
  duration + success).
- `pkg/providers/openai_compat/provider.go` — fill `Status.Success`,
  `DurationMs`.
- `pkg/providers/azure/provider.go` — same.
- `pkg/providers/http_provider.go`, `legacy_provider.go`,
  `claude_provider.go` — minor — pass through.
- `pkg/agent/loop.go` — emit `"LLM dispatch"` and `"LLM finish"` events;
  remove "LLM call succeeded".
- `pkg/tools/toolloop.go` — same logging change.
- Provider tests for every file above.

## Non-goals

- This change does not introduce a metrics endpoint. The finish event is the
  scrapable surface.
- It does not change cost accounting or budgeting behaviour. `cost_usd` is
  logged where the upstream provider returns it; downstream summing is left
  to log analysis.
- It does not redact prompt content. The finish event carries only metadata.
