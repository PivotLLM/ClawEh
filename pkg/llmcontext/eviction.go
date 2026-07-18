// ClawEh
// License: MIT

package llmcontext

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
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
//   - Superseded & age > ProtectTurns                     → evicted (stale duplicate).
//   - Superseded & age <= ProtectTurns                    → kept, but a budget
//     (a later read of the SAME slice, or a later            candidate (reclaimed
//     SUCCESSFUL write to the file)                          under memory pressure).
//   - Not superseded & age <= ProtectTurns                → never evicted.
//   - Not superseded & age > EvictTurns                   → evicted (any size).
//   - Budget valve: if reader-result bytes exceed         → evict superseded-first,
//     BudgetBytes, evict candidates                          then largest-first.
//
// Supersession respects the protect window: a read the model touched in the last
// ProtectTurns is part of the active working set and is kept even when superseded
// (so it can read a file once and make several edits), but it becomes a budget
// candidate so a suddenly-bloated context can still reclaim it. Two refinements
// keep supersession honest: it is range-aware (reading a different page of a large
// file does not evict the earlier page — see readerSliceKey), and only a
// *successful* write supersedes (a failed edit leaves the file unchanged, so it
// must not evict the read the model needs). Beyond the window, superseded reads
// and reads older than EvictTurns are dropped, and the budget valve sheds the
// rest — so nothing becomes immortal.
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
	Tool     string // originating tool, e.g. "file_read_bytes"
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
//	[Evicted 18432 bytes at 6 turns (large): file_read_bytes files/novels/ch17.md]
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
	"file_read_bytes": "path",
	"file_read_lines": "path",
	"file_list":       "path",
	"web_fetch":       "url",
}

// evictionReaderRangeArg maps a paginated reader tool to the argument that names
// the start of the slice it read. Reads of the SAME file at different starts are
// distinct pages, not duplicates, so read-vs-read supersession keys on
// path+start — otherwise reading page 2 of a large file would evict page 1.
var evictionReaderRangeArg = map[string]string{
	"file_read_lines": "start_line",
	"file_read_bytes": "offset",
}

// evictionReaderRangeDefault is the implied slice start when the range arg is
// omitted (file_read_lines defaults start_line=1; file_read_bytes offset=0).
var evictionReaderRangeDefault = map[string]int64{
	"file_read_lines": 1,
	"file_read_bytes": 0,
}

