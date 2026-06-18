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
	empty := &fakeMemModel{content: "   "}        // empty/whitespace, no error
	good := &fakeMemModel{content: `{"ok":true}`} // valid
	c := &memoryModelCaller{clients: []llmcontext.LLMClient{empty, good}}

	got, err := c.Consolidate(context.Background(), "sys", "{}")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != `{"ok":true}` {
		t.Fatalf("content = %q, want the second model's output", got)
	}
	if empty.calls != 1 || good.calls != 1 {
		t.Fatalf("calls: empty=%d good=%d, want 1/1", empty.calls, good.calls)
	}
}

// TestConsolidate_AllEmptyReturnsError verifies that when every model yields
// empty content, the caller returns an error (recorded as "error", not the
// confusing "invalid_json"), rather than an empty string.
func TestConsolidate_AllEmptyReturnsError(t *testing.T) {
	c := &memoryModelCaller{clients: []llmcontext.LLMClient{
		&fakeMemModel{content: ""},
		&fakeMemModel{err: errors.New("boom")},
	}}
	got, err := c.Consolidate(context.Background(), "sys", "{}")
	if err == nil {
		t.Fatal("expected an error when all models fail/return empty")
	}
	if got != "" {
		t.Fatalf("content = %q, want empty", got)
	}
	if !strings.Contains(err.Error(), "all memory models failed") {
		t.Fatalf("error = %v", err)
	}
}
