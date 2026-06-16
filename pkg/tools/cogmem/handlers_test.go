// ClawEh - Cognitive Memory
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package cogmem

import (
	"context"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/global"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

const testSession = "chan:123"

// buildHandlers maps bare tool names to their global handlers, built against a
// temp workspace. RegisterTools recovers the workspace from deps.Host as a
// tools.ToolDeps, so the test supplies a real one.
func buildHandlers(t *testing.T) (map[string]global.ToolHandler, string) {
	t.Helper()
	ws := t.TempDir()
	defs := GlobalProvider.RegisterTools(global.Deps{Host: tools.ToolDeps{Workspace: ws, AgentID: "alice"}})
	m := make(map[string]global.ToolHandler, len(defs))
	for _, d := range defs {
		m[d.Name] = d.Handler
	}
	return m, ws
}

func newCall(session string, args map[string]any) *global.ToolCall {
	if args == nil {
		args = map[string]any{}
	}
	return &global.ToolCall{Ctx: context.Background(), Args: args, AgentID: "alice", Session: session}
}

func run(t *testing.T, h global.ToolHandler, call *global.ToolCall) *global.Result {
	t.Helper()
	res, err := h(call)
	if err != nil {
		t.Fatalf("handler returned go error: %v", err)
	}
	if res == nil {
		t.Fatal("nil result")
	}
	return res
}

func TestCreateRememberGetUpdateRetire(t *testing.T) {
	h, _ := buildHandlers(t)

	// create_domain returns an id.
	res := run(t, h["domain_create"], newCall(testSession, map[string]any{
		"type": "project", "name": "Bob's project", "summary": "demo",
	}))
	if res.IsError {
		t.Fatalf("create_domain error: %s", res.ForLLM)
	}
	domainID := extractID(t, res.ForLLM, "d")

	// remember adds a hook to the domain.
	res = run(t, h["hook_create"], newCall(testSession, map[string]any{
		"domain_id": domainID, "kind": "fact", "text": "the sky is blue",
	}))
	if res.IsError {
		t.Fatalf("remember error: %s", res.ForLLM)
	}
	hookID := extractID(t, res.ForLLM, "h")

	// get_domain shows the hook id and text.
	res = run(t, h["domain_get"], newCall(testSession, map[string]any{"id": domainID}))
	if res.IsError {
		t.Fatalf("get_domain error: %s", res.ForLLM)
	}
	if !strings.Contains(res.ForLLM, hookID) || !strings.Contains(res.ForLLM, "the sky is blue") {
		t.Fatalf("get_domain missing hook: %s", res.ForLLM)
	}

	// update_domain with the correct version succeeds.
	res = run(t, h["domain_update"], newCall(testSession, map[string]any{
		"id": domainID, "expected_version": float64(1), "set_summary": "updated",
		"set_blockers": []any{"waiting on review"},
	}))
	if res.IsError {
		t.Fatalf("update_domain error: %s", res.ForLLM)
	}

	// update_domain with a stale version is rejected clearly.
	res = run(t, h["domain_update"], newCall(testSession, map[string]any{
		"id": domainID, "expected_version": float64(1), "set_summary": "again",
	}))
	if !res.IsError || !strings.Contains(res.ForLLM, "version conflict") {
		t.Fatalf("expected version conflict, got: isErr=%v %s", res.IsError, res.ForLLM)
	}

	// retire_hook removes it from active memory.
	res = run(t, h["hook_retire"], newCall(testSession, map[string]any{
		"id": hookID, "reason": "no longer true",
	}))
	if res.IsError {
		t.Fatalf("retire_hook error: %s", res.ForLLM)
	}

	// search no longer finds it (retired != active).
	res = run(t, h["hook_search"], newCall(testSession, map[string]any{"query": "sky"}))
	if res.IsError {
		t.Fatalf("search error: %s", res.ForLLM)
	}
	if strings.Contains(res.ForLLM, hookID) {
		t.Fatalf("retired hook still found in search: %s", res.ForLLM)
	}
}

func TestRememberWithDomainHint(t *testing.T) {
	h, _ := buildHandlers(t)
	res := run(t, h["hook_create"], newCall(testSession, map[string]any{
		"domain_hint": "new project", "kind": "preference", "text": "use tabs",
	}))
	if res.IsError {
		t.Fatalf("remember(hint) error: %s", res.ForLLM)
	}
	if !strings.Contains(res.ForLLM, "Stored hook h") {
		t.Fatalf("unexpected result: %s", res.ForLLM)
	}
}

func TestSearchActiveVsReview(t *testing.T) {
	h, _ := buildHandlers(t)
	res := run(t, h["domain_create"], newCall(testSession, map[string]any{"type": "project", "name": "p"}))
	domainID := extractID(t, res.ForLLM, "d")

	// active hook
	run(t, h["hook_create"], newCall(testSession, map[string]any{
		"domain_id": domainID, "kind": "fact", "text": "active widget", "status": "active",
	}))
	// review hook
	run(t, h["hook_create"], newCall(testSession, map[string]any{
		"domain_id": domainID, "kind": "fact", "text": "review widget", "status": "review",
	}))

	res = run(t, h["hook_search"], newCall(testSession, map[string]any{"query": "widget"}))
	if !strings.Contains(res.ForLLM, "active widget") {
		t.Fatalf("search missed active hook: %s", res.ForLLM)
	}
	if strings.Contains(res.ForLLM, "review widget") {
		t.Fatalf("search returned review hook: %s", res.ForLLM)
	}
}

func TestForgetRetiresMatches(t *testing.T) {
	h, _ := buildHandlers(t)
	res := run(t, h["domain_create"], newCall(testSession, map[string]any{"type": "project", "name": "p"}))
	domainID := extractID(t, res.ForLLM, "d")
	for _, txt := range []string{"forget me one", "forget me two", "keep this"} {
		run(t, h["hook_create"], newCall(testSession, map[string]any{
			"domain_id": domainID, "kind": "fact", "text": txt,
		}))
	}
	res = run(t, h["hook_forget"], newCall(testSession, map[string]any{"query": "forget me"}))
	if res.IsError {
		t.Fatalf("forget error: %s", res.ForLLM)
	}
	if !strings.Contains(res.ForLLM, "Retired 2 hook") {
		t.Fatalf("expected 2 retired, got: %s", res.ForLLM)
	}
	// "keep this" should still be searchable.
	res = run(t, h["hook_search"], newCall(testSession, map[string]any{"query": "keep this"}))
	if !strings.Contains(res.ForLLM, "keep this") {
		t.Fatalf("forget removed the wrong hook: %s", res.ForLLM)
	}
}

func TestArchiveAndListDomains(t *testing.T) {
	h, _ := buildHandlers(t)
	res := run(t, h["domain_create"], newCall(testSession, map[string]any{"type": "project", "name": "Bob proj"}))
	domainID := extractID(t, res.ForLLM, "d")

	res = run(t, h["domain_list"], newCall(testSession, map[string]any{"status": "active"}))
	if !strings.Contains(res.ForLLM, domainID) {
		t.Fatalf("active list missing domain: %s", res.ForLLM)
	}

	if r := run(t, h["domain_archive"], newCall(testSession, map[string]any{"id": domainID})); r.IsError {
		t.Fatalf("archive error: %s", r.ForLLM)
	}
	res = run(t, h["domain_list"], newCall(testSession, map[string]any{"status": "active"}))
	if strings.Contains(res.ForLLM, domainID) {
		t.Fatalf("archived domain still active: %s", res.ForLLM)
	}
}

func TestStatusAndConsolidate(t *testing.T) {
	h, _ := buildHandlers(t)
	res := run(t, h["memory_status"], newCall(testSession, nil))
	if res.IsError {
		t.Fatalf("status error: %s", res.ForLLM)
	}
	if !strings.Contains(res.ForLLM, "Pending (review) hooks: 0") ||
		!strings.Contains(res.ForLLM, "Last consolidation run: none") {
		t.Fatalf("unexpected status: %s", res.ForLLM)
	}

	res = run(t, h["memory_consolidate"], newCall(testSession, nil))
	if res.IsError || !strings.Contains(res.ForLLM, "worker not yet running") {
		t.Fatalf("unexpected consolidate result: %s", res.ForLLM)
	}
}

func TestEmptySessionErrors(t *testing.T) {
	h, _ := buildHandlers(t)
	for _, name := range []string{"domain_get", "hook_search", "hook_create", "memory_status", "domain_create"} {
		res := run(t, h[name], newCall("", map[string]any{"id": "dXXXXX", "query": "x", "kind": "fact", "text": "t", "type": "project", "name": "n"}))
		if !res.IsError {
			t.Fatalf("%s: expected error for empty session, got: %s", name, res.ForLLM)
		}
	}
}

func TestRememberRejectsSecret(t *testing.T) {
	h, _ := buildHandlers(t)
	res := run(t, h["hook_create"], newCall(testSession, map[string]any{
		"domain_hint": "creds", "kind": "fact", "text": "the api_key is sk-123",
	}))
	if !res.IsError || !strings.Contains(res.ForLLM, "secret") {
		t.Fatalf("expected secret rejection, got: isErr=%v %s", res.IsError, res.ForLLM)
	}
}

// extractID pulls the first whitespace-delimited token beginning with prefix
// followed by Crockford chars (e.g. "d3K9P") from text.
func extractID(t *testing.T, text, prefix string) string {
	t.Helper()
	for _, tok := range strings.FieldsFunc(text, func(r rune) bool {
		return r == ' ' || r == '\n' || r == '\t' || r == '(' || r == ')' || r == ',' || r == '.'
	}) {
		if len(tok) == 6 && strings.HasPrefix(tok, prefix) && isCrockfordTail(tok[1:]) {
			return tok
		}
	}
	t.Fatalf("no %s-id found in: %s", prefix, text)
	return ""
}

// isCrockfordTail reports whether s is all uppercase Crockford base32 digits
// (the id tail), distinguishing a real id from an English word like "domain".
func isCrockfordTail(s string) bool {
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	for _, c := range s {
		if !strings.ContainsRune(alphabet, c) {
			return false
		}
	}
	return true
}