// evictionWriterArg maps a writer tool to the argument naming the resource it
// mutates. A successful write supersedes any earlier read of the same resource.
var evictionWriterArg = map[string]string{
	"file_write":        "path",
	"file_edit":         "path",
	"file_append":       "path",
	"file_copy":         "destination_path",
	"file_edit_lines":   "path",
	"file_edit_bytes":   "path",
	"file_insert_lines": "path",
	"file_insert_bytes": "path",
	"file_delete_lines": "path",
	"file_delete_bytes": "path",
	"file_delete":       "path",
	"file_move":         "source_path", // the moved-away file; reads of it are now stale
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

// readerSliceKey returns the read-vs-read supersession key: the resource path
// plus, for paginated readers, the slice start — so distinct pages of one file
// coexist and only a re-read of the SAME page supersedes. Non-paginated readers
// (file_list, web_fetch) key on the bare resource. Write-invalidation still keys
// on the bare path (any edit invalidates every cached page of that file).
func readerSliceKey(tool string, args map[string]any) (string, bool) {
	res, ok := readerResource(tool, args)
	if !ok {
		return "", false
	}
	rangeArg, paged := evictionReaderRangeArg[tool]
	if !paged {
		return res, true
	}
	start := evictionReaderRangeDefault[tool]
	if args != nil {
		if v, present := args[rangeArg]; present {
			start = toInt64(v)
		}
	}
	return fmt.Sprintf("%s#%d", res, start), true
}

// toInt64 coerces a JSON-decoded numeric arg (float64 by default) to int64.
func toInt64(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	case string:
		i, _ := strconv.ParseInt(strings.TrimSpace(n), 10, 64)
		return i
	}
	return 0
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
	toolErr := make([]bool, len(stored))
	for i := range stored {
		msgs[i] = stored[i].Message
		// A failed tool result is marked with Message.Type (set by the agent loop);
		// used below so a failed write does not supersede the read it needs.
		toolErr[i] = stored[i].Message.Type == providers.MessageTypeToolError
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
	maxReadIdx := map[string]int{}  // keyed by slice (path#start) for read-vs-read
	maxWriteIdx := map[string]int{} // keyed by path for write-invalidation
	errByCallID := make(map[string]bool)
	type pendingWrite struct {
		res    string
		idx    int
		callID string
	}
	var writes []pendingWrite
	for idx, mm := range msgs {
		for _, tc := range mm.ToolCalls {
			name, args := normToolCall(tc)
			callByID[tc.ID] = toolMeta{tool: name, args: args}
			if res, ok := writerResource(name, args); ok {
				// Defer recording the write until its result's error status is known.
				writes = append(writes, pendingWrite{res: res, idx: idx, callID: tc.ID})
			}
		}
		if mm.Role == "tool" && mm.ToolCallID != "" {
			errByCallID[mm.ToolCallID] = toolErr[idx]
			meta := callByID[mm.ToolCallID]
			if key, ok := readerSliceKey(meta.tool, meta.args); ok {
				if cur, k := maxReadIdx[key]; !k || idx > cur {
					maxReadIdx[key] = idx
				}
			}
		}
	}
	// A write supersedes earlier reads of the file only if it actually modified it.
	// A failed edit (e.g. "old_text not found") leaves the file unchanged, so it
	// must not evict the read the model needs to build a correct edit — that is
	// exactly the loop this guards against.
	for _, wr := range writes {
		if errByCallID[wr.callID] {
			continue // failed write: no supersession
		}
		if cur, k := maxWriteIdx[wr.res]; !k || wr.idx > cur {
			maxWriteIdx[wr.res] = wr.idx
		}
	}

	// Classify each reader tool-result message.
	type cand struct {
		idx, size  int
		superseded bool
	}
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
		sliceKey, _ := readerSliceKey(meta.tool, meta.args)
		size := len(mm.Content)
		if size == 0 {
			continue
		}

		// Superseded = a strictly-later read of the SAME slice (a genuine re-read of
		// that page), or a later SUCCESSFUL write to the file. Range-aware: reading
		// a different page of the same file is not a duplicate, and a failed write
		// does not count (see maxWriteIdx above).
		superseded := false
		if r, k := maxReadIdx[sliceKey]; k && r > idx {
			superseded = true
		}
		if w, k := maxWriteIdx[res]; k && w > idx {
			superseded = true
		}

		a := ages[idx]
		if superseded {
			// Past the working-set window a superseded read is a pure stale
			// duplicate — drop it now. Within the window keep it (the model may
			// still be editing against it), but demote it to a budget candidate so a
			// suddenly-bloated context can still reclaim it under memory pressure.
			if a > p.ProtectTurns {
				marks[idx] = "superseded"
				continue
			}
			budgetCands = append(budgetCands, cand{idx: idx, size: size, superseded: true})
			remainingReaderBytes += size
			continue
		}

		// Non-superseded reads respect the protect window (the recent working set).
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

	// Budget valve: when reader bytes exceed budget, evict superseded duplicates
	// first (least valuable), then largest-first, until back under budget.
	if budget := m.evictionBudgetBytes(); budget > 0 && remainingReaderBytes > budget {
		sort.Slice(budgetCands, func(i, j int) bool {
			if budgetCands[i].superseded != budgetCands[j].superseded {
				return budgetCands[i].superseded
			}
			return budgetCands[i].size > budgetCands[j].size
		})
		for _, c := range budgetCands {
			if remainingReaderBytes <= budget {
				break
			}
			if c.superseded {
				marks[c.idx] = "superseded"
			} else {
				marks[c.idx] = "budget"
			}
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
