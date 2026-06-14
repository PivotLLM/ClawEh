// ClawEh - Cognitive Memory
// License: MIT

package consolidate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/cogmem/store"
	"github.com/google/uuid"
)

// leaseTTL bounds how long a single RunOnce may hold the per-archive lease.
const leaseTTL = 10 * time.Minute

// SourceMessage is one message pulled from the session archive.
type SourceMessage struct {
	Seq  int64
	Role string
	Text string
}

// MessageSource is the read side of the session archive. It is deliberately
// decoupled from pkg/memory so the worker carries no dependency on the archive
// implementation; the gateway adapts its archive to this interface.
type MessageSource interface {
	// Bounds returns the lowest and highest seq present in the archive.
	Bounds() (minSeq, maxSeq int64, err error)
	// Range returns messages with seq in [minSeq, maxSeq] in ascending order.
	Range(minSeq, maxSeq int64) ([]SourceMessage, error)
}

// ModelCaller invokes the configured memory model with a system prompt and a
// JSON user message, returning the raw model text (expected to be JSON). It is
// decoupled from pkg/providers so the worker never imports a provider package.
type ModelCaller interface {
	Consolidate(ctx context.Context, systemPrompt, userJSON string) (raw string, err error)
}

// Worker runs the consolidation "sleep cycle" against one store + archive pair.
type Worker struct {
	st    *store.Store
	src   MessageSource
	model ModelCaller

	batchOpts      BatchOptions
	proposeDomains bool
	autoPromote    bool
	debugDump      string
	modelName      string
}

// Option configures a Worker (functional-options pattern, per dev standards).
type Option func(*Worker)

// WithBatchOptions sets the batching levers (count/token/char budgets).
func WithBatchOptions(o BatchOptions) Option { return func(w *Worker) { w.batchOpts = o } }

// WithProposeDomains lets the model create new domains (informational lever).
func WithProposeDomains(v bool) Option { return func(w *Worker) { w.proposeDomains = v } }

// WithAutoPromote is currently informational: inferred items already land in
// review via the contract, so promotion stays a human/MCP action.
func WithAutoPromote(v bool) Option { return func(w *Worker) { w.autoPromote = v } }

// WithDebugDump writes the system/user/raw payloads of each run to dir.
func WithDebugDump(dir string) Option { return func(w *Worker) { w.debugDump = dir } }

// WithModelName records a human-readable model name in consolidation runs.
func WithModelName(name string) Option { return func(w *Worker) { w.modelName = name } }

// NewWorker builds a Worker over a store, an archive source, and a model caller.
func NewWorker(st *store.Store, src MessageSource, model ModelCaller, opts ...Option) *Worker {
	w := &Worker{
		st:        st,
		src:       src,
		model:     model,
		batchOpts: DefaultBatchOptions(),
	}
	for _, o := range opts {
		o(w)
	}
	return w
}

// RunParams identifies one consolidation run.
type RunParams struct {
	AgentID     string
	SessionKey  string
	Workspace   string
	ArchivePath string
	Trigger     string // message, idle, nightly, manual
}

// RunResult reports the outcome of a single RunOnce.
type RunResult struct {
	Applied  int
	More     bool
	Status   string // busy, idle, ok, error, invalid_json, aborted
	SeqStart int64
	SeqEnd   int64
}

