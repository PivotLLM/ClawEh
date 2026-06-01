// ClawEh
// License: MIT

package llmcontext

import (
	"strings"

	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// tailCronPrefix is the cron-wrapper marker used for noise detection.
// Must match cronWrapperPrefix in pkg/memory/jsonl.go.
const tailCronPrefix = "The following message is from a cron job that fired at "

// selectTail returns the suffix of history to retain in the context window.
//
// Algorithm:
//  1. Walk history newest-to-oldest in turn groups (see resolveGroup).
//  2. Accumulate groups while their token cost fits within budget.
//  3. Minimum floor: if totalMeaningful < minMessages, keep adding groups
//     regardless of budget exhaustion.
//  4. Collapse consecutive noise messages in the retained tail to at most one.
//
// A budget ≤ 0 disables the budget check (only the floor applies).
// estimate converts a message slice into an estimated token count; pass the
// Manager's estTokens so the configured divisor and safety margin apply.
func selectTail(history []providers.Message, budget, minMessages int, estimate func([]providers.Message) int) []providers.Message {
	if len(history) == 0 {
		return nil
	}
	if estimate == nil {
		estimate = estimateTokens
	}

	type span struct{ start, end int }
	var spans []span
	totalTokens := 0
	totalMeaningful := 0

	i := len(history) - 1
	for i >= 0 {
		g := resolveGroup(history, i)
		slice := history[g.start : g.end+1]
		cost := estimate(slice)
		meaningful := countMeaningfulMessages(slice)

		fits := budget <= 0 || totalTokens+cost <= budget
		belowFloor := minMessages > 0 && totalMeaningful < minMessages

		if fits || belowFloor {
			spans = append(spans, span{g.start, g.end})
			totalTokens += cost
			totalMeaningful += meaningful
			i = g.start - 1
		} else {
			break
		}
	}

	if len(spans) == 0 {
		return nil
	}

	// Spans were collected newest-first; reverse for chronological order.
	for lo, hi := 0, len(spans)-1; lo < hi; lo, hi = lo+1, hi-1 {
		spans[lo], spans[hi] = spans[hi], spans[lo]
	}

	total := 0
	for _, s := range spans {
		total += s.end - s.start + 1
	}
	out := make([]providers.Message, 0, total)
	for _, s := range spans {
		out = append(out, history[s.start:s.end+1]...)
	}

	return collapseNoise(out)
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
		if key, ok := cronCollapseKey(m.Content); ok {
			lastCron = key
		}
		lastByRole[m.Role] = m.Content
	}
	return n
}

// collapseNoise removes redundant consecutive noise messages, keeping at most
// one instance from each run of identical same-role messages.
func collapseNoise(msgs []providers.Message) []providers.Message {
	if len(msgs) == 0 {
		return msgs
	}
	out := make([]providers.Message, 0, len(msgs))
	lastByRole := make(map[string]string)
	lastCron := ""
	for _, m := range msgs {
		if isTailNoise(m, lastByRole, lastCron) {
			continue
		}
		if key, ok := cronCollapseKey(m.Content); ok {
			lastCron = key
		}
		lastByRole[m.Role] = m.Content
		out = append(out, m)
	}
	return out
}

// isTailNoise returns true if m is a noise duplicate given the current state.
func isTailNoise(m providers.Message, lastByRole map[string]string, lastCron string) bool {
	if key, ok := cronCollapseKey(m.Content); ok {
		return key != "" && key == lastCron
	}
	prev, ok := lastByRole[m.Role]
	return ok && m.Content == prev
}

// cronCollapseKey returns the collapse key for a cron-wrapper message: the
// fingerprint when present, otherwise the payload (legacy fallback). The bool is
// false for non-cron content.
func cronCollapseKey(content string) (string, bool) {
	fp, payload, ok := parseCronMarker(content)
	if !ok {
		return "", false
	}
	if fp != "" {
		return fp, true
	}
	return payload, true
}

// parseCronMarker reports whether content is a cron-wrapper message. It tolerates
// an optional leading "[<hex>] " fingerprint marker (new format) and also the
// legacy un-marked form. Returns the fingerprint ("" if absent), the payload
// (text after the timestamp line), and ok.
func parseCronMarker(content string) (fingerprint, payload string, ok bool) {
	work := content
	if strings.HasPrefix(work, "[") {
		if end := strings.IndexByte(work, ']'); end > 1 && end <= 12 {
			token := work[1:end]
			if isLowerHex(token) && strings.HasPrefix(work[end:], "] ") {
				fingerprint = token
				work = work[end+2:]
			}
		}
	}

	if !strings.HasPrefix(work, tailCronPrefix) {
		return "", "", false
	}
	if idx := strings.IndexByte(work[len(tailCronPrefix):], '\n'); idx >= 0 {
		payload = work[len(tailCronPrefix)+idx+1:]
	}
	return fingerprint, payload, true
}

// isLowerHex reports whether s is non-empty and consists solely of lowercase
// hexadecimal digits.
func isLowerHex(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') {
			continue
		}
		return false
	}
	return true
}
