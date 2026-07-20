// ClawEh
// License: MIT

package mcpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/mcpserver/acl"
	"github.com/PivotLLM/ClawEh/pkg/tools"
	"github.com/mark3labs/mcp-go/server"
)

func TestExtractBearer(t *testing.T) {
	cases := []struct {
		header string
		want   string
	}{
		{"Bearer SST123", "SST123"},
		{"bearer SST123", "SST123"},    // scheme is case-insensitive
		{"BEARER  SST123  ", "SST123"}, // surrounding space trimmed
		{"Basic abc", ""},              // wrong scheme
		{"", ""},                       // absent
		{"Bearer", ""},                 // scheme only, no token
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		if c.header != "" {
			r.Header.Set("Authorization", c.header)
		}
		if got := extractBearer(r); got != c.want {
			t.Errorf("extractBearer(%q) = %q, want %q", c.header, got, c.want)
		}
	}
}

// TestBearerAuthMiddleware covers the HTTP-layer 401 gate: missing token,
// unknown token, and a valid registered token (which reaches the next handler).
func TestBearerAuthMiddleware(t *testing.T) {
	st, tok := seedSessionToken("alice")

	var reached atomic.Bool
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Store(true)
		w.WriteHeader(http.StatusOK)
	})
	h := bearerAuthMiddleware(st, next)

	check := func(name, header string, wantCode int, wantReached bool) {
		reached.Store(false)
		r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		if header != "" {
			r.Header.Set("Authorization", header)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != wantCode {
			t.Errorf("%s: status = %d, want %d", name, w.Code, wantCode)
		}
		if reached.Load() != wantReached {
			t.Errorf("%s: next reached = %v, want %v", name, reached.Load(), wantReached)
		}
		if wantCode == http.StatusUnauthorized {
			if got := w.Header().Get("WWW-Authenticate"); !strings.Contains(got, "Bearer") {
				t.Errorf("%s: missing WWW-Authenticate Bearer challenge, got %q", name, got)
			}
		}
	}

	check("no token", "", http.StatusUnauthorized, false)
	check("unknown token", "Bearer SSTdeadbeef", http.StatusUnauthorized, false)
	check("valid token", "Bearer "+tok, http.StatusOK, true)
}

// TestBearerAuthMode_CopiesTokenIntoArgs verifies the bearer authMode lifts the
// context token into args under the session_token key, so dispatch is
// transport-agnostic, while the internal mode leaves args untouched.
func TestBearerAuthMode_CopiesTokenIntoArgs(t *testing.T) {
	ctx := withBearerToken(context.Background(), "SSTfromheader")

	bArgs := map[string]any{}
	bearerAuthMode.prepareArgs(ctx, bArgs)
	if bArgs[sessionTokenParam] != "SSTfromheader" {
		t.Errorf("bearer prepareArgs did not inject token: %v", bArgs)
	}
	if bearerAuthMode.injectParam {
		t.Error("bearer mode must not inject the session_token schema param")
	}

	iArgs := map[string]any{sessionTokenParam: "SSTinarg"}
	internalAuthMode.prepareArgs(context.Background(), iArgs)
	if iArgs[sessionTokenParam] != "SSTinarg" {
		t.Errorf("internal prepareArgs altered args: %v", iArgs)
	}
	if !internalAuthMode.injectParam {
		t.Error("internal mode must inject the session_token schema param")
	}
}

// TestEndpointSchemas_DifferByParam asserts the two endpoints publish the same
// tool but only /internal carries the session_token parameter in its schema.
func TestEndpointSchemas_DifferByParam(t *testing.T) {
	rf := &mockTool{name: "read_file", params: map[string]any{
		"type":       "object",
		"properties": map[string]any{"path": map[string]any{"type": "string"}},
	}, result: tools.NewToolResult("ok")}
	regs := map[string]*tools.ToolRegistry{"alice": newRegistryWith(rf)}
	st, _ := seedSessionToken("alice")
	resolver := resolverFor(regs)

	build := func(mode authMode) string {
		srv := server.NewMCPServer("t", "0")
		addToolsToServer(srv, mode, regs, []string{"*"}, st, resolver, nil, acl.Default, nil, nil, nil)
		listed := srv.ListTools()
		tool, ok := listed["read_file"]
		if !ok {
			t.Fatal("read_file not registered")
		}
		return string(tool.Tool.RawInputSchema)
	}

	if internal := build(internalAuthMode); !strings.Contains(internal, sessionTokenParam) {
		t.Errorf("/internal schema missing %s param: %s", sessionTokenParam, internal)
	}
	if bearer := build(bearerAuthMode); strings.Contains(bearer, sessionTokenParam) {
		t.Errorf("/mcp schema must not carry %s param: %s", sessionTokenParam, bearer)
	}
}
