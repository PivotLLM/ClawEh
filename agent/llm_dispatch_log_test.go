package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/spawnllm/common"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
)

// modelScriptProvider answers each call by the wire model it is sent.
type modelScriptProvider struct {
	answer func(ctx context.Context, model string) (*providers.LLMResponse, error)
}

func (p modelScriptProvider) Chat(ctx context.Context, _ []providers.Message, _ []providers.ToolDefinition, model string, _ map[string]any) (*providers.LLMResponse, error) {
	return p.answer(ctx, model)
}

func (modelScriptProvider) GetDefaultModel() string { return "alice-wire" }

var (
	candOpus  = providers.FallbackCandidate{Provider: "Anthropic", Model: "opus-wire", Alias: "Claude Opus"}
	candBob   = providers.FallbackCandidate{Provider: "Bob's API", Model: "bob-wire", Alias: "Bob Model"}
	candAlice = providers.FallbackCandidate{Provider: "Alice's API", Model: "alice-wire", Alias: "Alice Model"}
)

const rateLimited = "status 429: rate limit exceeded"

// answerOK is a successful answer naming the served model.
func answerOK(model string) *providers.LLMResponse {
	return &providers.LLMResponse{
		Content: "from " + model,
		Status:  &providers.DispatchStatus{Success: true, Model: model + "-2026"},
	}
}

// chainTurn is a turn for alice running candidates through a fallback chain
// whose cooldowns are tracked by cooldown.
func chainTurn(p providers.LLMProvider, cooldown *providers.CooldownTracker, candidates ...providers.FallbackCandidate) (*llmTurn, *llmDispatch) {
	al := &AgentLoop{cfg: &config.Config{}, fallback: providers.NewFallbackChain(cooldown)}
	turn := &llmTurn{
		al:         al,
		agent:      &AgentInstance{ID: "alice", Provider: p},
		candidates: candidates,
		model:      candidates[0].Alias,
		messages:   []providers.Message{{Role: "user", Content: "hi"}},
	}
	d := &llmDispatch{provider: candidates[0].Provider, runProvider: p, runModel: candidates[0].Model}
	return turn, d
}

// Log messages the per-call contract is about.
const (
	msgFinish    = "LLM finish"
	msgFailed    = "Fallback: candidate failed"
	msgSkipped   = "Fallback: skipped candidate (cooldown)"
	msgCallError = "LLM call failed"
)

// callRecords returns, in order, the finish, fallback and call-failed records
// in buf.
func callRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, rec := range readLogRecords(t, buf) {
		msg := fmt.Sprint(rec["message"])
		if msg == msgFinish || msg == msgCallError || strings.HasPrefix(msg, "Fallback: ") {
			out = append(out, rec)
		}
	}
	return out
}

// assertSequence fails unless recs carry exactly the messages want, in order.
func assertSequence(t *testing.T, recs []map[string]any, want ...string) {
	t.Helper()
	got := make([]string, len(recs))
	for i, rec := range recs {
		got[i] = fmt.Sprint(rec["message"])
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("log sequence = %q, want %q", got, want)
	}
}

// A candidate that fails gets its own finish line and its WARN at once,
// before the next candidate's line; the one that answers gets its own.
func TestCallModel_FailedCandidateLoggedAsItFails(t *testing.T) {
	p := modelScriptProvider{answer: func(_ context.Context, model string) (*providers.LLMResponse, error) {
		if model == candOpus.Model {
			return nil, errors.New(anthropic400)
		}
		return answerOK(model), nil
	}}
	turn, d := chainTurn(p, providers.NewCooldownTracker(), candOpus, candBob)

	buf := &bytes.Buffer{}
	t.Cleanup(logger.RedirectForTest(buf))
	resp, err := turn.requestModel(context.Background(), d)
	if err != nil {
		t.Fatalf("requestModel: %v", err)
	}
	if resp.Content != "from bob-wire" {
		t.Fatalf("content = %q, want Bob's answer", resp.Content)
	}

	recs := callRecords(t, buf)
	assertSequence(t, recs, msgFinish, msgFailed, msgFinish, "Fallback: succeeded with Bob's API/bob-wire after 2 attempts")
	assertFields(t, recs[0], map[string]any{
		"our_provider": "Anthropic",
		"our_model":    "Claude Opus",
		"model":        "opus-wire",
		"success":      false,
	})
	if e := fmt.Sprint(recs[0]["error"]); !strings.Contains(e, "thinking.type: adaptive is required") {
		t.Errorf("finish error = %q, want the provider's message", e)
	}
	assertFields(t, recs[1], map[string]any{
		"level":    "warn",
		"provider": "Anthropic",
		"model":    "opus-wire",
		"reason":   string(providers.FailoverFormat),
	})
	if e := fmt.Sprint(recs[1]["error"]); !strings.Contains(e, "thinking.type: adaptive is required") {
		t.Errorf("WARN error = %q, want the provider's message", e)
	}
	assertFields(t, recs[2], map[string]any{
		"our_provider": "Bob's API",
		"our_model":    "Bob Model",
		"model":        "bob-wire-2026",
		"success":      true,
	}, "error")
	assertFields(t, recs[3], map[string]any{"level": "info"})
}

