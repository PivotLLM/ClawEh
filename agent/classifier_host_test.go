// ClawEh
// License: MIT

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/agentreg"
	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/classifier"
	"github.com/PivotLLM/ClawEh/classifier/classifierfs"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/audit"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/tools"
	toolsclassifier "github.com/PivotLLM/ClawEh/tools/classifier"
	toolschedule "github.com/PivotLLM/ClawEh/tools/schedule"
)

// classifierTool is a tool registered straight on an agent's registry.
type classifierTool struct {
	name    string
	out     string
	isError bool
}

func (c *classifierTool) Name() string               { return c.name }
func (c *classifierTool) Description() string        { return "test tool " + c.name }
func (c *classifierTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (c *classifierTool) Execute(context.Context, map[string]any) *tools.ToolResult {
	if c.isError {
		return tools.ErrorResult(c.out)
	}
	return tools.NewToolResult(c.out)
}

type sessionClassifierTool struct{ classifierTool }

func (sessionClassifierTool) IsSessionScoped() bool { return true }

// chatServer is an OpenAI-compatible endpoint: it records each request and
// answers with reply, or with status when it is set.
type chatServer struct {
	mu       sync.Mutex
	requests []map[string]any
	reply    string
	status   int
}

func (s *chatServer) handle(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.requests = append(s.requests, body)
	reply, status := s.reply, s.status
	s.mu.Unlock()
	out := []byte(`{"error":{"message":"slow down"}}`)
	if status == 0 {
		status = http.StatusOK
		var err error
		out, err = json.Marshal(map[string]any{"choices": []any{map[string]any{
			"message": map[string]any{"role": "assistant", "content": reply}, "finish_reason": "stop",
		}}})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := w.Write(out); err != nil {
		return
	}
}

func (s *chatServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

// classifierHostConfig has Alice (default, classifiers on, model alpha) and
// Bob (classifiers off) on an OpenAI-compatible provider at url; beta is a
// model that is not in Classification.
func classifierHostConfig(t *testing.T, url string) *config.Config {
	t.Helper()
	cfg := ownerTestConfig(t)
	cfg.Providers = []config.Provider{{Name: "p", Protocol: "openai-chat", BaseURL: url + "/v1", APIKey: "k"}}
	cfg.Models = []config.ModelConfig{
		{ModelName: "alpha", Model: "alpha-wire", Provider: "p", Enabled: true},
		{ModelName: "beta", Model: "beta-wire", Provider: "p", Enabled: true},
	}
	cfg.Agents.Defaults.Models = []string{"alpha"}
	cfg.Agents.List = []config.AgentConfig{
		{
			ID: "alice", Name: "Alice", Default: true, Models: []string{"alpha"},
			Classifiers: &config.AgentClassifiersConfig{Enabled: true, Model: "alpha"},
		},
		{ID: "bob", Name: "Bob"},
	}
	// alpha, and the models some tests switch to, are in Classification;
	// beta is not.
	cfg.TaskModels.TextModels = []config.TaskModelRef{{Model: "beta"}}
	cfg.TaskModels.ClassifyModels = []config.TaskModelRef{{Model: "alpha"}, {Model: "gamma"}, {Model: "cc"}}
	return cfg
}

// newClassifierHostLoop builds a loop over cfg with a real dispatcher and
// binds a host to it.
func newClassifierHostLoop(t *testing.T, cfg *config.Config) (*AgentLoop, *ClassifierHost, *bus.MessageBus) {
	t.Helper()
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))
	tools.RegisterProvider(tools.NamespacedProvider(toolsclassifier.Namespace, toolsclassifier.GlobalProvider))
	msgBus := bus.NewMessageBus()
	al := mustNewAgentLoop(t, cfg, msgBus, &mockProvider{}, providers.NewProviderDispatcher(cfg), OwnsDataDir())
	host := NewClassifierHost()
	host.Bind(al)
	return al, host, msgBus
}

// registerOn registers tool on the configured agent id's registry.
func registerOn(t *testing.T, al *AgentLoop, id string, tool tools.Tool) {
	t.Helper()
	inst, ok := al.GetRegistry().GetConfigured(id)
	if !ok {
		t.Fatalf("no agent %s", id)
	}
	inst.Tools.Register(tool)
}

// Only agents with the switch on are scopes, with <workspace>/classifiers;
// before Bind there are none.
func TestClassifierHost_Scopes(t *testing.T) {
	if got := NewClassifierHost().Scopes(); got != nil {
		t.Fatalf("Scopes before Bind = %v", got)
	}
	al, host, _ := newClassifierHostLoop(t, classifierHostConfig(t, "http://127.0.0.1:0"))
	alice, _ := al.GetRegistry().GetConfigured("alice")
	want := []classifier.Scope{{AgentID: "alice", BaseDirectory: filepath.Join(alice.Workspace, classifierfs.BaseDirName)}}
	if got := host.Scopes(); !slices.Equal(got, want) {
		t.Fatalf("Scopes = %+v, want %+v", got, want)
	}
}

// Tools run as the agent through its own registry: a tool it lacks and a
// session-scoped tool are refused by name; a tool's error is returned.
func TestClassifierHost_Tools(t *testing.T) {
	al, host, _ := newClassifierHostLoop(t, classifierHostConfig(t, "http://127.0.0.1:0"))
	registerOn(t, al, "alice", &classifierTool{name: "inbox_list", out: `[{"id":"m1"}]`})
	registerOn(t, al, "alice", &classifierTool{name: "inbox_broken", out: "the mailbox is locked", isError: true})
	registerOn(t, al, "alice", &sessionClassifierTool{classifierTool{name: "chat_only", out: "x"}})
	ctx := context.Background()

	if err := host.Has("alice", "inbox_list"); err != nil {
		t.Errorf("Has(inbox_list) = %v", err)
	}
	if out, err := host.Call(ctx, "alice", "inbox_list", map[string]any{}); err != nil || out != `[{"id":"m1"}]` {
		t.Errorf("Call(inbox_list) = %q, %v", out, err)
	}
	for _, tc := range []struct {
		agent, tool, want string
	}{
		{"alice", "nosuch", "tool nosuch is not available to agent alice"},
		{"bob", "inbox_list", "tool inbox_list is not available to agent bob"},
		{"alice", "chat_only", "tool chat_only works only in a conversation"},
	} {
		if err := host.Has(tc.agent, tc.tool); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Has(%s, %s) = %v, want %q", tc.agent, tc.tool, err, tc.want)
		}
		if _, err := host.Call(ctx, tc.agent, tc.tool, map[string]any{}); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Call(%s, %s) = %v, want %q", tc.agent, tc.tool, err, tc.want)
		}
	}
	if _, err := host.Call(ctx, "alice", "inbox_broken", map[string]any{}); err == nil || !strings.Contains(err.Error(), "the mailbox is locked") {
		t.Errorf("Call(inbox_broken) = %v, want the tool's message", err)
	}
	if _, err := host.Call(ctx, "nobody", "inbox_list", map[string]any{}); err == nil {
		t.Error("Call for an unknown agent succeeded")
	}
	// A classifier tool is refused at run time even when the agent has it.
	registerOn(t, al, "alice", &classifierTool{name: "classifier_update", out: "updated"})
	const refusal = "classifier_update can't be used by a classifier."
	if err := host.Has("alice", "classifier_update"); err == nil || err.Error() != refusal {
		t.Errorf("Has(classifier_update) = %v, want %q", err, refusal)
	}
	if _, err := host.Call(ctx, "alice", "classifier_update", map[string]any{}); err == nil || err.Error() != refusal {
		t.Errorf("Call(classifier_update) = %v, want %q", err, refusal)
	}
	if err := NewClassifierHost().Has("alice", "inbox_list"); !errors.Is(err, errLoopNotReady) {
		t.Errorf("Has before Bind = %v", err)
	}
}

