// ClawEh
// License: MIT

package llmcontext

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/memory"
	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// EvictionPolicy controls the per-turn, LLM-free eviction sweep that collapses
// re-retrievable tool results (file reads, web fetches) in the live window to a
// short placeholder so the window rarely fills enough to trigger summarization
// compaction. Eviction is reversible: the agent re-reads the resource from disk
// or re-fetches it when the placeholder tells it to, so the only cost of an
// over-eager eviction is a re-read, never lost work.
//
// Turn age is 1-based: the newest turn group is age 1.
//
//   - Superseded (a later read of the same resource, or a → evicted at any age
//     later write/edit to it)                                and any size.
//   - Age <= ProtectTurns (and not superseded)            → never evicted.
//   - Age > EvictTurns                                    → evicted (any size).
//   - Budget valve: if reader-result bytes still exceed   → largest-first eviction
//     BudgetBytes, evict more (age > ProtectTurns)          until under budget.
//
// Supersession is checked before the protect window because a stale duplicate
// the agent has already re-read is pure bloat regardless of recency; the
// most-recent read of each resource is never superseded, so the active view is
// always retained. The protect window therefore guards only the age and budget
// tiers (in practice, mainly the budget valve, which can otherwise fire at any
// age). Shedding large reads under memory pressure is the budget valve's job —
// evicting them on age alone would just force re-reads when there is room.
type EvictionPolicy struct {
	Enabled      bool
	ProtectTurns int
	EvictTurns   int
	BudgetBytes  int  // 0 => derived from contextWindow (~40%)
	NotifyUser   bool // emit a one-line in-conversation notice per eviction
}

// DefaultEvictionPolicy returns the built-in defaults: enabled, protect the last
// 3 turns, evict reads older than 10 turns, with the budget valve shedding the
// largest reads under memory pressure.
func DefaultEvictionPolicy() EvictionPolicy {
	return EvictionPolicy{
		Enabled:      true,
		ProtectTurns: 3,
		EvictTurns:   10,
		BudgetBytes:  0,
		NotifyUser:   false,
	}
}

// EvictionEvent records a single tool-result eviction performed by the sweep.
// It is returned to the agent loop for optional in-conversation notification and
// is always written to the DEBUG log by the sweep itself.
type EvictionEvent struct {
	Seq      int64
	Tool     string // originating tool, e.g. "file_read"
	Resource string // path or URL the tool read
	Bytes    int    // original content size freed
	AgeTurns int    // 1-based turn-group age (1 = newest)
	Reason   string // "superseded" | "stale" | "large" | "budget"
}

// evictionResourceCap caps the resource string in the user-facing notice so a
// very long URL does not flood the conversation.
const evictionResourceCap = 96

// String renders the one-line in-conversation notice:
//
//	[Evicted 18432 bytes at 6 turns (large): file_read files/novels/ch17.md]
func (e EvictionEvent) String() string {
	reason := ""
	if e.Reason != "" {
		reason = " (" + e.Reason + ")"
	}
	return fmt.Sprintf("[Evicted %d bytes at %d turns%s: %s %s]",
		e.Bytes, e.AgeTurns, reason, e.Tool, capResource(e.Resource, evictionResourceCap))
}

func capResource(s string, max int) string {
	if len(s) <= max || max < 2 {
		return s
	}
	return s[:max-1] + "…"
}

// evictionMarker prefixes the placeholder left in place of evicted content so the
// sweep is idempotent (already-evicted results are skipped) and the LLM is told
// how to recover the content.
const evictionMarker = "[evicted:"

func evictionPlaceholder(tool, resource string, bytes int) string {
	return fmt.Sprintf("%s %s %s (%d bytes) — content evicted to save context; re-read if you need it again]",
		evictionMarker, tool, resource, bytes)
}

func isEvicted(content string) bool {
	return strings.HasPrefix(content, evictionMarker)
}

// evictionReaderArg maps a re-retrievable reader tool to the argument naming the
// resource it read. Only these tools are ever evicted — their content can be
// recovered by calling the tool again.
var evictionReaderArg = map[string]string{
	"file_read": "path",
	"file_list": "path",
	"web_fetch": "url",
}

