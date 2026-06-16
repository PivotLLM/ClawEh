// ClawEh - Cognitive Memory
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package cogmem

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/PivotLLM/ClawEh/pkg/cogmem/store"
	"github.com/PivotLLM/ClawEh/pkg/global"
)

// handlerFunc is the inner handler shape: it receives the opened store plus the
// call, and returns the model-facing text or an error. wrap() handles store
// lifecycle and turns errors into error Results.
type handlerFunc func(s *store.Store, call *global.ToolCall) (string, error)

// wrap builds a global.ToolHandler that resolves the per-session database from
// the workspace + ToolCall.Session, opens it (ensuring the parent dir exists),
// runs h, and closes the store. A missing session yields an error Result.
func wrap(workspace string, h handlerFunc) global.ToolHandler {
	return func(call *global.ToolCall) (*global.Result, error) {
		if call.Session == "" {
			return errResult("cognitive memory requires an active session", nil), nil
		}
		if workspace == "" {
			return errResult("cognitive memory is unavailable (no workspace configured)", nil), nil
		}
		dbPath := store.SessionDBPath(workspace, call.Session)
		if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
			return errResult("failed to prepare memory store directory", err), nil
		}
		s, err := store.Open(dbPath)
		if err != nil {
			return errResult("failed to open memory store", err), nil
		}
		defer func() { _ = s.Close() }()

		text, err := h(s, call)
		if err != nil {
			return errResult(err.Error(), err), nil
		}
		return &global.Result{ForLLM: text}, nil
	}
}

func errResult(msg string, err error) *global.Result {
	return &global.Result{IsError: true, ForLLM: msg, Err: err}
}

// --- argument helpers ---

