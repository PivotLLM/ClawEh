// ClawEh
// License: MIT

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/PivotLLM/ClawEh/classifier"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/tools"
	"github.com/PivotLLM/ClawEh/tools/untrusted"
)

// mailServer is an external MCP server with a mailbox: list returns the
// message ids, read one message, as JSON text content.
type mailServer struct{}

func (mailServer) CallTool(_ context.Context, _, toolName string, args map[string]any) (*mcp.CallToolResult, error) {
	text := `{"value":[{"id":"m1"},{"id":"m2"}]}`
	if toolName == "read" {
		from := "news@example.org"
		if args["id"] == "m1" {
			from = "bob@example.com"
		}
		text = fmt.Sprintf(`{"from":{"address":%q},"subject":"About %s"}`, from, args["id"])
	}
	return &mcp.CallToolResult{Content: []mcp.Content{mcp.TextContent{Type: "text", Text: text}}}, nil
}

// newHostService is a classifier service over host, as the gateway builds
// it (no notifier, no auditor).
func newHostService(t *testing.T, host *ClassifierHost) *classifier.Service {
	t.Helper()
	svc := classifier.New(classifier.Host{
		Tools: host, Model: host, Logger: logger.NewLogger("classifier"),
		Modes: host.Modes, Ceilings: host.Ceilings, Scopes: host.Scopes, Redact: host.Redact,
		WrapUntrusted: untrusted.Wrap,
	})
	t.Cleanup(func() {
		if err := svc.Close(context.Background()); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return svc
}

// A tool of an external MCP server marks its result as untrusted content
// for the model; a classifier reads it unwrapped, as the JSON it is, both as
// its source and as its fetch. The agent's own turns still get it wrapped.
func TestClassifierHost_UntrustedResults(t *testing.T) {
	srv := &chatServer{reply: "urgent\nfrom a client"}
	ts := httptest.NewServer(http.HandlerFunc(srv.handle))
	defer ts.Close()
	al, host, _ := newClassifierHostLoop(t, classifierHostConfig(t, ts.URL))
	list := tools.NewMCPTool(mailServer{}, "mail", mcp.Tool{Name: "list"})
	read := tools.NewMCPTool(mailServer{}, "mail", mcp.Tool{Name: "read"})
	registerOn(t, al, "alice", list)
	registerOn(t, al, "alice", read)

	inst, _ := al.GetRegistry().GetConfigured("alice")
	if res := inst.Tools.ExecuteWithContext(context.Background(), list.Name(), map[string]any{}, "", "", nil); res == nil ||
		!strings.HasPrefix(res.ForLLM, untrusted.Preamble) {
		t.Fatalf("an agent turn's result = %+v, want it marked untrusted", res)
	}
	out, err := host.Call(context.Background(), "alice", list.Name(), map[string]any{})
	if err != nil || out != `{"value":[{"id":"m1"},{"id":"m2"}]}` {
		t.Fatalf("Call = %q, %v; want the JSON unwrapped", out, err)
	}

	svc := newHostService(t, host)
	scope := host.Scopes()[0]
	def, err := json.Marshal(map[string]any{
		"id": "inbox", "enabled": false, "trigger": map[string]any{"every_seconds": 60},
		"source": map[string]any{"tool": list.Name(), "items": "value", "id": "id"},
		"fetch": map[string]any{
			"tool": read.Name(), "args": map[string]any{"id": "{{item.id}}"},
			"fields": map[string]any{"from": "from.address", "subject": "subject"},
		},
		"classify": map[string]any{"instructions": "Sort it.", "categories": []any{map[string]any{"name": "urgent"}, map[string]any{"name": "low"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Create(context.Background(), scope, def); err != nil {
		t.Fatalf("Create with an MCP source = %v", err)
	}
	res, err := svc.Test(context.Background(), scope, "inbox", "m1")
	if err != nil || res.Outcome != classifier.OutcomeRecorded || res.Category != "urgent" || len(res.MissingFields) != 0 {
		t.Fatalf("Test = %+v, %v; want m1 classified with every fetch field", res.Decision, err)
	}
	srv.mu.Lock()
	last := srv.requests[len(srv.requests)-1]
	srv.mu.Unlock()
	body, err := json.Marshal(last)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "bob@example.com") || strings.Contains(string(body), "UNTRUSTED_CONTENT") {
		t.Errorf("model request = %s, want the fetched fields without the wrapper", body)
	}
}

// Through the real registry, a source tool's error reaches classifier_test
// with the tool named once; a provider's error is redacted.
func TestClassifierHost_ErrorsNameToolOnceAndAreRedacted(t *testing.T) {
	const key = "sk-live-0123456789abcdefghij"
	al, host, _ := newClassifierHostLoop(t, classifierHostConfig(t, "http://127.0.0.1:0"))
	registerOn(t, al, "alice", &classifierTool{name: "inbox_list", out: `{"items":[{"id":"m1"}]}`})
	svc := newHostService(t, host)
	scope := host.Scopes()[0]
	def, err := json.Marshal(map[string]any{
		"id": "inbox", "enabled": false, "trigger": map[string]any{"every_seconds": 60},
		"source":   map[string]any{"tool": "inbox_list", "items": "items", "id": "id"},
		"classify": map[string]any{"instructions": "Sort it.", "categories": []any{map[string]any{"name": "urgent"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Create(context.Background(), scope, def); err != nil {
		t.Fatalf("Create: %v", err)
	}
	registerOn(t, al, "alice", &classifierTool{name: "inbox_list", out: "401 Unauthorized: api_key=" + key, isError: true})
	_, err = svc.Test(context.Background(), scope, "inbox", "")
	if err == nil || strings.Contains(err.Error(), key) ||
		!strings.HasPrefix(err.Error(), "source inbox_list: the tool failed.\n"+untrusted.Preamble) ||
		!strings.Contains(err.Error(), "401 Unauthorized: api_key=") || strings.Count(err.Error(), "inbox_list") != 1 {
		t.Errorf("Test = %v, want the tool named once, its message marked untrusted and the key redacted", err)
	}
	if got := host.Redact("bad key " + key); strings.Contains(got, key) {
		t.Errorf("Redact = %q", got)
	}
}

// StateFailed raises the operator alert naming the agent and the path, once
// per agent id; before Bind it raises nothing.
func TestClassifierHost_StateFailedAlert(t *testing.T) {
	NewClassifierHost().StateFailed(context.Background(), "alice", "/x/state.db", errors.New("boom")) // no loop: nothing
	al, host, _ := newClassifierHostLoop(t, classifierHostConfig(t, "http://127.0.0.1:0"))
	rec := &alertRecorder{}
	al.SetAlerter(rec)
	host.StateFailed(context.Background(), "alice", "/ws/classifiers/state.db", errors.New("is a directory"))
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.alerts) != 1 {
		t.Fatalf("alerts = %+v", rec.alerts)
	}
	a := rec.alerts[0]
	if a.Title != "Classifier state unavailable" || a.EventID != "classifier-state:alice" || a.Details != "is a directory" ||
		a.Description != "agent Alice: /ws/classifiers/state.db cannot be opened, so its classifiers do not run" {
		t.Errorf("alert = %+v", a)
	}
}
