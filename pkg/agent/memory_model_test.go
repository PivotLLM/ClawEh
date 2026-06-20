package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/llmcontext"
	"github.com/PivotLLM/ClawEh/pkg/providers"
)

type fakeMemModel struct {
	content string
	err     error
	calls   int
}

func (f *fakeMemModel) Complete(_ context.Context, _ []providers.Message) (llmcontext.LLMReply, error) {
	f.calls++
	return llmcontext.LLMReply{Content: f.content}, f.err
}

// TestConsolidate_EmptyContentFallsThrough verifies a model that returns empty
// content (no error) is skipped in favour of the next model, instead of
// returning "" (which the worker records as invalid_json / unexpected end of
// JSON input).
func TestConsolidate_EmptyContentFallsThrough(t *testing.T) {
	empty := &fakeMemModel{content: "   "}              // empty/whitespace, no error
	good := &fakeMemModel{content: `{"operations":[]}`} // valid, parseable Output
	c := &memoryModelCaller{
		clients:   []llmcontext.LLMClient{empty, good},
		names:     []string{"empty-model", "good-model"},
		modelName: "empty-model",
	}

	got, model, err := c.Consolidate(context.Background(), "sys", "{}")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != `{"operations":[]}` {
		t.Fatalf("content = %q, want the second model's output", got)
	}
	if model != "good-model" {
		t.Fatalf("model = %q, want the model that produced the reply", model)
	}
	if empty.calls != 1 || good.calls != 1 {
		t.Fatalf("calls: empty=%d good=%d, want 1/1", empty.calls, good.calls)
	}
}

// TestConsolidate_InvalidJSONFallsThrough verifies a model that returns
// non-empty but unparseable text (a bare fence) is skipped in favour of the
// next model — so the worker never has to record a raw JSON-parser error.
func TestConsolidate_InvalidJSONFallsThrough(t *testing.T) {
	fence := &fakeMemModel{content: "```json\n```"}     // non-empty, fence-only → parses to ""
	good := &fakeMemModel{content: `{"operations":[]}`} // valid
	c := &memoryModelCaller{
		clients:   []llmcontext.LLMClient{fence, good},
		names:     []string{"fence-model", "good-model"},
		modelName: "fence-model",
	}

	got, model, err := c.Consolidate(context.Background(), "sys", "{}")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != `{"operations":[]}` || model != "good-model" {
		t.Fatalf("got %q/%q, want valid output from good-model", got, model)
	}
	if fence.calls != 1 || good.calls != 1 {
		t.Fatalf("calls: fence=%d good=%d, want 1/1", fence.calls, good.calls)
	}
}

// TestConsolidate_AllUnusableReturnsCleanError verifies that when no model
// yields a usable, parseable response, the caller returns a clean,
// human-readable error (never the raw "unexpected end of JSON input") and still
// attributes the attempt to the head model.
func TestConsolidate_AllUnusableReturnsCleanError(t *testing.T) {
	c := &memoryModelCaller{
		clients: []llmcontext.LLMClient{
			&fakeMemModel{content: ""},             // empty
			&fakeMemModel{content: "```\n```"},     // unparseable
			&fakeMemModel{err: errors.New("boom")}, // transport error
		},
		names:     []string{"a", "b", "c"},
		modelName: "a",
	}
	got, model, err := c.Consolidate(context.Background(), "sys", "{}")
	if err == nil {
		t.Fatal("expected an error when no model returns a usable response")
	}
	if got != "" {
		t.Fatalf("content = %q, want empty", got)
	}
	if model != "a" {
		t.Fatalf("model = %q, want head model on total failure", model)
	}
	if !strings.Contains(err.Error(), "no usable response") {
		t.Fatalf("error = %v, want a clean message", err)
	}
	if strings.Contains(err.Error(), "unexpected end of JSON") {
		t.Fatalf("error leaked raw JSON-parser message: %v", err)
	}
}