// A call cut off by shutdown gets its finish line, naming the candidate that
// was running, and no candidate-failed WARN; the failure before it keeps its
// lines.
func TestCallModel_CancelledCallLogsRunningCandidate(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	p := modelScriptProvider{answer: func(ctx context.Context, model string) (*providers.LLMResponse, error) {
		if model == candOpus.Model {
			return nil, errors.New(rateLimited)
		}
		cancel(errShuttingDown)
		return nil, ctx.Err()
	}}
	turn, d := chainTurn(p, providers.NewCooldownTracker(), candOpus, candBob)

	buf := &bytes.Buffer{}
	t.Cleanup(logger.RedirectForTest(buf))
	if _, err := turn.requestModel(ctx, d); !errors.Is(err, errShuttingDown) {
		t.Fatalf("requestModel error = %v, want the shutdown interruption", err)
	}

	recs := callRecords(t, buf)
	assertSequence(t, recs, msgFinish, msgFailed, msgFinish)
	assertFields(t, recs[0], map[string]any{"our_model": "Claude Opus", "success": false, "error": rateLimited})
	assertFields(t, recs[1], map[string]any{"provider": "Anthropic", "model": "opus-wire"})
	assertFields(t, recs[2], map[string]any{
		"our_provider": "Bob's API",
		"our_model":    "Bob Model",
		"success":      false,
		"error":        "context canceled",
	})
}

// When every candidate fails, each call has its finish line; only the ones
// the chain fell back from get a WARN, not the last; the turn's failure is
// logged once.
func TestCallModel_AllCandidatesFail(t *testing.T) {
	p := modelScriptProvider{answer: func(context.Context, string) (*providers.LLMResponse, error) {
		return nil, errors.New(rateLimited)
	}}
	turn, d := chainTurn(p, providers.NewCooldownTracker(), candOpus, candBob)

	buf := &bytes.Buffer{}
	t.Cleanup(logger.RedirectForTest(buf))
	if _, err := turn.requestModel(context.Background(), d); err == nil {
		t.Fatal("requestModel succeeded, want an error")
	}

	recs := callRecords(t, buf)
	assertSequence(t, recs, msgFinish, msgFailed, msgFinish, msgCallError)
	assertFields(t, recs[0], map[string]any{"our_model": "Claude Opus", "success": false, "error": rateLimited})
	assertFields(t, recs[1], map[string]any{"model": "opus-wire", "reason": string(providers.FailoverRateLimit)})
	assertFields(t, recs[2], map[string]any{"our_model": "Bob Model", "success": false, "error": rateLimited})
	assertFields(t, recs[3], map[string]any{"level": "error"})
}

