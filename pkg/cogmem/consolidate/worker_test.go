// ClawEh - Cognitive Memory
// License: MIT

package consolidate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/cogmem/store"
)

// fakeSource is an in-memory MessageSource over a fixed slice (ascending seq).
type fakeSource struct {
	msgs []SourceMessage
}

func (f *fakeSource) Bounds() (int64, int64, error) {
	if len(f.msgs) == 0 {
		return 0, 0, nil
	}
	return f.msgs[0].Seq, f.msgs[len(f.msgs)-1].Seq, nil
}

func (f *fakeSource) Range(minSeq, maxSeq int64) ([]SourceMessage, error) {
	var out []SourceMessage
	for _, m := range f.msgs {
		if m.Seq >= minSeq && m.Seq <= maxSeq {
			out = append(out, m)
		}
	}
	return out, nil
}

// fakeModel returns a canned raw string regardless of input.
type fakeModel struct {
	raw   string
	model string
	err   error
}

func (m *fakeModel) Consolidate(ctx context.Context, system, userJSON string) (string, string, error) {
	name := m.model
	if name == "" {
		name = "fake-model"
	}
	return m.raw, name, m.err
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.cogmem.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func sampleMessages() []SourceMessage {
	return []SourceMessage{
		{Seq: 1, Role: "user", Text: "Please always run gofmt before committing."},
		{Seq: 2, Role: "assistant", Text: "Understood, I'll run gofmt first."},
		{Seq: 3, Role: "tool", Text: "ignored plumbing"},
	}
}

func params() RunParams {
	return RunParams{
		AgentID:     "alice",
		SessionKey:  "agent:alice:main",
		Workspace:   "/nonexistent-workspace",
		ArchivePath: "/sessions/agent_alice_main.archive.db",
		Trigger:     "message",
	}
}

// seedDomain creates a project domain with one active rule hook and returns the
// domain id, the hook id, and the highest hook id we may supersede.
func seedDomain(t *testing.T, s *store.Store) (string, string) {
	t.Helper()
	ctx := context.Background()
	d, err := s.CreateDomain(ctx, s.DB(), store.CreateDomainParams{
		AgentID: "alice", SessionKey: "agent:alice:main",
		Name: "ClawEh", Status: store.StatusActive,
		Summary: "Go gateway project",
	})
	if err != nil {
		t.Fatalf("seed domain: %v", err)
	}
	h, err := s.AddMemory(ctx, s.DB(), store.AddMemoryParams{
		DomainID: d.ID, Type: store.TypeRule, Text: "Run make test after changes.",
		Status: store.StatusActive, Confidence: 0.9, Source: store.SourceUserExplicit,
	})
	if err != nil {
		t.Fatalf("seed hook: %v", err)
	}
	return d.ID, h.ID
}

func TestRunOnceHappyPath(t *testing.T) {
	s := openStore(t)
	domainID, memoryID := seedDomain(t, s)
	src := &fakeSource{msgs: sampleMessages()}

	// A valid supersede: replace the existing rule with a new one, evidence in batch.
	raw := fmt.Sprintf(`{
		"domain_ops": [],
		"memory_ops": [{
			"op": "supersede",
			"domain": %q,
			"old_id": %q,
			"type": "rule",
			"text": "Always run gofmt and make test before committing.",
			"status": "active",
			"source": "user_explicit",
			"confidence": 0.95,
			"evidence": {"seq_start": 1, "seq_end": 2}
		}],
		"conflict_ledger": []
	}`, domainID, memoryID)

	w := NewWorker(s, src, &fakeModel{raw: raw}, WithModelName("test-model"))
	res, err := w.RunOnce(context.Background(), params())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("status = %q, want ok", res.Status)
	}
	if res.Applied < 1 {
		t.Fatalf("applied = %d, want >=1", res.Applied)
	}

	ctx := context.Background()
	st, _ := s.GetState(ctx, s.DB(), params().ArchivePath)
	if st.ConsolidatedSeq != 2 {
		t.Fatalf("consolidated_seq = %d, want 2 (lastSeq)", st.ConsolidatedSeq)
	}
	if st.LastSeenSeq != 3 {
		t.Fatalf("last_seen_seq = %d, want 3 (maxSeq)", st.LastSeenSeq)
	}

	run, ok, err := s.LastRun(ctx, s.DB())
	if err != nil || !ok {
		t.Fatalf("last run: ok=%v err=%v", ok, err)
	}
	if run.Status != "ok" || run.OpsApplied < 1 {
		t.Fatalf("run = %+v, want status ok applied>=1", run)
	}

	// Old hook retired, new one active.
	active, _ := s.ListMemories(ctx, s.DB(), domainID, store.StatusActive)
	if len(active) != 1 || active[0].Text != "Always run gofmt and make test before committing." {
		t.Fatalf("active hooks = %+v", active)
	}
}

