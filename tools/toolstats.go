// ClawEh
// License: MIT

package tools

import (
	"sync"

	"github.com/PivotLLM/ClawEh/agenttoken"
)

// maxLastToolErrorRunes bounds the remembered text of the last failed call.
const maxLastToolErrorRunes = 300

// toolStats is the tally of one registered run's tool calls.
type toolStats struct {
	calls     int
	errors    int
	lastError string
}

// toolStatsRegistry holds the tallies of the runs that asked for one, keyed by
// session key. Only sub-agent runs register (see BeginToolStats), so a primary
// session's tool calls find no entry and cost one map lookup.
var toolStatsRegistry = struct {
	mu      sync.Mutex
	entries map[string]*toolStats
}{entries: map[string]*toolStats{}}

// BeginToolStats starts counting the tool calls made under sessionKey. Only
// sub-agent runs call it — their callers need to know whether the worker's
// tools worked — so primary sessions are never tallied. Beginning a key that is
// already registered resets its tally.
func BeginToolStats(sessionKey string) {
	if sessionKey == "" {
		return
	}
	toolStatsRegistry.mu.Lock()
	defer toolStatsRegistry.mu.Unlock()
	toolStatsRegistry.entries[sessionKey] = &toolStats{}
}

// RecordToolResult counts one tool call made under sessionKey. A nil result or
// one with IsError counts as a failure, and its text (prefixed with the tool
// name, token-redacted and truncated) becomes the run's last error. It is a
// no-op for a key that was never registered, which is every primary session.
func RecordToolResult(sessionKey, toolName string, r *ToolResult) {
	if sessionKey == "" {
		return
	}
	toolStatsRegistry.mu.Lock()
	defer toolStatsRegistry.mu.Unlock()
	st, ok := toolStatsRegistry.entries[sessionKey]
	if !ok {
		return
	}
	st.calls++
	if r != nil && !r.IsError {
		return
	}
	st.errors++
	st.lastError = toolName + ": " + failureText(r)
}

// EndToolStats stops counting for sessionKey and returns its tally. An unknown
// key returns zeros.
func EndToolStats(sessionKey string) (calls, errors int, lastError string) {
	toolStatsRegistry.mu.Lock()
	defer toolStatsRegistry.mu.Unlock()
	st, ok := toolStatsRegistry.entries[sessionKey]
	if !ok {
		return 0, 0, ""
	}
	delete(toolStatsRegistry.entries, sessionKey)
	return st.calls, st.errors, st.lastError
}

// failureText is the token-redacted, bounded description of a failed call.
func failureText(r *ToolResult) string {
	text := "tool returned nil result"
	if r != nil {
		text = r.ForLLM
		if text == "" && r.Err != nil {
			text = r.Err.Error()
		}
	}
	text = agenttoken.Redact(text)
	if runes := []rune(text); len(runes) > maxLastToolErrorRunes {
		text = string(runes[:maxLastToolErrorRunes]) + "…"
	}
	return text
}