// A candidate in cooldown is not called: it has no finish line, and its skip
// is logged once, wherever it sits in the chain.
func TestCallModel_CooldownSkipLoggedOnceWithoutFinish(t *testing.T) {
	tests := []struct {
		name    string
		parked  []providers.FallbackCandidate
		failing map[string]bool
		wantErr bool
		want    []string
	}{
		{
			name:   "skip then answer",
			parked: []providers.FallbackCandidate{candOpus},
			want:   []string{msgSkipped, msgFinish, "Fallback: succeeded with Bob's API/bob-wire after 2 attempts"},
		},
		{
			name:    "failure then trailing skip",
			parked:  []providers.FallbackCandidate{candBob},
			failing: map[string]bool{candOpus.Model: true},
			wantErr: true,
			want:    []string{msgFinish, msgFailed, msgSkipped, msgCallError},
		},
		{
			name:    "all skipped",
			parked:  []providers.FallbackCandidate{candOpus, candBob},
			wantErr: true,
			want:    []string{msgSkipped, msgSkipped, msgCallError},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cooldown := providers.NewCooldownTracker()
			for _, c := range tt.parked {
				cooldown.MarkFailure(c.Provider, c.Model, providers.FailoverRateLimit, 429, 0)
				if cooldown.IsAvailable(c.Provider, c.Model) {
					t.Fatalf("%s is not parked", c.Alias)
				}
			}
			p := modelScriptProvider{answer: func(_ context.Context, model string) (*providers.LLMResponse, error) {
				if tt.failing[model] {
					return nil, errors.New(rateLimited)
				}
				return answerOK(model), nil
			}}
			turn, d := chainTurn(p, cooldown, candOpus, candBob)

			buf := &bytes.Buffer{}
			t.Cleanup(logger.RedirectForTest(buf))
			_, err := turn.requestModel(context.Background(), d)
			if (err != nil) != tt.wantErr {
				t.Fatalf("requestModel error = %v, want error %v", err, tt.wantErr)
			}

			recs := callRecords(t, buf)
			assertSequence(t, recs, tt.want...)
			parked := map[string]bool{}
			for _, c := range tt.parked {
				parked[c.Model] = true
			}
			for _, rec := range recs {
				switch rec["message"] {
				case msgFinish:
					for _, c := range tt.parked {
						if rec["our_model"] == c.Alias {
							t.Errorf("finish line for parked %s", c.Alias)
						}
					}
				case msgSkipped:
					if !parked[fmt.Sprint(rec["model"])] {
						t.Errorf("skip logged for %v, which is not parked", rec["model"])
					}
				}
			}
		})
	}
}

// With no fallback chain the one provider call writes exactly one line.
func TestCallModel_DirectPathOneFinish(t *testing.T) {
	p := modelScriptProvider{answer: func(_ context.Context, model string) (*providers.LLMResponse, error) {
		return answerOK(model), nil
	}}
	turn := &llmTurn{
		al:       &AgentLoop{cfg: &config.Config{}},
		agent:    &AgentInstance{ID: "alice", Provider: p},
		model:    "Alice Model",
		messages: []providers.Message{{Role: "user", Content: "hi"}},
	}
	d := &llmDispatch{provider: "Alice Co", runProvider: p, runModel: "alice-wire"}

	buf := &bytes.Buffer{}
	t.Cleanup(logger.RedirectForTest(buf))
	if _, err := turn.requestModel(context.Background(), d); err != nil {
		t.Fatalf("requestModel: %v", err)
	}

	recs := callRecords(t, buf)
	assertSequence(t, recs, msgFinish)
	assertFields(t, recs[0], map[string]any{
		"our_provider": "Alice Co",
		"our_model":    "Alice Model",
		"model":        "alice-wire-2026",
		"success":      true,
	})
}

// A context-window retry writes one finish line per call: the call that
// overflowed and the retried one.
func TestRequestModel_ContextWindowRetryOneFinishPerCall(t *testing.T) {
	al := newTestAgentLoop(t).al
	agent := al.registry.Default()
	if agent == nil {
		t.Fatal("no default agent")
	}
	const sessionKey = "ctx-window-finish"
	for range 6 {
		addMessage(t, agent.Sessions, sessionKey, "user", "old user msg")
		addMessage(t, agent.Sessions, sessionKey, "assistant", "old assistant reply")
	}
	agent.Provider = &sequenceProvider{
		responses: []*providers.LLMResponse{nil, {Content: "recovered"}},
		errors:    []error{errors.New("context_length_exceeded: request too long"), nil},
	}
	opts := processOptions{SessionKey: sessionKey, Channel: "cli", ChatID: "direct", UserMessage: "trigger"}
	cm, releaseCM := al.getContextManager(agent, sessionKey)
	defer releaseCM()

	buf := &bytes.Buffer{}
	t.Cleanup(logger.RedirectForTest(buf))
	if got := runIteration(t, al, agent, []providers.Message{{Role: "user", Content: "trigger"}}, opts, cm).content; got != "recovered" {
		t.Fatalf("content = %q, want the retried answer", got)
	}

	var agentCalls []map[string]any
	for _, rec := range finishRecords(t, buf) {
		if rec["kind"] == llmKindAgent {
			agentCalls = append(agentCalls, rec)
		}
	}
	if len(agentCalls) != 2 {
		t.Fatalf("agent finish lines = %d, want 2: %v", len(agentCalls), agentCalls)
	}
	assertFields(t, agentCalls[0], map[string]any{"success": false, "error": "context_length_exceeded: request too long"})
	assertFields(t, agentCalls[1], map[string]any{"success": true}, "error")
}