func argStr(call *global.ToolCall, key string) string {
	if v, ok := call.Args[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func argInt(call *global.ToolCall, key string, def int) int {
	switch v := call.Args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	}
	return def
}

func argFloat(call *global.ToolCall, key string, def float64) float64 {
	switch v := call.Args[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	}
	return def
}

func argStrSlice(call *global.ToolCall, key string) ([]string, bool) {
	raw, ok := call.Args[key]
	if !ok {
		return nil, false
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out, true
}

// --- handlers ---

func getDomain(s *store.Store, call *global.ToolCall) (string, error) {
	id := argStr(call, "id")
	if id == "" {
		return "", errors.New("id is required")
	}
	d, err := s.GetDomain(call.Ctx, s.DB(), id, true)
	if err != nil {
		return "", mapErr(err, id)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Domain %s [%s] %q (status=%s, version=%d)\n", d.ID, d.Type, d.Name, d.Status, d.Version)
	if d.Summary != "" {
		fmt.Fprintf(&b, "Summary: %s\n", d.Summary)
	}
	if d.Triggers != "" {
		fmt.Fprintf(&b, "Triggers (auto-load on tool use): %s\n", d.Triggers)
	}
	writeStateLine(&b, "Blockers", d.State.Blockers)
	writeStateLine(&b, "Next actions", d.State.NextActions)
	writeStateLine(&b, "Constraints", d.State.Constraints)
	if len(d.Memories) == 0 {
		b.WriteString("Memories: (none active)\n")
	} else {
		fmt.Fprintf(&b, "Memories (%d active):\n", len(d.Memories))
		for _, h := range d.Memories {
			fmt.Fprintf(&b, "  %s [%s] (conf=%.2f) %s\n", h.ID, h.Type, h.Confidence, h.Text)
		}
	}
	return b.String(), nil
}

func search(s *store.Store, call *global.ToolCall) (string, error) {
	query := argStr(call, "query")
	if query == "" {
		return "", errors.New("query is required")
	}
	limit := argInt(call, "limit", 20)
	hooks, err := s.SearchMemories(call.Ctx, s.DB(), query, limit)
	if err != nil {
		return "", err
	}
	if len(hooks) == 0 {
		return fmt.Sprintf("No active memories match %q.", query), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d active memories matching %q:\n", len(hooks), query)
	for _, h := range hooks {
		fmt.Fprintf(&b, "  %s [%s] (domain=%s, conf=%.2f) %s\n", h.ID, h.Type, h.DomainID, h.Confidence, h.Text)
	}
	return b.String(), nil
}

func listDomains(s *store.Store, call *global.ToolCall) (string, error) {
	var statuses []store.Status
	if st := argStr(call, "status"); st != "" {
		statuses = append(statuses, store.Status(st))
	}
	domains, err := s.ListDomains(call.Ctx, s.DB(), statuses...)
	if err != nil {
		return "", err
	}
	typeFilter := argStr(call, "type")
	var out []store.Domain
	for _, d := range domains {
		if typeFilter != "" && string(d.Type) != typeFilter {
			continue
		}
		out = append(out, d)
	}
	if len(out) == 0 {
		return "No domains.", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d domain(s):\n", len(out))
	for _, d := range out {
		summary := d.Summary
		if summary == "" {
			summary = "(no summary)"
		}
		fmt.Fprintf(&b, "  %s · %s · %s · %s · %s\n", d.ID, d.Type, d.Name, summary, d.Status)
	}
	return b.String(), nil
}

func explain(s *store.Store, call *global.ToolCall) (string, error) {
	id := argStr(call, "id")
	if id == "" {
		return "", errors.New("id is required")
	}
	// Memory ids carry the "h" prefix; domain ids "d". Try the matching lookup
	// first, then fall back to the other so a mis-typed prefix still resolves.
	if strings.HasPrefix(id, "h") {
		if h, err := s.GetMemory(call.Ctx, s.DB(), id); err == nil {
			return explainMemory(h), nil
		}
	}
	if d, err := s.GetDomain(call.Ctx, s.DB(), id, false); err == nil {
		return explainDomain(d), nil
	}
	if h, err := s.GetMemory(call.Ctx, s.DB(), id); err == nil {
		return explainMemory(h), nil
	}
	return "", mapErr(store.ErrNotFound, id)
}

func explainDomain(d store.Domain) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Domain %s %q\n", d.ID, d.Name)
	fmt.Fprintf(&b, "  type=%s status=%s version=%d\n", d.Type, d.Status, d.Version)
	fmt.Fprintf(&b, "  created=%s updated=%s\n", d.CreatedAt.Format("2006-01-02"), d.UpdatedAt.Format("2006-01-02"))
	if d.Summary != "" {
		fmt.Fprintf(&b, "  summary: %s\n", d.Summary)
	}
	return b.String()
}

func explainMemory(h store.Memory) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Memory %s (domain %s)\n", h.ID, h.DomainID)
	fmt.Fprintf(&b, "  type=%s status=%s confidence=%.2f source=%s\n", h.Type, h.Status, h.Confidence, h.Source)
	fmt.Fprintf(&b, "  text: %s\n", h.Text)
	if h.SourceSeqStart != nil && h.SourceSeqEnd != nil {
		fmt.Fprintf(&b, "  evidence: seq %d..%d\n", *h.SourceSeqStart, *h.SourceSeqEnd)
	}
	if h.SupersedesMemoryID != nil {
		fmt.Fprintf(&b, "  supersedes: %s\n", *h.SupersedesMemoryID)
	}
	if h.RetireReason != nil && *h.RetireReason != "" {
		fmt.Fprintf(&b, "  retire reason: %s\n", *h.RetireReason)
	}
	return b.String()
}

func remember(s *store.Store, call *global.ToolCall) (string, error) {
	mtype := argStr(call, "type")
	text := argStr(call, "text")
	if mtype == "" || text == "" {
		return "", errors.New("type and text are required")
	}

	domainID := argStr(call, "domain_id")
	if domainID == "" {
		hint := argStr(call, "domain_hint")
		if hint == "" {
			// No domain specified → the always-on general domain (seeded on open).
			g, err := s.GeneralDomain(call.Ctx, s.DB())
			if err != nil {
				return "", err
			}
			domainID = g.ID
		} else {
			d, err := s.CreateDomain(call.Ctx, s.DB(), store.CreateDomainParams{
				AgentID:    call.AgentID,
				SessionKey: call.Session,
				Type:       store.DomainProject,
				Name:       hint,
				Status:     store.StatusActive,
			})
			if err != nil {
				return "", err
			}
			domainID = d.ID
		}
	}

	status := store.Status(argStr(call, "status"))
	if status == "" {
		status = store.StatusActive
	}
	h, err := s.AddMemory(call.Ctx, s.DB(), store.AddMemoryParams{
		DomainID:   domainID,
		Type:       store.MemoryType(mtype),
		Text:       text,
		Status:     status,
		Confidence: argFloat(call, "confidence", 0.9),
		Source:     store.SourceToolWrite,
	})
	if err != nil {
		return "", mapErr(err, domainID)
	}
	return fmt.Sprintf("Stored memory %s in domain %s (type=%s, status=%s).", h.ID, domainID, h.Type, h.Status), nil
}

func updateDomain(s *store.Store, call *global.ToolCall) (string, error) {
	id := argStr(call, "id")
	if id == "" {
		return "", errors.New("id is required")
	}
	if _, ok := call.Args["expected_version"]; !ok {
		return "", errors.New("expected_version is required")
	}
	expected := int64(argInt(call, "expected_version", 0))

	cur, err := s.GetDomain(call.Ctx, s.DB(), id, false)
	if err != nil {
		return "", mapErr(err, id)
	}
	p := store.UpdateDomainParams{ExpectedVersion: expected}
	if v := argStr(call, "set_summary"); v != "" {
		p.Summary = &v
	}
	state := cur.State
	changedState := false
	if v, ok := argStrSlice(call, "set_blockers"); ok {
		state.Blockers = v
		changedState = true
	}
	if v, ok := argStrSlice(call, "set_next_actions"); ok {
		state.NextActions = v
		changedState = true
	}
	if v, ok := argStrSlice(call, "set_constraints"); ok {
		state.Constraints = v
		changedState = true
	}
	if changedState {
		p.State = &state
	}
	// set_triggers present (even empty) replaces the trigger list; empty clears it.
	if _, ok := call.Args["set_triggers"]; ok {
		v := argStr(call, "set_triggers")
		p.Triggers = &v
	}
	if p.Summary == nil && p.State == nil && p.Triggers == nil {
		return "", errors.New("nothing to update (set_summary, set_triggers, or a list field required)")
	}
	if err := s.UpdateDomain(call.Ctx, s.DB(), id, p); err != nil {
		if errors.Is(err, store.ErrVersionConflict) {
			return "", fmt.Errorf("version conflict: domain %s is at version %d, not %d — reload and retry", id, cur.Version, expected)
		}
		return "", mapErr(err, id)
	}
	return fmt.Sprintf("Updated domain %s (now version %d).", id, cur.Version+1), nil
}

// exportMemory writes the agent's entire active memory as one Markdown document
// to files/MEMORY_EXPORT.md (its writable area) and reports the path and counts.
func exportMemory(s *store.Store, call *global.ToolCall) (string, error) {
	doc, nDomains, nMemories, err := renderFullExport(call.Ctx, s)
	if err != nil {
		return "", err
	}
	// The store lives at <workspace>/sessions/<key>.cogmem.db; recover <workspace>
	// to write the export into the agent's read/write files/ directory.
	workspace := filepath.Dir(filepath.Dir(s.Path()))
	outDir := filepath.Join(workspace, "files")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", fmt.Errorf("failed to prepare export directory: %w", err)
	}
	outPath := filepath.Join(outDir, "MEMORY_EXPORT.md")
	if err := os.WriteFile(outPath, []byte(doc), 0o644); err != nil {
		return "", fmt.Errorf("failed to write export: %w", err)
	}
	return fmt.Sprintf("Exported %d domain(s) and %d memory(ies) to files/MEMORY_EXPORT.md.", nDomains, nMemories), nil
}

func retireHook(s *store.Store, call *global.ToolCall) (string, error) {
	id := argStr(call, "id")
	reason := argStr(call, "reason")
	if id == "" || reason == "" {
		return "", errors.New("id and reason are required")
	}
	if err := s.RetireMemory(call.Ctx, s.DB(), id, reason); err != nil {
		return "", mapErr(err, id)
	}
	return fmt.Sprintf("Retired memory %s.", id), nil
}

func createDomain(s *store.Store, call *global.ToolCall) (string, error) {
	typ := argStr(call, "type")
	name := argStr(call, "name")
	if typ == "" || name == "" {
		return "", errors.New("type and name are required")
	}
	d, err := s.CreateDomain(call.Ctx, s.DB(), store.CreateDomainParams{
		AgentID:    call.AgentID,
		SessionKey: call.Session,
		Type:       store.DomainType(typ),
		Name:       name,
		Status:     store.StatusActive,
		Summary:    argStr(call, "summary"),
		Triggers:   argStr(call, "triggers"),
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Created domain %s (type=%s, name=%q).", d.ID, d.Type, d.Name), nil
}

func archiveDomain(s *store.Store, call *global.ToolCall) (string, error) {
	id := argStr(call, "id")
	if id == "" {
		return "", errors.New("id is required")
	}
	if err := s.ArchiveDomain(call.Ctx, s.DB(), id); err != nil {
		return "", mapErr(err, id)
	}
	return fmt.Sprintf("Archived domain %s.", id), nil
}

func forget(s *store.Store, call *global.ToolCall) (string, error) {
	query := argStr(call, "query")
	if query == "" {
		return "", errors.New("query is required")
	}
	domainFilter := argStr(call, "domain_id")
	hooks, err := s.SearchMemories(call.Ctx, s.DB(), query, 100)
	if err != nil {
		return "", err
	}
	var retired []string
	for _, h := range hooks {
		if domainFilter != "" && h.DomainID != domainFilter {
			continue
		}
		if err := s.RetireMemory(call.Ctx, s.DB(), h.ID, "forget: "+query); err != nil {
			return "", err
		}
		retired = append(retired, h.ID)
	}
	if len(retired) == 0 {
		return fmt.Sprintf("No active memories matched %q; nothing retired.", query), nil
	}
	sort.Strings(retired)
	return fmt.Sprintf("Retired %d memories: %s.", len(retired), strings.Join(retired, ", ")), nil
}

func consolidate(_ *store.Store, call *global.ToolCall) (string, error) {
	if consolidateTrigger != nil {
		consolidateTrigger(call.AgentID, call.Session)
		return "Consolidation requested.", nil
	}
	return "Consolidation queued (worker not yet running).", nil
}

func status(s *store.Store, call *global.ToolCall) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "Cognitive memory database: %s (healthy)\n", s.Path())

	pending, err := s.PendingCount(call.Ctx, s.DB())
	if err != nil {
		return "", err
	}
	fmt.Fprintf(&b, "Pending (review) memories: %d\n", pending)

	run, ok, err := s.LastRun(call.Ctx, s.DB())
	if err != nil {
		return "", err
	}
	if !ok {
		b.WriteString("Last consolidation run: none\n")
	} else {
		fmt.Fprintf(&b, "Last consolidation run: %s (trigger=%s, status=%s, ops=%d) at %s\n",
			run.ID, run.Trigger, run.Status, run.OpsApplied, run.StartedAt.Format("2006-01-02 15:04:05"))
	}
	if consolidateTrigger == nil {
		b.WriteString("Consolidation worker: not running\n")
	} else {
		b.WriteString("Consolidation worker: active\n")
	}
	return b.String(), nil
}

// --- small helpers ---

func writeStateLine(b *strings.Builder, label string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(b, "%s: %s\n", label, strings.Join(items, "; "))
}

// mapErr turns store sentinel errors into user-facing messages naming the id.
func mapErr(err error, id string) error {
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("%s not found", id)
	}
	return err
}
