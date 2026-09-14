// ClawEh
// License: MIT

package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PivotLLM/spawnllm/openai_compat"

	"github.com/PivotLLM/ClawEh/llmcontext"
	llmlogger "github.com/PivotLLM/ClawEh/llmcontext/logger"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
)

// scriptedProvider answers Chat with a canned response or error and records
// every call's model and options, so a test can see the chain walk.
type scriptedProvider struct {
	mu      sync.Mutex
	content string
	err     error
	calls   []scriptedCall
}

type scriptedCall struct {
	model string
	opts  map[string]any
}

func (p *scriptedProvider) Chat(_ context.Context, _ []providers.Message, _ []providers.ToolDefinition, model string, opts map[string]any) (*providers.LLMResponse, error) {
	p.mu.Lock()
	p.calls = append(p.calls, scriptedCall{model: model, opts: opts})
	p.mu.Unlock()
	if p.err != nil {
		return nil, p.err
	}
	return &providers.LLMResponse{Content: p.content, FinishReason: "stop"}, nil
}

func (p *scriptedProvider) GetDefaultModel() string { return "scripted" }

func (p *scriptedProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

func compressClient(p providers.LLMProvider, providerName, model string) *providerLLMClient {
	return &providerLLMClient{provider: p, model: model, providerName: providerName, requestJSONObject: true}
}

// TestCompressModelCaller_WalksChainAndSkipsExcluded: the first model not in
// Exclude answers, the reply names it, and excluded models are never called.
func TestCompressModelCaller_WalksChainAndSkipsExcluded(t *testing.T) {
	first := &scriptedProvider{content: "from-first"}
	second := &scriptedProvider{content: "from-second"}
	c := &compressModelCaller{clients: []*providerLLMClient{
		compressClient(first, "p1", "model-a"),
		compressClient(second, "p2", "model-b"),
	}}

	reply, err := c.Complete(context.Background(), llmcontext.ModelRequest{System: "s", User: "u", JSONObject: true})
	if err != nil || reply.Content != "from-first" || reply.Model != "model-a" {
		t.Fatalf("Complete = %+v, %v; want model-a's reply", reply, err)
	}

	reply, err = c.Complete(context.Background(), llmcontext.ModelRequest{System: "s", User: "u", Exclude: []string{"model-a"}})
	if err != nil || reply.Content != "from-second" || reply.Model != "model-b" {
		t.Fatalf("Complete with model-a excluded = %+v, %v; want model-b's reply", reply, err)
	}
	if first.callCount() != 1 {
		t.Fatalf("excluded model was called: %d calls", first.callCount())
	}

	_, err = c.Complete(context.Background(), llmcontext.ModelRequest{Exclude: []string{"model-a", "model-b"}})
	if !errors.Is(err, llmcontext.ErrNoModel) || !strings.Contains(err.Error(), "2 excluded") {
		t.Fatalf("every model excluded should yield ErrNoModel with the count, got %v", err)
	}
}

// TestCompressModelCaller_ErrorMovesOnAndNamesLastModel: a transport error on
// one model moves to the next; when every model fails the last error comes
// back together with the last model tried, so the report can name it.
func TestCompressModelCaller_ErrorMovesOnAndNamesLastModel(t *testing.T) {
	broken := &scriptedProvider{err: errors.New("boom")}
	good := &scriptedProvider{content: "ok"}
	c := &compressModelCaller{clients: []*providerLLMClient{
		compressClient(broken, "p1", "model-a"),
		compressClient(good, "p2", "model-b"),
	}}
	reply, err := c.Complete(context.Background(), llmcontext.ModelRequest{})
	if err != nil || reply.Model != "model-b" {
		t.Fatalf("expected the chain to move past the failing model: %+v, %v", reply, err)
	}

	onlyBroken := &compressModelCaller{clients: []*providerLLMClient{compressClient(broken, "p1", "model-a")}}
	reply, err = onlyBroken.Complete(context.Background(), llmcontext.ModelRequest{})
	if err == nil || reply.Model != "model-a" {
		t.Fatalf("expected the last error with the model named: %+v, %v", reply, err)
	}
}

// TestCompressModelCaller_JSONObjectForwarded: the engine's JSONObject flag
// becomes the provider's response-format option, and its absence sends none.
func TestCompressModelCaller_JSONObjectForwarded(t *testing.T) {
	p := &scriptedProvider{content: "{}"}
	c := &compressModelCaller{clients: []*providerLLMClient{compressClient(p, "p1", "m")}}
	if _, err := c.Complete(context.Background(), llmcontext.ModelRequest{JSONObject: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Complete(context.Background(), llmcontext.ModelRequest{}); err != nil {
		t.Fatal(err)
	}
	if v, _ := p.calls[0].opts[openai_compat.ResponseFormatJSONObjectOption].(bool); !v {
		t.Errorf("JSONObject request did not set the response-format option: %v", p.calls[0].opts)
	}
	if p.calls[1].opts != nil {
		t.Errorf("plain request should carry no options, got %v", p.calls[1].opts)
	}
}

// TestCompressModelCaller_SharedCooldownSkipsModel: a model parked in the
// shared cooldown tracker (e.g. an out-of-credits 402 hit by the main chain)
// is skipped by the compaction path — not retried — and the error says so.
func TestCompressModelCaller_SharedCooldownSkipsModel(t *testing.T) {
	tracker := providers.NewCooldownTrackerWithPolicy(providers.DefaultCooldownPolicy())
	tracker.MarkFailure("Abliteration", "abliterated-model", providers.FailoverBilling, 402, 0)

	p := &scriptedProvider{content: "{}"}
	c := &compressModelCaller{
		clients:  []*providerLLMClient{compressClient(p, "Abliteration", "abliterated-model")},
		cooldown: tracker,
	}
	_, err := c.Complete(context.Background(), llmcontext.ModelRequest{})
	if !errors.Is(err, llmcontext.ErrNoModel) || !strings.Contains(err.Error(), "1 in cooldown") {
		t.Fatalf("cooled model should be skipped with ErrNoModel naming the cooldown, got %v", err)
	}
	if p.callCount() != 0 {
		t.Fatalf("model cooled in the shared tracker must not be called; calls=%d", p.callCount())
	}
}

// TestCompressModelCaller_BillingFailurePutsModelInCooldown: a summarization
// model returning a billing (402) error is put in the shared cooldown and not
// retried on the next call — so an out-of-credits summarizer is not hammered.
func TestCompressModelCaller_BillingFailurePutsModelInCooldown(t *testing.T) {
	tracker := providers.NewCooldownTrackerWithPolicy(providers.DefaultCooldownPolicy())
	p := &scriptedProvider{err: errors.New("API request failed:   Status: 402   Body: {\"error\":{\"billing_url\":\"x\"}}")}
	c := &compressModelCaller{
		clients:  []*providerLLMClient{compressClient(p, "Abliteration", "abliterated-model")},
		cooldown: tracker,
	}

	reply, err := c.Complete(context.Background(), llmcontext.ModelRequest{})
	if err == nil || reply.Model != "abliterated-model" || p.callCount() != 1 {
		t.Fatalf("first call: reply=%+v err=%v calls=%d", reply, err, p.callCount())
	}
	if tracker.IsAvailable("Abliteration", "abliterated-model") {
		t.Fatal("402 should have parked the model in the shared cooldown")
	}

	_, err = c.Complete(context.Background(), llmcontext.ModelRequest{})
	if !errors.Is(err, llmcontext.ErrNoModel) || p.callCount() != 1 {
		t.Fatalf("second call should skip the cooled model without dispatching: err=%v calls=%d", err, p.callCount())
	}
}

// TestSessionTokenLayer pins the token section's placement and text, and that
// an empty token yields an empty (skipped) layer.
func TestSessionTokenLayer(t *testing.T) {
	l := sessionTokenLayer("SST-abc")
	if l.Name != "session_token" || !l.AfterSummary {
		t.Fatalf("layer = %+v; want session_token after the summary", l)
	}
	if !strings.HasPrefix(l.Text, "# Session Token\n\n") || !strings.HasSuffix(l.Text, "session_token: SST-abc") {
		t.Fatalf("token layer text = %q", l.Text)
	}
	if empty := sessionTokenLayer(""); empty.Text != "" || !empty.AfterSummary {
		t.Fatalf("empty token should give an empty after-summary layer: %+v", empty)
	}
}

// issuingSTI hands out a new token on every Issue call.
type issuingSTI struct {
	recordingSTI
	n int
}

func (s *issuingSTI) Issue(_, _, _ string) string {
	s.n++
	return "SST-" + strings.Repeat("x", s.n)
}

// TestSessionToken_IssuedAndReissuedOnEntry: the token issued when a session's
// context manager is created lands in the dispatch layers, and a reissue
// (session clear) replaces it in place.
func TestSessionToken_IssuedAndReissuedOnEntry(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("no default agent")
	}
	al.SetSessionTokenIssuer(&issuingSTI{})

	const key = "token-entry"
	_, release := al.getContextManager(agent, key)
	defer release()

	opts := processOptions{SessionKey: key, Channel: "cli", ChatID: "direct"}
	layers := al.promptLayers(agent, opts)
	last := layers[len(layers)-1]
	if last.Name != "session_token" || !strings.HasSuffix(last.Text, "session_token: SST-x") {
		t.Fatalf("first dispatch token layer = %+v", last)
	}

	al.reissueSessionToken(agent, key)
	layers = al.promptLayers(agent, opts)
	last = layers[len(layers)-1]
	if !strings.HasSuffix(last.Text, "session_token: SST-xx") {
		t.Fatalf("reissued token not rendered: %+v", last)
	}
	if al.sessionToken(agent, "no-such-session") != "" {
		t.Fatal("an uncached session has no token")
	}
}

// logCapture collects host log lines for the bridge test.
type logCapture struct {
	mu  sync.Mutex
	buf []byte
}

func (c *logCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.buf = append(c.buf, p...)
	return len(p), nil
}

func (c *logCapture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return string(c.buf)
}

// TestInstallLogging_BridgesEngineLogs: after InstallLogging, an event the
// engine emits through its own seam shows up in ClawEh's logger with its
// level, component and fields.
func TestInstallLogging_BridgesEngineLogs(t *testing.T) {
	var out logCapture
	restore := logger.RedirectForTest(&out)
	defer restore()
	InstallLogging()
	defer llmlogger.SetBackend(nil)

	llmlogger.WarnCF("llmcontext", "bridge check", map[string]any{"session_key": "s1"})
	llmlogger.DebugCF("memory", "debug bridge", nil)
	time.Sleep(10 * time.Millisecond)

	got := out.String()
	for _, want := range []string{"bridge check", "llmcontext", "s1", "\"level\":\"warn\"", "debug bridge", "\"level\":\"debug\""} {
		if !strings.Contains(got, want) {
			t.Errorf("host log missing %q:\n%s", want, got)
		}
	}
}
