package web

import (
	"context"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/global"
)

// TestGlobalProvider_NilToolsReturnGracefulError reproduces the crash where an
// agent invoked web_search on a server with no search provider configured: the
// instance was nil and the handler dereferenced it (SIGSEGV → crash loop). With
// no usable config, search/fetch are nil and the handlers must return an error
// result instead of panicking.
func TestGlobalProvider_NilToolsReturnGracefulError(t *testing.T) {
	// Zero Deps → Cfg is nil → search and fetch are left nil.
	defs := globalWebProvider{}.RegisterTools(global.Deps{})
	handlers := map[string]global.ToolHandler{}
	for _, d := range defs {
		handlers[d.Name] = d.Handler
	}

	for _, name := range []string{"search", "fetch"} {
		h, ok := handlers[name]
		if !ok {
			t.Fatalf("%s tool not registered", name)
		}
		res, err := h(&global.ToolCall{Ctx: context.Background(), Args: map[string]any{"query": "x", "url": "http://example.com"}})
		if err != nil {
			t.Fatalf("%s handler returned go error: %v", name, err)
		}
		if res == nil || !res.IsError {
			t.Fatalf("%s on unconfigured server should return an error result, got %+v", name, res)
		}
		if !strings.Contains(res.ForLLM, "not configured") {
			t.Fatalf("%s error message should explain it is not configured: %q", name, res.ForLLM)
		}
	}
}