// evictionWriterArg maps a writer tool to the argument naming the resource it
// mutates. A write supersedes any earlier read of the same resource.
var evictionWriterArg = map[string]string{
	"file_write":  "path",
	"file_edit":   "path",
	"file_append": "path",
	"file_copy":   "destination_path",
}

type toolMeta struct {
	tool string
	args map[string]any
}

// normToolCall extracts the (normalized) tool name and argument map from a stored
// ToolCall. Stored history only persists Function.Name/Function.Arguments (the
// runtime Name/Arguments fields are json:"-"), so fall back to those.
func normToolCall(tc providers.ToolCall) (string, map[string]any) {
	name := tc.Name
	if name == "" && tc.Function != nil {
		name = tc.Function.Name
	}
	name = normToolName(name)

	args := tc.Arguments
	if args == nil && tc.Function != nil && strings.TrimSpace(tc.Function.Arguments) != "" {
		_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
	}
	return name, args
}

// normToolName strips any "mcp__server__" namespace prefix so native and
// MCP-published tool names compare equal.
func normToolName(name string) string {
	if i := strings.LastIndex(name, "__"); i >= 0 {
		return name[i+2:]
	}
	return name
}

func readerResource(tool string, args map[string]any) (string, bool) {
	arg, ok := evictionReaderArg[tool]
	if !ok || args == nil {
		return "", false
	}
	v, _ := args[arg].(string)
	v = strings.TrimSpace(v)
	return v, v != ""
}

func writerResource(tool string, args map[string]any) (string, bool) {
	arg, ok := evictionWriterArg[tool]
	if !ok || args == nil {
		return "", false
	}
	v, _ := args[arg].(string)
	v = strings.TrimSpace(v)
	return v, v != ""
}

// evictionBudgetBytes resolves the reader-bytes budget: the configured value, or
// ~40% of the context window (converted tokens→bytes) when unset.
func (m *Manager) evictionBudgetBytes() int {
	if m.cfg.eviction.BudgetBytes > 0 {
		return m.cfg.eviction.BudgetBytes
	}
	if m.cfg.contextWindow <= 0 {
		return 0
	}
	cpt := m.cfg.charsPerToken
	if cpt <= 0 {
		cpt = defaultCharsPerToken
	}
	return int(float64(m.cfg.contextWindow) * cpt * 0.40)
}