// The model call uses the agent's classifier model, with no tools, and
// returns its name; no model, a model not among the agent's, and a model in
// cooldown are refused without a request; a failure puts the model in
// cooldown.
func TestClassifierHost_Complete(t *testing.T) {
	srv := &chatServer{reply: "urgent\nfrom a client"}
	ts := httptest.NewServer(http.HandlerFunc(srv.handle))
	defer ts.Close()
	ctx := context.Background()

	cfg := classifierHostConfig(t, ts.URL)
	al, host, _ := newClassifierHostLoop(t, cfg)
	reply, model, err := host.Complete(ctx, "alice", "Sort it.", "<item>hi</item>")
	if err != nil || reply != "urgent\nfrom a client" || model != "alpha" {
		t.Fatalf("Complete = %q, %q, %v", reply, model, err)
	}
	req := srv.requests[0]
	if req["model"] != "alpha-wire" {
		t.Errorf("request model = %v, want alpha-wire", req["model"])
	}
	if _, ok := req["tools"]; ok {
		t.Errorf("request offered tools: %v", req["tools"])
	}
	msgs, ok := req["messages"].([]any)
	if !ok || len(msgs) != 2 {
		t.Fatalf("messages = %v", req["messages"])
	}
	for i, want := range []map[string]any{
		{"role": "system", "content": "Sort it."},
		{"role": "user", "content": "<item>hi</item>"},
	} {
		m, ok := msgs[i].(map[string]any)
		if !ok || m["role"] != want["role"] || m["content"] != want["content"] {
			t.Errorf("message %d = %v, want %v", i, msgs[i], want)
		}
	}

	// A failure is recorded against the shared cooldown policy; the next call
	// is refused with the model named, without a request.
	srv.mu.Lock()
	srv.status = http.StatusTooManyRequests
	srv.mu.Unlock()
	if _, _, err = host.Complete(ctx, "alice", "s", "u"); err == nil {
		t.Fatal("Complete on a 429 succeeded")
	}
	sent := srv.count()
	_, _, err = host.Complete(ctx, "alice", "s", "u")
	var cd *classifier.CooldownError
	if !errors.As(err, &cd) || cd.Model != "alpha" || cd.Remaining <= 0 {
		t.Fatalf("Complete in cooldown = %v, want a CooldownError for alpha", err)
	}
	if srv.count() != sent {
		t.Error("a model in cooldown was called")
	}
	al.cooldownTracker().MarkSuccess("p", "alpha-wire")

	// An item still in flight after the switch was turned off is asked on
	// the agent's model as before.
	cfg.Agents.List[0].Classifiers.Enabled = false
	srv.mu.Lock()
	srv.status = 0
	srv.mu.Unlock()
	if _, model, err := host.Complete(ctx, "alice", "s", "u"); err != nil || model != "alpha" {
		t.Fatalf("Complete with the switch off = %q, %v", model, err)
	}
	cfg.Agents.List[0].Classifiers.Enabled = true

	for _, tc := range []struct {
		name  string
		model string
		off   bool // the switch was turned off while an item was in flight
		want  string
	}{
		{"no model", "", false, "has no classifier model"},
		{"not in Classification", "beta", false, "model beta is not in Classification (agent Alice)"},
		{"not in Classification, switch off", "beta", true, "model beta is not in Classification (agent Alice)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := classifierHostConfig(t, ts.URL)
			cfg.Agents.List[0].Classifiers.Model = tc.model
			cfg.Agents.List[0].Classifiers.Enabled = !tc.off
			_, host, _ := newClassifierHostLoop(t, cfg)
			before := srv.count()
			_, _, err := host.Complete(ctx, "alice", "s", "u")
			if !errors.Is(err, classifier.ErrNoModel) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Complete = %v, want ErrNoModel with %q", err, tc.want)
			}
			if srv.count() != before {
				t.Error("the model was called")
			}
		})
	}
}

