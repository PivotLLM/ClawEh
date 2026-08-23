// ClawEh
// License: MIT

package llmcontext

import (
	"time"

	"github.com/PivotLLM/ClawEh/pkg/cronmsg"
	"github.com/PivotLLM/ClawEh/pkg/memory"
	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// selectTail returns the suffix of history to retain in the context window,
// together with the index in stored the caller should summarize up to: the
// caller summarizes stored[:start] and keeps the returned tail.
//
// start is derived as len(stored)-len(tail) rather than from the span walk, so
// it also absorbs the messages steps 4 and 5 remove. Those messages are noise
// duplicates and partial tool plumbing; folding them into the summarized prefix
// is what actually removes them from the live window. The consequence is that a
// collapsed duplicate can appear both in the summary input and (as its surviving
// copy) in the tail — harmless, since the two are identical by definition.
//
// Algorithm:
//  1. Walk history newest-to-oldest in turn groups (see resolveGroup).
//  2. Accumulate groups while their token cost fits within budget AND they are
//     not older than maxAge.
//  3. Minimum floor: if totalMeaningful < minMessages, keep adding groups
//     regardless of budget or age.
//  4. Advance start past a leading partial tool group so the tail begins on a
//     clean boundary, handing those messages to the summary.
//  5. Collapse consecutive noise messages in the retained tail to at most one.
//
// A budget <= 0 disables the budget check and a maxAge <= 0 disables the age
// check; the floor always applies. estimate converts a message slice into an
// estimated token count; pass the Manager's estTokens so the configured divisor
// and safety margin apply.
func selectTail(
	stored []memory.StoredMessage,
	budget, minMessages int,
	maxAge time.Duration,
	now time.Time,
	estimate func([]providers.Message) int,
) ([]memory.StoredMessage, int) {
	if len(stored) == 0 {
		return nil, 0
	}
	if estimate == nil {
		estimate = estimateTokens
	}

	plain := storedToPlain(stored)

	start := len(stored)
	totalTokens := 0
	totalMeaningful := 0

	i := len(stored) - 1
	for i >= 0 {
		g := resolveGroup(plain, i)
		cost := estimate(plain[g.start : g.end+1])
		meaningful := countMeaningfulMessages(plain[g.start : g.end+1])

		fits := budget <= 0 || totalTokens+cost <= budget
		// A group is judged by its OLDEST message: turn groups span seconds, so
		// which end is measured is immaterial in practice, and taking the oldest
		// keeps the cap honest about the age of what is retained.
		fresh := maxAge <= 0 || !isOlderThan(stored[g.start].CreatedAt, maxAge, now)
		belowFloor := minMessages > 0 && totalMeaningful < minMessages

		if (fits && fresh) || belowFloor {
			start = g.start
			totalTokens += cost
			totalMeaningful += meaningful
			i = g.start - 1
			continue
		}
		break
	}

	if start >= len(stored) {
		return nil, len(stored)
	}

	start = advancePastPartialToolGroup(stored, start)
	if start >= len(stored) {
		return nil, len(stored)
	}
	tail := collapseStoredNoise(stored[start:])
	return tail, len(stored) - len(tail)
}

// isOlderThan reports whether ts is more than maxAge before now. A zero
// timestamp is never considered old: messages written before CreatedAt existed
// must not be evicted by an age rule that cannot see them.
func isOlderThan(ts time.Time, maxAge time.Duration, now time.Time) bool {
	if ts.IsZero() {
		return false
	}
	return now.Sub(ts) > maxAge
}

// advancePastPartialToolGroup moves start forward past any leading tool result
// or assistant tool-call message so the retained tail begins on a clean
// boundary — a user message, or an assistant message without tool calls.
// resolveGroup binds a tool result back to its assistant, so a budget or age cut
// can leave the tail starting mid-group; without this the provider sanitizer
// would drop that leading group on every single dispatch (and the context would
// be neither kept nor summarized). Advancing the index rather than trimming the
// returned slice hands those messages to the summary, where they belong.
func advancePastPartialToolGroup(stored []memory.StoredMessage, start int) int {
	for start < len(stored) {
		r := stored[start].Role
		if r == "tool" || (r == "assistant" && len(stored[start].ToolCalls) > 0) {
			start++
			continue
		}
		break
	}
	return start
}

type groupBounds struct{ start, end int }

// resolveGroup returns the index bounds of the atomic turn group ending at end.
// If history[end] has a ToolCallID, the group extends back to the assistant
// message whose ToolCalls slice contains a matching ID.
// If no match is found, the group is just {end, end}.
func resolveGroup(history []providers.Message, end int) groupBounds {
	id := history[end].ToolCallID
	if id == "" {
		return groupBounds{end, end}
	}
	for j := end - 1; j >= 0; j-- {
		for _, tc := range history[j].ToolCalls {
			if tc.ID == id {
				return groupBounds{j, end}
			}
		}
	}
	return groupBounds{end, end}
}

// countMeaningfulMessages counts non-noise messages in a slice using the same
// stateful noise definition as the storage layer: identical content for the same
// role, or identical cron key (fingerprint-or-payload) for cron-wrapper messages.
func countMeaningfulMessages(msgs []providers.Message) int {
	lastByRole := make(map[string]string)
	lastCron := ""
	n := 0
	for _, m := range msgs {
		if isTailNoise(m, lastByRole, lastCron) {
			continue
		}
		n++
		if key, ok := cronmsg.CollapseKey(m.Content); ok {
			lastCron = key
		}
		lastByRole[m.Role] = m.Content
	}
	return n
}

// collapseStoredNoise removes redundant consecutive noise messages, keeping at
// most one instance from each run of identical same-role messages.
func collapseStoredNoise(msgs []memory.StoredMessage) []memory.StoredMessage {
	if len(msgs) == 0 {
		return msgs
	}
	out := make([]memory.StoredMessage, 0, len(msgs))
	lastByRole := make(map[string]string)
	lastCron := ""
	for _, m := range msgs {
		if isTailNoise(m.Message, lastByRole, lastCron) {
			continue
		}
		if key, ok := cronmsg.CollapseKey(m.Content); ok {
			lastCron = key
		}
		lastByRole[m.Role] = m.Content
		out = append(out, m)
	}
	return out
}

// isTailNoise returns true if m is a noise duplicate given the current state.
func isTailNoise(m providers.Message, lastByRole map[string]string, lastCron string) bool {
	if key, ok := cronmsg.CollapseKey(m.Content); ok {
		return key != "" && key == lastCron
	}
	prev, ok := lastByRole[m.Role]
	return ok && m.Content == prev
}
