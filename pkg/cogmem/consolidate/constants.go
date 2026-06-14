// ClawEh - Cognitive Memory
// License: MIT

package consolidate

// PromptFilename is the per-agent consolidation prompt. It is seeded into each
// agent workspace by internal/workspace.Populate (write-if-missing) and is
// operator-editable; the worker loads <workspace>/COGMEM.md and falls back to
// the embedded default when absent.
const PromptFilename = "COGMEM.md"

// Batching defaults (DEC-4 / COGMEM-TODO §3). These are levers, surfaced
// per-agent via config and the worker's functional options; the values here are
// the fallback defaults.
const (
	defaultMaxMessages     = 200
	defaultMaxInputTokens  = 96000
	defaultPerMessageChars = 12000
	defaultMaxOutputTokens = 8000

	// approxCharsPerToken is the tokenizer-free estimate (~4 chars/token).
	approxCharsPerToken = 4
)

// Actor labels recorded in the audit ledger.
const (
	actorSleepCycle = "sleep_cycle"
)
