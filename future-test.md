# Future Test Specifications

Tests deferred from the current implementation cycle. Implement these after the remediation items in `remediation-1.md` are complete and the system is stable.

---

## Long-Run Simulation Test

**Goal:** Verify that the context management system remains correct and performant across months-equivalent usage without manual intervention.

**Location:** `pkg/llmcontext/longrun_test.go` (build tag `//go:build longrun`)

**Run command:** `go test -tags longrun -timeout 10m ./pkg/llmcontext/...`

---

### Scenario 1: Sustained Cron Noise

Simulate a cron agent that receives a short wake-up message every iteration with no meaningful content change. Verify that the cooling mechanism suppresses repeated low-gain compression attempts.

**Setup:**
- Context window: 8000 tokens
- Normal threshold: 60%, safety: 85%
- Message threshold: 20
- 500 iterations; each iteration adds one 20-char user message and one 30-char assistant message ("cron tick N: nothing to do" / "Acknowledged.")

**Assertions:**
- Total compress calls < 30 (cooling suppresses the majority)
- Final history length < 60 messages (tail selection keeps only recent turns)
- No `doCompress` error returned at any iteration
- Final stored summary is valid JSON parseable by `unmarshalSummary`
- Archive seq range is monotonically increasing with no gaps at boundaries

---

### Scenario 2: Large Tool Outputs

Simulate an agent that periodically receives large tool results (e.g. file reads, API responses). Verify that large messages are handled before persistence and do not cause runaway context growth.

**Setup:**
- Context window: 16000 tokens
- Safety threshold: 80%
- 50 iterations; every 5th iteration inserts a tool-result message of 12000 chars (~3000 tokens)
- Remaining iterations alternate normal 200-char user/assistant pairs

**Assertions:**
- No iteration leaves total estimated tokens above `contextWindow` after `AddToolResult` + `PreDispatchCheck`
- Large tool messages written to store are truncated to ≤ `maxSingleMessageTokens` (from `applyLargeMsgChecks`)
- After truncation, re-estimated size stays under safety threshold
- Archive contains the original (un-truncated) content for the large tool messages at the correct seq numbers
- `get_session_messages` returns the archived content when queried by those seq numbers

---

### Scenario 3: Multiple Compress Cycles with Restarts

Verify that compaction state survives process restarts and that coverage seq fields remain consistent across cycles.

**Setup:**
- Context window: 4000 tokens
- Normal threshold: 50%, retain: 20%
- 10 compress cycles; after each cycle, serialize manager state, reconstruct a new manager from the persisted store, and continue
- Each cycle adds 40 pairs of 100-char messages before triggering manual `Compact()`

**Assertions:**
- Each cycle produces a new stored summary; `unmarshalSummary` must parse it
- `CoveredSeqStart` and `CoveredSeqEnd` in each summary span the messages summarized in that cycle (no overlap with retained tail)
- After each restart, `msgCount` and `compressedAtCount` are restored from persisted meta (not reset to zero)
- Cooling state round-trips correctly: if cooling was true before restart, it is true after
- The 10th summary's `CoveredSeqEnd` ≈ 10 × 80 − (retained tail length); verify ±2

---

### Scenario 4: Restart Mid-Turn

Verify that `PendingTurn` recovery correctly replays the outstanding user message and that no duplicate messages are stored.

**Setup:**
- Build a session with 20 message pairs in a JSONL store
- Set `PendingTurn = true` via `SetPendingTurn`
- Add a user message as the last entry (no assistant reply)
- Call `ListPendingSessions`; verify the session key is returned
- Simulate recovery: call the recovery logic, capturing the bus message published
- Call `ClearPendingTurn`; verify `ListPendingSessions` no longer returns the key

**Assertions:**
- Recovery bus message has `IsRetry: true`
- Recovery bus message `SessionKey` equals the pre-resolved session key
- `Metadata["preresolved_agent_id"]` is set to the correct agent ID
- History after recovery has exactly 21 user messages (no duplication of the last one)
- After `ClearPendingTurn`, `PendingTurn` is `false` in the `.meta.json` file on disk

---

### Scenario 5: Manual `/compact` and `/clear`

Verify the manual compaction and clear commands leave the system in a fully consistent state.

**Setup:**
- Session with 100 pairs of 150-char messages; trigger one automatic compress
- Then call `Compact()` manually; verify it succeeds (not no-op due to cooling)
- Then call `Reset()` (the `/clear` path)