// failSkipAnswer is a turn whose chain fails Opus, skips Bob (in cooldown)
// and is answered by Alice's model.
func failSkipAnswer(t *testing.T) (*llmTurn, *llmDispatch) {
	t.Helper()
	cooldown := providers.NewCooldownTracker()
	cooldown.MarkFailure(candBob.Provider, candBob.Model, providers.FailoverRateLimit, 429, 0)
	if cooldown.IsAvailable(candBob.Provider, candBob.Model) {
		t.Fatal("Bob Model is not parked")
	}
	p := modelScriptProvider{answer: func(_ context.Context, model string) (*providers.LLMResponse, error) {
		if model == candOpus.Model {
			return nil, errors.New(rateLimited)
		}
		return answerOK(model), nil
	}}
	return chainTurn(p, cooldown, candOpus, candBob, candAlice)
}

// The turn's own notifier still hears of each candidate passed over: the
// failure and the skip, each with the candidate tried next.
func TestCallModel_ForwardsToTurnNotifier(t *testing.T) {
	turn, d := failSkipAnswer(t)
	type notice struct {
		passed []providers.FallbackAttempt
		next   providers.FallbackCandidate
	}
	var notices []notice
	turn.notifier = func(passed []providers.FallbackAttempt, next providers.FallbackCandidate) {
		notices = append(notices, notice{passed: passed, next: next})
	}
	t.Cleanup(logger.RedirectForTest(&bytes.Buffer{}))
	if _, err := turn.requestModel(context.Background(), d); err != nil {
		t.Fatalf("requestModel: %v", err)
	}

	if len(notices) != 2 {
		t.Fatalf("notices = %d, want 2: %+v", len(notices), notices)
	}
	first, second := notices[0], notices[1]
	if len(first.passed) != 1 || first.passed[0].Skipped || first.passed[0].Model != candOpus.Model || first.next != candBob {
		t.Errorf("first notice = %+v, want Opus's failure then Bob", first)
	}
	if len(second.passed) != 1 || !second.passed[0].Skipped || second.passed[0].Model != candBob.Model || second.next != candAlice {
		t.Errorf("second notice = %+v, want Bob's skip then Alice", second)
	}
}

// Fail, skip, answer: finish lines for the two calls only, one WARN for the
// failure and one skip line, in the order they happened.
func TestCallModel_FailSkipAnswerOrder(t *testing.T) {
	turn, d := failSkipAnswer(t)
	buf := &bytes.Buffer{}
	t.Cleanup(logger.RedirectForTest(buf))
	if _, err := turn.requestModel(context.Background(), d); err != nil {
		t.Fatalf("requestModel: %v", err)
	}

	recs := callRecords(t, buf)
	assertSequence(t, recs, msgFinish, msgFailed, msgSkipped, msgFinish,
		"Fallback: succeeded with Alice's API/alice-wire after 3 attempts")
	assertFields(t, recs[0], map[string]any{"our_model": "Claude Opus", "success": false})
	assertFields(t, recs[1], map[string]any{"model": "opus-wire"})
	assertFields(t, recs[2], map[string]any{"model": "bob-wire"})
	assertFields(t, recs[3], map[string]any{"our_model": "Alice Model", "success": true})
}

// A same-model retry after a short Retry-After is not a fallback: both calls
// have a finish line, and no candidate-failed WARN is written.
func TestCallModel_SameModelRetryNoWarn(t *testing.T) {
	calls := 0
	p := modelScriptProvider{answer: func(_ context.Context, model string) (*providers.LLMResponse, error) {
		calls++
		if calls == 1 {
			return nil, &common.HTTPStatusError{StatusCode: 429, BodyPreview: "slow down", RetryAfter: time.Millisecond}
		}
		return answerOK(model), nil
	}}
	turn, d := chainTurn(p, providers.NewCooldownTracker(), candOpus, candBob)

	buf := &bytes.Buffer{}
	t.Cleanup(logger.RedirectForTest(buf))
	if _, err := turn.requestModel(context.Background(), d); err != nil {
		t.Fatalf("requestModel: %v", err)
	}

	recs := callRecords(t, buf)
	assertSequence(t, recs, msgFinish, msgFinish)
	assertFields(t, recs[0], map[string]any{"our_model": "Claude Opus", "success": false})
	assertFields(t, recs[1], map[string]any{"our_model": "Claude Opus", "success": true}, "error")
}