func TestRunOnceMarkConsolidatedOnSuccess(t *testing.T) {
	s := openStore(t)
	domainID, memoryID := seedDomain(t, s)
	src := &fakeSource{msgs: sampleMessages()}

	raw := fmt.Sprintf(`{
		"domain_ops": [],
		"memory_ops": [{
			"op": "supersede",
			"domain": %q,
			"old_id": %q,
			"type": "rule",
			"text": "Always run gofmt and make test before committing.",
			"status": "active",
			"source": "user_explicit",
			"confidence": 0.95,
			"evidence": {"seq_start": 1, "seq_end": 2}
		}],
		"conflict_ledger": []
	}`, domainID, memoryID)

	var (
		calls  int
		gotSeq int64
	)
	mark := func(uptoSeq int64) error {
		calls++
		gotSeq = uptoSeq
		return nil
	}

	w := NewWorker(s, src, &fakeModel{raw: raw}, WithModelName("test-model"), WithMarkConsolidated(mark))
	res, err := w.RunOnce(context.Background(), params())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("status = %q, want ok", res.Status)
	}
	if calls != 1 {
		t.Fatalf("mark calls = %d, want 1", calls)
	}
	// lastSeq is the highest meaningful seq consolidated (seq 2; seq 3 is a tool
	// plumbing message dropped by MeaningfulRole).
	if gotSeq != 2 {
		t.Fatalf("mark seq = %d, want 2 (lastSeq)", gotSeq)
	}
}

func TestRunOnceMarkConsolidatedErrorDoesNotRollBack(t *testing.T) {
	s := openStore(t)
	domainID, memoryID := seedDomain(t, s)
	src := &fakeSource{msgs: sampleMessages()}

	raw := fmt.Sprintf(`{"domain_ops":[],"memory_ops":[{"op":"supersede","domain":%q,"old_id":%q,"type":"rule","text":"Run gofmt and tests.","status":"active","source":"user_explicit","evidence":{"seq_start":1,"seq_end":2}}],"conflict_ledger":[]}`, domainID, memoryID)

	mark := func(uptoSeq int64) error { return fmt.Errorf("archive open boom") }

	w := NewWorker(s, src, &fakeModel{raw: raw}, WithModelName("test-model"), WithMarkConsolidated(mark))
	res, err := w.RunOnce(context.Background(), params())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	// A mark failure must not change the run status nor the watermark.
	if res.Status != "ok" {
		t.Fatalf("status = %q, want ok", res.Status)
	}
	ctx := context.Background()
	st, _ := s.GetState(ctx, s.DB(), params().ArchivePath)
	if st.ConsolidatedSeq != 2 {
		t.Fatalf("consolidated_seq = %d, want 2 (watermark intact)", st.ConsolidatedSeq)
	}
	// The error is surfaced in the run record.
	run, ok, err := s.LastRun(ctx, s.DB())
	if err != nil || !ok {
		t.Fatalf("last run: ok=%v err=%v", ok, err)
	}
	if run.Error == "" {
		t.Fatalf("run.Error = %q, want non-empty mark error", run.Error)
	}
}

func TestRunOnceMarkConsolidatedNotCalledOnInvalidJSON(t *testing.T) {
	s := openStore(t)
	seedDomain(t, s)
	src := &fakeSource{msgs: sampleMessages()}

	calls := 0
	mark := func(uptoSeq int64) error { calls++; return nil }

	w := NewWorker(s, src, &fakeModel{raw: "not json at all"}, WithMarkConsolidated(mark))
	res, err := w.RunOnce(context.Background(), params())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.Status != "invalid_json" {
		t.Fatalf("status = %q, want invalid_json", res.Status)
	}
	if calls != 0 {
		t.Fatalf("mark calls = %d, want 0 on invalid_json", calls)
	}
}

func TestRunOnceMarkConsolidatedNotCalledOnAborted(t *testing.T) {
	s := openStore(t)
	domainID, memoryID := seedDomain(t, s)
	src := &fakeSource{msgs: sampleMessages()}

	// Evidence seq_end 99 is outside the batch [1,2] → Validate fails → aborted.
	raw := fmt.Sprintf(`{"domain_ops":[],"memory_ops":[{"op":"supersede","domain":%q,"old_id":%q,"type":"rule","text":"Out of range.","status":"active","source":"user_explicit","evidence":{"seq_start":1,"seq_end":99}}],"conflict_ledger":[]}`, domainID, memoryID)

	calls := 0
	mark := func(uptoSeq int64) error { calls++; return nil }

	w := NewWorker(s, src, &fakeModel{raw: raw}, WithMarkConsolidated(mark))
	res, err := w.RunOnce(context.Background(), params())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.Status != "aborted" {
		t.Fatalf("status = %q, want aborted", res.Status)
	}
	if calls != 0 {
		t.Fatalf("mark calls = %d, want 0 on aborted", calls)
	}
}

