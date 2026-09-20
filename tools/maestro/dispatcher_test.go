// ClawEh
// License: MIT

package maestro

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	mllm "github.com/PivotLLM/Maestro/llm"

	"github.com/PivotLLM/ClawEh/global"
	toolsagents "github.com/PivotLLM/ClawEh/tools/agents"
)

// stubRunner is a SyncRunner that records what it was asked and returns a
// canned result or error. With block set it waits for ctx to end and returns
// ctx.Err(), standing in for a sub-agent that never finishes.
type stubRunner struct {
	gotTask  string
	gotModel string
	gotDepth int
	res      *global.SyncResult
	err      error
	block    bool
}

func (r *stubRunner) RunSync(ctx context.Context, task, model string) (*global.SyncResult, error) {
	r.gotTask, r.gotModel, r.gotDepth = task, model, toolsagents.SpawnDepth(ctx)
	if r.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if r.err != nil {
		return nil, r.err
	}
	if r.res == nil {
		return &global.SyncResult{Content: "ok"}, nil
	}
	return r.res, nil
}

// TestDispatcher_PropagatesDepth verifies the Maestro host dispatcher forwards
// the caller's context (carrying sub-agent depth) to the SyncRunner, so a
// Maestro worker's dispatch is bounded by MaxSpawnDepth rather than resetting to
// depth 0.
func TestDispatcher_PropagatesDepth(t *testing.T) {
	runner := &stubRunner{}
	d := &dispatcher{run: runner}

	ctx := toolsagents.WithSpawnDepth(context.Background(), 2)
	if _, err := d.Dispatch(ctx, &mllm.DispatchRequest{Prompt: "do it"}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if runner.gotDepth != 2 {
		t.Fatalf("dispatcher dropped depth: SyncRunner saw %d, want 2", runner.gotDepth)
	}
}

// TestDispatcher_ModelHintPassthrough: Maestro's placeholders mean "host
// default"; anything else is passed to the host as a model alias.
func TestDispatcher_ModelHintPassthrough(t *testing.T) {
	cases := map[string]string{
		"": "", "default": "", "Default": "", "host": "", "claw": "", "  fast  ": "fast", "Pro": "Pro",
	}
	for hint, want := range cases {
		runner := &stubRunner{}
		d := &dispatcher{run: runner}
		if _, err := d.Dispatch(context.Background(), &mllm.DispatchRequest{Prompt: "p", LLMID: hint}); err != nil {
			t.Fatalf("Dispatch(%q): %v", hint, err)
		}
		if runner.gotModel != want {
			t.Errorf("llm_model_id %q → host model %q, want %q", hint, runner.gotModel, want)
		}
	}
}

// TestDispatcher_MapsUsageAndModel: tokens, cost, iterations and the served
// model flow from the sub-agent run into Maestro's result.
func TestDispatcher_MapsUsageAndModel(t *testing.T) {
	runner := &stubRunner{res: &global.SyncResult{
		Content: "the answer", Iterations: 4,
		Model: "claude-x", Provider: "anthropic",
		InputTokens: 1200, OutputTokens: 300, CacheReadTokens: 50, CacheCreationTokens: 7, CostUSD: 0.42,
	}}
	d := &dispatcher{run: runner}

	res, err := d.Dispatch(context.Background(), &mllm.DispatchRequest{Prompt: "audit control X"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if !res.Success || res.ExitCode != 0 || res.Text != "the answer" || !res.NormalTermination || !res.ResponseParsed {
		t.Fatalf("result = %+v, want success with text", res)
	}
	if res.InputTokens != 1200 || res.OutputTokens != 300 || res.CacheReadTokens != 50 || res.CacheCreationTokens != 7 {
		t.Errorf("tokens not mapped: %+v", res)
	}
	if res.CostUSD != 0.42 || res.NumTurns != 4 || res.ProviderModel != "claude-x" {
		t.Errorf("cost/turns/model not mapped: cost=%v turns=%d model=%q", res.CostUSD, res.NumTurns, res.ProviderModel)
	}
	if res.BytesSent != int64(len("audit control X")) || res.BytesReceived != int64(len("the answer")) {
		t.Errorf("byte counts not mapped: %+v", res)
	}

	// A run that did not report its model falls back to the synthetic label.
	runner.res.Model = ""
	res, _ = d.Dispatch(context.Background(), &mllm.DispatchRequest{Prompt: "p"})
	if res.ProviderModel != hostProviderModel {
		t.Errorf("unreported model → %q, want %q", res.ProviderModel, hostProviderModel)
	}
}

// TestDispatcher_PermanentErrors: failures no retry can fix surface as Maestro
// permanent errors (the runner fails the task immediately).
func TestDispatcher_PermanentErrors(t *testing.T) {
	for _, sentinel := range []error{global.ErrSpawnDepthExceeded, global.ErrModelNotAvailable, global.ErrSpawnUnavailable} {
		d := &dispatcher{run: &stubRunner{err: fmt.Errorf("%w: details", sentinel)}}
		res, err := d.Dispatch(context.Background(), &mllm.DispatchRequest{Prompt: "p"})
		if res != nil || err == nil {
			t.Fatalf("%v: got result %+v err %v, want (nil, permanent error)", sentinel, res, err)
		}
		if !mllm.IsPermanent(err) || !errors.Is(err, sentinel) {
			t.Errorf("%v: err %v is not a permanent error wrapping the sentinel", sentinel, err)
		}
	}

	// No runner at all is permanent too.
	res, err := (&dispatcher{}).Dispatch(context.Background(), &mllm.DispatchRequest{Prompt: "p"})
	if res != nil || !mllm.IsPermanent(err) {
		t.Errorf("missing runner: got %+v / %v, want permanent error", res, err)
	}
}

// TestDispatcher_TransientError: an ordinary failure is a non-zero exit (the
// runner's retry budget applies), not an infrastructure error.
func TestDispatcher_TransientError(t *testing.T) {
	d := &dispatcher{run: &stubRunner{err: errors.New("LLM call failed after retries: 503")}}
	res, err := d.Dispatch(context.Background(), &mllm.DispatchRequest{Prompt: "p"})
	if err != nil {
		t.Fatalf("transient error must not be returned as err, got %v", err)
	}
	if res.ExitCode != 1 || res.Success || res.StopReason != "error" || !strings.Contains(res.Stderr, "503") {
		t.Errorf("result = %+v, want exit 1 with stderr", res)
	}
}

// TestDispatcher_Timeout: a sub-agent that outlives the dispatch timeout is cut
// off and reported as a retryable timeout.
func TestDispatcher_Timeout(t *testing.T) {
	d := &dispatcher{run: &stubRunner{block: true}, timeout: 30 * time.Millisecond}
	start := time.Now()
	res, err := d.Dispatch(context.Background(), &mllm.DispatchRequest{Prompt: "never finishes"})
	if err != nil {
		t.Fatalf("timeout must map to a result, got err %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("dispatch did not honour the timeout")
	}
	if res.ExitCode != 1 || res.StopReason != "timeout" || !strings.Contains(res.Stderr, "turn timeout") {
		t.Errorf("result = %+v, want exit 1 / stop_reason timeout", res)
	}
}

// TestDispatcher_CallerCancelIsNotATimeout: with a timeout configured, a caller
// whose own context ends first is reported as an error, not as the turn timeout.
func TestDispatcher_CallerCancelIsNotATimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := &dispatcher{run: &stubRunner{block: true}, timeout: time.Minute}
	res, err := d.Dispatch(ctx, &mllm.DispatchRequest{Prompt: "p"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if res.ExitCode != 1 || res.StopReason != "error" || strings.Contains(res.Stderr, "turn timeout") {
		t.Errorf("caller cancellation mislabelled: %+v", res)
	}
}

// TestDispatcher_NoTimeoutWhenUnset: timeout 0 leaves the caller's context alone.
func TestDispatcher_NoTimeoutWhenUnset(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	runner := &stubRunner{block: true}
	d := &dispatcher{run: runner}
	done := make(chan *mllm.DispatchResult, 1)
	go func() {
		res, _ := d.Dispatch(ctx, &mllm.DispatchRequest{Prompt: "p"})
		done <- res
	}()
	select {
	case <-done:
		t.Fatal("dispatch returned before the caller cancelled; an unset timeout must not cut it off")
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	res := <-done
	if res.StopReason == "timeout" {
		t.Errorf("caller cancellation was reported as a timeout: %+v", res)
	}
}