// RunOnce performs one consolidation pass: it leases the archive, selects the
// next batch of un-consolidated messages, asks the model to propose memory
// operations, validates them against the contract, and applies the valid result
// in one transaction. The watermark advances only on a successful apply.
func (w *Worker) RunOnce(ctx context.Context, p RunParams) (RunResult, error) {
	leaseName := "consolidate:" + p.ArchivePath
	owner := w.leaseOwner(p.AgentID)
	ok, err := w.st.AcquireLease(ctx, w.st.DB(), leaseName, owner, leaseTTL)
	if err != nil {
		return RunResult{}, fmt.Errorf("consolidate: acquire lease: %w", err)
	}
	if !ok {
		return RunResult{Status: "busy"}, nil
	}
	defer func() { _ = w.st.ReleaseLease(ctx, w.st.DB(), leaseName, owner) }()

	state, err := w.st.GetState(ctx, w.st.DB(), p.ArchivePath)
	if err != nil {
		return RunResult{}, fmt.Errorf("consolidate: get state: %w", err)
	}
	consolidated := state.ConsolidatedSeq

	_, maxSeq, err := w.src.Bounds()
	if err != nil {
		return RunResult{}, fmt.Errorf("consolidate: archive bounds: %w", err)
	}
	if maxSeq <= consolidated {
		return RunResult{Status: "idle", SeqStart: consolidated + 1}, nil
	}

	src, err := w.src.Range(consolidated+1, maxSeq)
	if err != nil {
		return RunResult{}, fmt.Errorf("consolidate: archive range: %w", err)
	}
	msgs := make([]Message, 0, len(src))
	for _, m := range src {
		if !MeaningfulRole(m.Role) {
			continue
		}
		msgs = append(msgs, Message{Seq: m.Seq, Role: m.Role, Text: m.Text})
	}

	batch, lastSeq, more := SelectBatch(msgs, w.batchOpts)
	if len(batch) == 0 {
		return RunResult{Status: "idle", SeqStart: consolidated + 1}, nil
	}

	in := Input{
		Curated:      ReadCurated(p.Workspace),
		CurrentState: w.currentState(ctx),
		NewMessages:  batch,
	}

	system, _ := LoadPrompt(PromptPath(p.Workspace))
	userJSON, err := json.Marshal(in)
	if err != nil {
		return RunResult{}, fmt.Errorf("consolidate: marshal input: %w", err)
	}

	result := RunResult{More: more, SeqStart: consolidated + 1, SeqEnd: lastSeq}
	started := time.Now()
	inputTokens := EstimateTokens(system + string(userJSON))

	raw, err := w.model.Consolidate(ctx, system, string(userJSON))
	if err != nil {
		w.recordRun(ctx, p, "error", 0, consolidated+1, lastSeq, inputTokens, 0, err.Error(), started)
		result.Status = "error"
		return result, fmt.Errorf("consolidate: model call: %w", err)
	}
	outputTokens := EstimateTokens(raw)

	out, perr := parseOutput(raw)
	if perr != nil {
		w.recordRun(ctx, p, "invalid_json", 0, consolidated+1, lastSeq, inputTokens, outputTokens, perr.Error(), started)
		w.dump(p, system, string(userJSON), raw, 0)
		result.Status = "invalid_json"
		return result, nil
	}

	if verr := out.Validate(in); verr != nil {
		w.recordRun(ctx, p, "aborted", 0, consolidated+1, lastSeq, inputTokens, outputTokens, verr.Error(), started)
		w.dump(p, system, string(userJSON), raw, 0)
		result.Status = "aborted"
		return result, nil
	}

	applied, err := Apply(ctx, w.st, out, ApplyContext{
		AgentID:    p.AgentID,
		SessionKey: p.SessionKey,
		Actor:      actorSleepCycle,
		Model:      w.modelName,
	})
	if err != nil {
		w.recordRun(ctx, p, "error", applied, consolidated+1, lastSeq, inputTokens, outputTokens, err.Error(), started)
		result.Status = "error"
		return result, fmt.Errorf("consolidate: apply: %w", err)
	}

	if err := w.st.SetWatermark(ctx, w.st.DB(), p.ArchivePath, lastSeq, maxSeq); err != nil {
		return result, fmt.Errorf("consolidate: set watermark: %w", err)
	}
	w.recordRun(ctx, p, "ok", applied, consolidated+1, lastSeq, inputTokens, outputTokens, "", started)
	w.dump(p, system, string(userJSON), raw, applied)

	result.Applied = applied
	result.Status = "ok"
	return result, nil
}

// currentState projects the active+review domains and their active hooks into
// the compact view the model sees.
func (w *Worker) currentState(ctx context.Context) CurrentState {
	cs := CurrentState{}
	domains, err := w.st.ListDomains(ctx, w.st.DB(), store.StatusActive, store.StatusReview)
	if err != nil {
		return cs
	}
	for _, d := range domains {
		dv := DomainView{
			ID:      d.ID,
			Type:    string(d.Type),
			Name:    d.Name,
			Status:  string(d.Status),
			Version: d.Version,
			Summary: d.Summary,
			State:   d.State,
		}
		hooks, err := w.st.ListHooks(ctx, w.st.DB(), d.ID, store.StatusActive)
		if err == nil {
			for _, h := range hooks {
				dv.Hooks = append(dv.Hooks, HookView{
					ID:         h.ID,
					Kind:       string(h.Kind),
					Text:       h.Text,
					Confidence: h.Confidence,
				})
			}
		}
		cs.Domains = append(cs.Domains, dv)
	}
	return cs
}

func (w *Worker) recordRun(ctx context.Context, p RunParams, status string, applied int, seqStart, seqEnd int64, inTok, outTok int, errMsg string, started time.Time) {
	finished := time.Now()
	_ = w.st.RecordRun(ctx, w.st.DB(), store.Run{
		Trigger:      p.Trigger,
		Model:        w.modelName,
		SeqStart:     seqStart,
		SeqEnd:       seqEnd,
		InputTokens:  inTok,
		OutputTokens: outTok,
		Status:       status,
		OpsApplied:   applied,
		Error:        errMsg,
		StartedAt:    started,
		FinishedAt:   &finished,
	})
}

// dump writes the run payloads for offline model comparison when a debug dir is
// configured. Best-effort: failures are ignored.
func (w *Worker) dump(p RunParams, system, userJSON, raw string, applied int) {
	if w.debugDump == "" {
		return
	}
	if err := os.MkdirAll(w.debugDump, 0o755); err != nil {
		return
	}
	rec := struct {
		System   string `json:"system"`
		UserJSON string `json:"user_json"`
		Raw      string `json:"raw"`
		Applied  int    `json:"applied"`
	}{system, userJSON, raw, applied}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return
	}
	name := fmt.Sprintf("%s-%s.json", time.Now().UTC().Format("20060102T150405.000"), uuid.NewString()[:8])
	_ = os.WriteFile(filepath.Join(w.debugDump, name), b, 0o644)
}

func (w *Worker) leaseOwner(agentID string) string {
	id := uuid.NewString()
	if agentID == "" {
		return id
	}
	return fmt.Sprintf("%s-%d-%s", agentID, os.Getpid(), id)
}

// parseOutput trims the raw model text, strips a leading/trailing ```json fence
// if present, and unmarshals it into an Output.
func parseOutput(raw string) (Output, error) {
	s := strings.TrimSpace(raw)
	s = stripFence(s)
	var out Output
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return Output{}, err
	}
	return out, nil
}

func stripFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	// Drop the opening fence line (``` or ```json).
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	} else {
		return s
	}
	// Drop a trailing closing fence.
	if j := strings.LastIndex(s, "```"); j >= 0 {
		s = s[:j]
	}
	return strings.TrimSpace(s)
}