// SweepEvictions runs the per-turn eviction pass over the live window. It is
// LLM-free, idempotent, and rewrites the stored history in place (preserving
// seqs) so the saving persists across turns. It returns the evictions performed
// (newest content first is not guaranteed; order follows history position) for
// optional in-conversation notification; every eviction is also DEBUG-logged.
//
// Best-effort: on an empty window or a disabled policy it returns nil and makes
// no changes.
func (m *Manager) SweepEvictions(_ context.Context) []EvictionEvent {
	p := m.cfg.eviction
	if !p.Enabled {
		return nil
	}
	stored := m.store.GetHistoryWithSeqs(m.sessionKey)
	if len(stored) == 0 {
		return nil
	}

	msgs := make([]providers.Message, len(stored))
	for i := range stored {
		msgs[i] = stored[i].Message
	}

	// 1-based turn-group age per message index (newest group = 1).
	ages := make([]int, len(msgs))
	age := 1
	for i := len(msgs) - 1; i >= 0; {
		g := resolveGroup(msgs, i)
		for j := g.start; j <= g.end; j++ {
			ages[j] = age
		}
		age++
		i = g.start - 1
	}

	// Map tool-call ID → (tool, args); track the latest read and write index per
	// resource for supersession. Assistant tool calls precede their results, so
	// callByID is populated before a result references it.
	callByID := make(map[string]toolMeta, len(msgs))
	maxReadIdx := map[string]int{}
	maxWriteIdx := map[string]int{}
	for idx, mm := range msgs {
		for _, tc := range mm.ToolCalls {
			name, args := normToolCall(tc)
			callByID[tc.ID] = toolMeta{tool: name, args: args}
			if res, ok := writerResource(name, args); ok {
				if cur, k := maxWriteIdx[res]; !k || idx > cur {
					maxWriteIdx[res] = idx
				}
			}
		}
		if mm.Role == "tool" && mm.ToolCallID != "" {
			meta := callByID[mm.ToolCallID]
			if res, ok := readerResource(meta.tool, meta.args); ok {
				if cur, k := maxReadIdx[res]; !k || idx > cur {
					maxReadIdx[res] = idx
				}
			}
		}
	}

	// Classify each reader tool-result message.
	type cand struct{ idx, size int }
	marks := map[int]string{}
	var budgetCands []cand
	remainingReaderBytes := 0

	for idx, mm := range msgs {
		if mm.Role != "tool" || mm.ToolCallID == "" || isEvicted(mm.Content) {
			continue
		}
		meta := callByID[mm.ToolCallID]
		res, ok := readerResource(meta.tool, meta.args)
		if !ok {
			continue // not a re-retrievable reader → never evicted
		}
		size := len(mm.Content)
		if size == 0 {
			continue
		}

		// Supersession ignores the protect window: a read with a strictly-later
		// read of the same resource (or a later write/edit to it) is a stale
		// duplicate the agent has already replaced — keeping it, however recent,
		// only bloats the window. The most-recent read of each resource is never
		// superseded, so the agent never loses its current view of a file.
		superseded := false
		if r, k := maxReadIdx[res]; k && r > idx {
			superseded = true
		}
		if w, k := maxWriteIdx[res]; k && w > idx {
			superseded = true
		}
		if superseded {
			marks[idx] = "superseded"
			continue
		}

		// All remaining tiers respect the protect window (the recent working set).
		a := ages[idx]
		if a <= p.ProtectTurns {
			remainingReaderBytes += size // protected: stays in window
			continue
		}
		if a > p.EvictTurns {
			marks[idx] = "stale"
			continue
		}
		budgetCands = append(budgetCands, cand{idx: idx, size: size})
		remainingReaderBytes += size // candidate stays unless budget evicts it
	}

	// Budget valve: evict largest-first among the remaining candidates until the
	// reader-byte total is under budget.
	if budget := m.evictionBudgetBytes(); budget > 0 && remainingReaderBytes > budget {
		sort.Slice(budgetCands, func(i, j int) bool { return budgetCands[i].size > budgetCands[j].size })
		for _, c := range budgetCands {
			if remainingReaderBytes <= budget {
				break
			}
			marks[c.idx] = "budget"
			remainingReaderBytes -= c.size
		}
	}

	if len(marks) == 0 {
		return nil
	}

	// Apply in history order so events and the rewritten content are deterministic.
	events := make([]EvictionEvent, 0, len(marks))
	for idx := range msgs {
		reason, ok := marks[idx]
		if !ok {
			continue
		}
		mm := stored[idx].Message
		meta := callByID[mm.ToolCallID]
		res, _ := readerResource(meta.tool, meta.args)
		ev := EvictionEvent{
			Seq:      stored[idx].Seq,
			Tool:     meta.tool,
			Resource: res,
			Bytes:    len(mm.Content),
			AgeTurns: ages[idx],
			Reason:   reason,
		}
		stored[idx].Message.Content = evictionPlaceholder(meta.tool, res, ev.Bytes)
		events = append(events, ev)
		logger.DebugCF("llmcontext", "evicted tool result", map[string]any{
			"session_key": m.sessionKey,
			"seq":         ev.Seq,
			"tool":        ev.Tool,
			"resource":    ev.Resource,
			"bytes":       ev.Bytes,
			"age_turns":   ev.AgeTurns,
			"reason":      ev.Reason,
		})
	}

	// Persist, preserving seqs when the store supports it.
	if sh, ok := m.store.(interface {
		SetHistoryWithSeqs(string, []memory.StoredMessage)
	}); ok {
		sh.SetHistoryWithSeqs(m.sessionKey, stored)
	} else {
		plain := make([]providers.Message, len(stored))
		for i := range stored {
			plain[i] = stored[i].Message
		}
		m.store.SetHistory(m.sessionKey, plain)
	}
	if err := m.store.Save(m.sessionKey); err != nil {
		logger.WarnCF("llmcontext", "eviction save failed", map[string]any{
			"session_key": m.sessionKey,
			"error":       err.Error(),
		})
	}
	return events
}