func TestRunOnceInvalidJSON(t *testing.T) {
	s := openStore(t)
	seedDomain(t, s)
	src := &fakeSource{msgs: sampleMessages()}

	w := NewWorker(s, src, &fakeModel{raw: "not json at all"}, WithModelName("test-model"))
	res, err := w.RunOnce(context.Background(), params())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.Status != "invalid_json" {
		t.Fatalf("status = %q, want invalid_json", res.Status)
	}
	st, _ := s.GetState(context.Background(), s.DB(), params().ArchivePath)
	if st.ConsolidatedSeq != 0 {
		t.Fatalf("watermark advanced to %d on invalid json, want 0", st.ConsolidatedSeq)
	}
}

func TestRunOnceValidationAborted(t *testing.T) {
	s := openStore(t)
	domainID, memoryID := seedDomain(t, s)
	src := &fakeSource{msgs: sampleMessages()}

	// Evidence seq_end 99 is outside the batch [1,2] → Validate fails.
	raw := fmt.Sprintf(`{
		"domain_ops": [],
		"memory_ops": [{
			"op": "supersede",
			"domain": %q,
			"old_id": %q,
			"type": "rule",
			"text": "Out of range evidence.",
			"status": "active",
			"source": "user_explicit",
			"evidence": {"seq_start": 1, "seq_end": 99}
		}],
		"conflict_ledger": []
	}`, domainID, memoryID)

	w := NewWorker(s, src, &fakeModel{raw: raw}, WithModelName("test-model"))
	res, err := w.RunOnce(context.Background(), params())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.Status != "aborted" {
		t.Fatalf("status = %q, want aborted", res.Status)
	}
	st, _ := s.GetState(context.Background(), s.DB(), params().ArchivePath)
	if st.ConsolidatedSeq != 0 {
		t.Fatalf("watermark advanced to %d on aborted, want 0", st.ConsolidatedSeq)
	}
}

func TestRunOnceIdleNoMessages(t *testing.T) {
	s := openStore(t)
	src := &fakeSource{} // empty archive
	w := NewWorker(s, src, &fakeModel{raw: "{}"})
	res, err := w.RunOnce(context.Background(), params())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.Status != "idle" {
		t.Fatalf("status = %q, want idle", res.Status)
	}
}

func TestRunOnceIdleAlreadyConsolidated(t *testing.T) {
	s := openStore(t)
	src := &fakeSource{msgs: sampleMessages()}
	// Watermark already past max seq.
	if err := s.SetWatermark(context.Background(), s.DB(), params().ArchivePath, 3, 3); err != nil {
		t.Fatalf("watermark: %v", err)
	}
	w := NewWorker(s, src, &fakeModel{raw: "{}"})
	res, err := w.RunOnce(context.Background(), params())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.Status != "idle" {
		t.Fatalf("status = %q, want idle", res.Status)
	}
}

func TestRunOnceBusyWhenLeased(t *testing.T) {
	s := openStore(t)
	src := &fakeSource{msgs: sampleMessages()}
	// Hold the lease as someone else.
	ok, err := s.AcquireLease(context.Background(), s.DB(), "consolidate:"+params().ArchivePath, "other", leaseTTL)
	if err != nil || !ok {
		t.Fatalf("pre-acquire lease: ok=%v err=%v", ok, err)
	}
	w := NewWorker(s, src, &fakeModel{raw: "{}"})
	res, err := w.RunOnce(context.Background(), params())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.Status != "busy" {
		t.Fatalf("status = %q, want busy", res.Status)
	}
}

func TestRunOnceDebugDump(t *testing.T) {
	s := openStore(t)
	domainID, memoryID := seedDomain(t, s)
	src := &fakeSource{msgs: sampleMessages()}
	dir := t.TempDir()

	raw := fmt.Sprintf(`{"domain_ops":[],"memory_ops":[{"op":"supersede","domain":%q,"old_id":%q,"type":"rule","text":"Run gofmt and tests.","status":"active","source":"user_explicit","evidence":{"seq_start":1,"seq_end":2}}],"conflict_ledger":[]}`, domainID, memoryID)

	w := NewWorker(s, src, &fakeModel{raw: raw}, WithDebugDump(dir))
	if _, err := w.RunOnce(context.Background(), params()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("debug dump files = %d, want 1", len(entries))
	}
}