// A CLI classifier model runs in an empty folder of its own under the
// temporary agents' root, named for the classifier and removed afterwards,
// and never with the bypass flags, even with bypass_restrictions on.
func TestClassifierHost_CLIIsolated(t *testing.T) {
	logDir := t.TempDir()
	cfg := classifierHostConfig(t, "http://127.0.0.1:0")
	cfg.Providers = append(cfg.Providers, config.Provider{
		Name: "Claude CLI", Protocol: "claude-cli", Command: fakeClaudeCLI(t, logDir), BypassRestrictions: true,
	})
	cfg.Models = append(cfg.Models, config.ModelConfig{ModelName: "cc", Model: "claude-cli", Provider: "Claude CLI", Enabled: true})
	cfg.Agents.List[0].Models = []string{"cc"}
	cfg.Agents.List[0].Classifiers.Model = "cc"
	if err := os.MkdirAll(cfg.CLIPath(), 0o700); err != nil {
		t.Fatal(err)
	}
	al, host, _ := newClassifierHostLoop(t, cfg)
	registerOn(t, al, "alice", &classifierTool{name: "inbox_list", out: `[{"id":"m1","subject":"hi"}]`})

	svc := classifier.New(classifier.Host{Tools: host, Model: host, Logger: logger.NewLogger("classifier"), Modes: host.Modes, Scopes: host.Scopes})
	t.Cleanup(func() {
		if err := svc.Close(context.Background()); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	scope := host.Scopes()[0]
	def := `{"id":"inbox-sort","enabled":true,"dry_run":true,"trigger":{"every_seconds":3600},
		"source":{"tool":"inbox_list","items":"","id":"id"},
		"classify":{"instructions":"Sort it.","categories":[{"name":"urgent"},{"name":"low"}]}}`
	if _, err := svc.Create(context.Background(), scope, []byte(def)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	d, err := svc.Test(context.Background(), scope, "inbox-sort", "m1")
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if d.Model != "cc" {
		t.Errorf("decision model = %q, want cc", d.Model)
	}

	dir := strings.TrimSpace(readLog(t, logDir, "pwd"))
	root := realPath(t, al.GetRegistry().TempRoot())
	if parent := realPath(t, filepath.Dir(dir)); parent != root {
		t.Errorf("CLI ran in %s, want a folder under %s", dir, root)
	}
	if !strings.HasPrefix(filepath.Base(dir), "classifier-inbox-sort-") {
		t.Errorf("CLI folder %s is not named for the classifier", dir)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("CLI folder %s was not removed (err=%v)", dir, err)
	}
	args := strings.Split(strings.TrimSpace(readLog(t, logDir, "args")), "\n")
	for _, flag := range config.CLIAgentByProtocol("claude-cli").BypassArgs {
		if slices.Contains(args, flag) {
			t.Errorf("bypass flag %s passed to the classifier's CLI: %v", flag, args)
		}
	}
}

// The summary goes straight to the agent's default chat, after the agent's
// name; an agent with no default chat gets none.
func TestClassifierHost_Summary(t *testing.T) {
	cfg := classifierHostConfig(t, "http://127.0.0.1:0")
	cfg.Bindings = []config.AgentBinding{{
		AgentID: "alice", Default: true,
		Match: config.BindingMatch{Channel: "telegram", Peer: &config.PeerMatch{Kind: "direct", ID: "u1"}},
	}}
	_, host, msgBus := newClassifierHostLoop(t, cfg)
	text := "Classifier inbox-sort: 2 new items, 1 acted, 1 unclassified, 0 failed. Use classifier_activity for details."
	if err := host.Summary(context.Background(), "alice", text); err != nil {
		t.Fatalf("Summary: %v", err)
	}
	out := nextOutbound(t, msgBus)
	if out.Channel != "telegram" || out.ChatID != "u1" || out.Content != "Alice: "+text {
		t.Fatalf("summary = %+v", out)
	}
	if err := host.Summary(context.Background(), "bob", text); err == nil || !strings.Contains(err.Error(), "Bob has no default chat") {
		t.Fatalf("Summary for Bob = %v, want no default chat", err)
	}
	noOutbound(t, msgBus)
}

// Definitions written and items handled are audit rows under the owner.
func TestClassifierHost_Record(t *testing.T) {
	if err := audit.Init(t.TempDir()); err != nil {
		t.Fatalf("audit.Init: %v", err)
	}
	t.Cleanup(func() {
		if err := audit.Close(); err != nil {
			t.Errorf("audit.Close: %v", err)
		}
	})
	if audit.KindClassifierWrite != classifier.AuditWrite || audit.KindClassification != classifier.AuditClassification {
		t.Fatal("audit kinds and classifier kinds differ")
	}
	host := NewClassifierHost()
	host.Record(classifier.AuditEvent{
		Kind: classifier.AuditWrite, Agent: "alice", Classifier: "inbox-sort", Outcome: "ok",
		Details: map[string]any{"op": "update", "patch": json.RawMessage(`{"enabled":false}`)},
	})
	host.Record(classifier.AuditEvent{
		Kind: classifier.AuditClassification, Agent: "alice", Classifier: "inbox-sort",
		ItemID: "m1", Category: "urgent", Action: "inbox_move", Outcome: classifier.OutcomeActed,
	})
	host.Record(classifier.AuditEvent{
		Kind: classifier.AuditClassification, Agent: "alice", Classifier: "inbox-sort",
		ItemID: "m2", Category: "low", Action: "inbox_move", Outcome: classifier.OutcomeFailed,
		Details: map[string]any{"error": "the mailbox is locked"},
	})

	flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := audit.Default().Flush(flushCtx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	writes, err := audit.Default().Query(context.Background(), audit.Filter{Kind: audit.KindClassifierWrite, Agent: "alice"})
	if err != nil || len(writes) != 1 {
		t.Fatalf("classifier_write rows = %v, %v", writes, err)
	}
	if w := writes[0]; w.Summary != "alice/inbox-sort" || w.Outcome != audit.OutcomeOK || !strings.Contains(w.Details, `"op":"update"`) ||
		strings.Contains(w.Details, `"clone"`) {
		t.Errorf("write row = %+v", w)
	}
	rows, err := audit.Default().Query(context.Background(), audit.Filter{Kind: audit.KindClassification, Agent: "alice"})
	if err != nil || len(rows) != 2 {
		t.Fatalf("classification rows = %v, %v", rows, err)
	}
	for _, r := range rows {
		var details map[string]any
		if err := json.Unmarshal([]byte(r.Details), &details); err != nil {
			t.Fatalf("details not JSON: %v", err)
		}
		wantOutcome := audit.OutcomeOK
		if details["item_id"] == "m2" {
			wantOutcome = audit.OutcomeError
		}
		if r.Tool != "inbox_move" || r.Summary != "alice/inbox-sort" || r.Outcome != wantOutcome || details["category"] == "" {
			t.Errorf("classification row = %+v", r)
		}
	}
}

// A clone's classifier tools act as the agent it copies: what it creates is
// in the source's folder, owned by the source, and its audit row is under
// the source with the clone named in the details.
func TestClassifierHost_CloneActsAsOwner(t *testing.T) {
	if err := audit.Init(t.TempDir()); err != nil {
		t.Fatalf("audit.Init: %v", err)
	}
	t.Cleanup(func() {
		if err := audit.Close(); err != nil {
			t.Errorf("audit.Close: %v", err)
		}
	})
	cfg := classifierHostConfig(t, "http://127.0.0.1:0")
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))
	tools.RegisterProvider(tools.NamespacedProvider(toolsclassifier.Namespace, toolsclassifier.GlobalProvider))
	// As the gateway does: the service exists before the loop builds the
	// agents' tools, and the host is bound afterwards.
	host := NewClassifierHost()
	svc := classifier.New(classifier.Host{Tools: host, Model: host, Auditor: host, Logger: logger.NewLogger("classifier"), Modes: host.Modes, Scopes: host.Scopes})
	toolsclassifier.SetService(svc)
	t.Cleanup(func() {
		toolsclassifier.SetService(nil)
		if err := svc.Close(context.Background()); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	al := mustNewAgentLoop(t, cfg, bus.NewMessageBus(), &mockProvider{}, providers.NewProviderDispatcher(cfg), OwnsDataDir())
	host.Bind(al)
	registerOn(t, al, "alice", &classifierTool{name: "inbox_list", out: `[{"id":"m1"}]`})

	cloneID, err := al.GetRegistry().CreateClone("alice")
	if err != nil {
		t.Fatalf("CreateClone: %v", err)
	}
	clone, _ := al.GetRegistry().Get(cloneID)
	if _, ok := clone.Tools.Get("classifier_create"); !ok {
		t.Fatal("the clone has no classifier tools")
	}
	if bob, _ := al.GetRegistry().GetConfigured("bob"); bob != nil {
		if _, ok := bob.Tools.Get("classifier_create"); ok {
			t.Fatal("Bob (classifiers off) has classifier tools")
		}
	}
	res := clone.Tools.ExecuteWithContext(context.Background(), "classifier_create", map[string]any{
		"definition": map[string]any{
			"id": "inbox-sort", "enabled": false, "trigger": map[string]any{"every_seconds": 60},
			"source":   map[string]any{"tool": "inbox_list", "items": "", "id": "id"},
			"classify": map[string]any{"instructions": "Sort it.", "categories": []any{map[string]any{"name": "urgent"}}},
		},
	}, "", "", nil)
	if res == nil || res.IsError {
		t.Fatalf("classifier_create by the clone = %+v", res)
	}
	alice, _ := al.GetRegistry().GetConfigured("alice")
	path := filepath.Join(alice.Workspace, classifierfs.BaseDirName, "inbox-sort.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("definition not in Alice's folder: %v", err)
	}
	var def struct {
		Owner string `json:"owner"`
	}
	if err = json.Unmarshal(raw, &def); err != nil || def.Owner != "alice" {
		t.Fatalf("owner = %q (%v), want alice", def.Owner, err)
	}
	flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = audit.Default().Flush(flushCtx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	rows, err := audit.Default().Query(context.Background(), audit.Filter{Kind: audit.KindClassifierWrite, Agent: "alice"})
	if err != nil || len(rows) != 1 {
		t.Fatalf("classifier_write rows under alice = %v, %v", rows, err)
	}
	var details map[string]any
	if err := json.Unmarshal([]byte(rows[0].Details), &details); err != nil {
		t.Fatalf("details not JSON: %v", err)
	}
	if details["clone"] != agentreg.ShortID(cloneID) || details["op"] != "create" {
		t.Errorf("details = %s, want op create by clone %s", rows[0].Details, agentreg.ShortID(cloneID))
	}
	// Created with the agents area's exact modes.
	m := cfg.AgentModes(path)
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != m.File {
		t.Errorf("definition mode = %v (%v), want %o", fi.Mode().Perm(), err, m.File)
	}
}

// hangingTool never answers until its call is cancelled.
type hangingTool struct{ classifierTool }

func (h *hangingTool) Execute(ctx context.Context, _ map[string]any) *tools.ToolResult {
	<-ctx.Done()
	return tools.ErrorResult(ctx.Err().Error())
}

// A classifier tool call is bounded by the watch probe's timeout; one that
// runs past it fails with the tool named.
func TestClassifierHost_ToolCallTimeout(t *testing.T) {
	al, host, _ := newClassifierHostLoop(t, classifierHostConfig(t, "http://127.0.0.1:0"))
	fc := fakeClock(al)
	registerOn(t, al, "alice", &hangingTool{classifierTool{name: "inbox_list"}})

	done := make(chan error, 1)
	go func() {
		_, err := host.Call(context.Background(), "alice", "inbox_list", map[string]any{})
		done <- err
	}()
	advanceWhenWaiting(t, fc, 1, toolschedule.WatchProbeTimeout)
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "tool inbox_list did not answer within 1m0s") {
			t.Fatalf("Call = %v, want the timeout naming the tool", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the call did not end at the timeout")
	}
}

// A classifier model call's cost counts toward the daily spend alert like
// any other model call.
func TestClassifierHost_CountsTowardDailySpend(t *testing.T) {
	script := filepath.Join(t.TempDir(), "claude")
	body := "#!/bin/sh\ncat > /dev/null\n" +
		"printf '%s' '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"urgent\",\"total_cost_usd\":0.75}'\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := classifierHostConfig(t, "http://127.0.0.1:0")
	cfg.Providers = append(cfg.Providers, config.Provider{Name: "Claude CLI", Protocol: "claude-cli", Command: script})
	cfg.Models = append(cfg.Models, config.ModelConfig{ModelName: "cc", Model: "claude-cli", Provider: "Claude CLI", Enabled: true})
	cfg.Agents.List[0].Models = []string{"cc"}
	cfg.Agents.List[0].Classifiers.Model = "cc"
	cfg.Agents.Defaults.DailySpendAlertUSD = 1.0
	al, host, _ := newClassifierHostLoop(t, cfg)
	rec := &alertRecorder{}
	al.SetAlerter(rec)

	alerts := func() int {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		return len(rec.alerts)
	}
	if _, _, err := host.Complete(context.Background(), "alice", "s", "u"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if n := alerts(); n != 0 {
		t.Fatalf("alerts after $0.75 = %d, want 0", n)
	}
	if _, _, err := host.Complete(context.Background(), "alice", "s", "u"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if n := alerts(); n != 1 {
		t.Fatalf("alerts after $1.50 = %d, want 1", n)
	}
}

// Ceilings are the install's maximums and the processed-id retention from
// the loop's current configuration, the defaults for unset fields (and
// before Bind); a reload applies to the next call.
func TestClassifierHost_Ceilings(t *testing.T) {
	defaults := classifier.Ceilings{
		MaxRules:         config.DefaultClassifierMaxRules,
		MaxPromptChars:   config.DefaultClassifierMaxPromptChars,
		MaxItemChars:     config.DefaultClassifierMaxItemChars,
		MaxItemsPerRun:   config.DefaultClassifierMaxItemsPerRun,
		MaxParallel:      config.DefaultClassifierMaxParallel,
		MaxActivityHours: config.DefaultClassifierMaxActivityHours,
		MinEverySeconds:  config.DefaultClassifierMinEverySeconds,
		ProcessedDays:    config.DefaultClassifierProcessedDays,
	}
	if got := NewClassifierHost().Ceilings(); got != defaults {
		t.Fatalf("Ceilings before Bind = %+v, want the defaults %+v", got, defaults)
	}

	cfg := classifierHostConfig(t, "http://127.0.0.1:0")
	cfg.Classifiers.ProcessedDays = 3
	cfg.Classifiers.Limits.MaxRules = 7
	cfg.Classifiers.Limits.MaxParallel = 2
	al, host, _ := newClassifierHostLoop(t, cfg)
	want := defaults
	want.ProcessedDays, want.MaxRules, want.MaxParallel = 3, 7, 2
	if got := host.Ceilings(); got != want {
		t.Fatalf("Ceilings = %+v, want %+v", got, want)
	}

	next := *cfg
	next.Classifiers = config.ClassifiersConfig{Limits: config.ClassifierLimitsConfig{MaxRules: 9, MaxItemChars: 1000, MinEverySeconds: 60}}
	if err := al.ReloadProviderAndConfig(context.Background(), &mockProvider{}, &next); err != nil {
		t.Fatalf("reload: %v", err)
	}
	want = defaults
	want.MaxRules, want.MaxItemChars, want.MinEverySeconds = 9, 1000, 60
	if got := host.Ceilings(); got != want {
		t.Fatalf("Ceilings after reload = %+v, want %+v", got, want)
	}
}
