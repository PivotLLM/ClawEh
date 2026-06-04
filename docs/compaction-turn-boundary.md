# Compaction: turn-boundary first stage, emergency-only mid-turn

## Goal
Stop first-stage (size and message-count) compaction from firing mid-turn (between
tool-call iterations). Only the emergency (safety-net) level may compact mid-turn,
to avoid exceeding the model context window. First-stage compaction is deferred to
the turn boundary (after the LLM responds / before the next input is processed).
Also raise the default message-count threshold from 20 to 100 so size is the
primary driver.

## Trigger points today
- `triggerCheck` — runs on `AddUserMessage` (turn start) and `AddAssistantMessage`
  (turn end). Full logic: safety-net OR (normal-percent OR count) respecting cooldown.
- `PreDispatchCheck` — top of **every** tool-call iteration. Full logic. **This is
  what fired mid-turn.**
- `CheckAndCompress` — once, after Build, before the first dispatch. minPercent gate
  then safety-net OR normal-percent (no count trigger).
- `AddToolCallMessage` / `AddToolResult` — no trigger (deferred by design).

## Changes
1. `defaultMessageThreshold`: 20 → 100 (`pkg/llmcontext/options.go`).
2. `PreDispatchCheck`: fire only when `contextPct >= safetyPercent` (emergency).
   Remove the normal-percent and count branch.
3. `CheckAndCompress`: fire only when `contextPct >= safetyPercent` (emergency).
   Remove the normal-percent branch.
4. `triggerCheck`: unchanged. It remains the first-stage (normal + count) path and
   runs only at turn boundaries (`AddAssistantMessage` = after the LLM responds;
   `AddUserMessage` = before the next input is processed).

## Result
- Mid-turn (tool-call loop): only emergency compaction at `safetyPercent` (default 80%).
- End of turn (`AddAssistantMessage`): first-stage compaction (normal 50% or count 100).
- Count-based compaction now fires ~once per ~100 messages, at a turn boundary, not
  multiple times within a single tool-heavy turn.

## Notes
- The emergency path (`safetyNet=true`) has a non-LLM fallback (drop oldest groups),
  so it does not return `ErrCompressionFailed`. The normal-path LLM-failure →
  `ErrCompressionFailed` behavior now only occurs inside `triggerCheck` (which logs
  and continues), not through `PreDispatchCheck`/`CheckAndCompress`. Tests updated
  accordingly.