**Assertions for `/compact`:**
- `Compact()` returns nil (no error)
- Stored summary changes (new summary replaces old)
- History length decreases
- Archive bounds advance: new `archiveSeqEnd` > previous `archiveSeqEnd`

**Assertions for `/clear`:**
- `GetHistory` returns empty slice
- `GetSummary` returns empty string
- Archive file (`.archive.db`) is deleted or its message table is empty
- `GetArchiveBounds` returns `(0, 0)`
- `ListPendingSessions` does not return this session key
- A new message added after clear gets seq = 1 (reset, not continuing old seq)

---

### Scenario 6: Month-Equivalent Message Volume

Stress test total throughput and verify request size stays under provider limits.

**Assumptions:** 200 messages/day × 90 days = 18 000 messages. Each message averages 120 chars.

**Setup:**
- Context window: 100 000 tokens (Claude 3.5 Sonnet equivalent)
- Normal threshold: 70%, safety: 90%, retain: 15%
- Message threshold: 50
- Insert 18 000 alternating user/assistant pairs of ~120 chars each
- After every 100 inserts, call `Build()` and measure estimated token count of the returned messages
- Track all compress calls and their outcomes

**Assertions:**
- `Build()` estimated token count never exceeds `contextWindow` at any of the 180 sample points
- Total automatic compress calls between 50 and 400 (reasonable frequency)
- No compress call returns a non-nil error
- Final summary is valid and `CoveredSeqStart` is within 500 of seq 1 (summary covers early history)
- `get_session_messages` can retrieve message at seq 1 from the archive
- Archive DB file size < 100 MB for 18 000 × 120-char messages (payload JSON overhead vs raw text)
- Total test wall time < 5 minutes (performance baseline)

---

### Scenario 7: FTS5 Search Correctness

Verify full-text search returns correct results and does not silently miss content.

**Setup:**
- Insert 200 archive messages; 10 of them contain the phrase "quarterly revenue report"
- 5 of those 10 also contain the word "deficit"
- All others contain random office-noise text with no overlap

**Assertions:**
- `Search("quarterly revenue report")` returns exactly 10 results
- `Search("quarterly revenue report deficit")` returns exactly 5 results (implicit AND)
- `Search("quarterly OR deficit")` returns ≥ 10 results
- `Search("quarterly NOT deficit")` returns exactly 5 results
- `Search` with a malformed FTS5 expression (e.g. `"AND"`) returns a non-nil error, not a panic
- Seq numbers in results match the seq numbers of the inserted messages (no off-by-one)

---

### Scenario 8: Concurrent Session Safety

Verify that two goroutines writing to different sessions in the same JSONL store do not corrupt each other's data, and that per-session write mutexes prevent interleaved writes within a session.

**Setup:**
- 5 concurrent goroutines, each writing to a distinct session key
- Each goroutine inserts 100 pairs and calls `Save` after each insert
- One additional goroutine reads each session's history every 10ms throughout

**Assertions:**
- No data race detected (`-race` flag)
- Final history length for each session is exactly 200 messages
- No message from session A appears in session B's history
- No `Save` call returns an error

---

### Notes

- All scenarios except Scenario 8 are single-goroutine to keep assertions deterministic.
- Scenarios 1–6 require a real JSONL store on a temp directory (use `t.TempDir()`).
- All scenarios that touch the archive require a real `ArchiveStore` opened via
  `pkg/memory/archive.Open()` on a `t.TempDir()` path (not a mock).
- Mock LLM clients should return valid `Summary` JSON with incrementing `covered_seq_end` values to simulate realistic summarization.
- The ContextManager under test must be constructed with `WithArchiveDir(t.TempDir())`
  so the archive lifecycle is exercised.
- Scenario 2 assertions that reference "original un-truncated content" are correct:
  the archive receives the full message before `applyLargeMsgChecks` truncates the
  active context window. The truncated version appears only in the active JSONL store.
- Scenario 3 restart simulation must reconstruct a new ContextManager from the same
  JSONL store and archive dir (not from a mock) to exercise the `CompactionStateStore`
  interface round-trip.
- Scenario 4 uses `ListPendingSessions` on the real JSONL store to find the pending
  session, not a hardcoded session key.
- Scenario 8 concurrent safety test must use a real `ArchiveStore` (not mock) and
  run with `-race` to detect data races in the write mutex path.
- If the long-run test suite is added to CI, run it nightly rather than on every PR (use the `longrun` build tag to gate it).
