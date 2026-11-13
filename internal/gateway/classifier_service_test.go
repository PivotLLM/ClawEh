// ClawEh
// License: MIT

package gateway

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/agent"
	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/classifier/classifierfs"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/tools"
	toolsclassifier "github.com/PivotLLM/ClawEh/tools/classifier"
)

// listTool is a source tool that lists no items.
type listTool struct{}

func (listTool) Name() string               { return "probe_list" }
func (listTool) Description() string        { return "lists nothing" }
func (listTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (listTool) Execute(context.Context, map[string]any) *tools.ToolResult {
	return tools.NewToolResult(`{"items":[]}`)
}

// newClassifierService wires the service to its host: it is installed for
// the classifier tools before the loop exists (so the agent whose switch is
// on gets them as the loop builds its tools, and the other does not), and
// once the host is bound the service reads the loop's agents (scopes and
// tools), its maximums (classifiers.limits) and its modes (the agents area)
// from the live configuration.
func TestNewClassifierService(t *testing.T) {
	RegisterToolProvidersForTest()
	t.Cleanup(func() { toolsclassifier.SetService(nil) })

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			BaseDir:  t.TempDir(),
			Defaults: config.AgentDefaults{Models: []string{"m"}, MaxTokens: 4096, MaxToolIterations: 10},
			List: []config.AgentConfig{
				{
					ID: "alice", Name: "Alice", Default: true, Models: []string{"m"},
					Classifiers: &config.AgentClassifiersConfig{Enabled: true, Model: "m"},
				},
				{ID: "bob", Name: "Bob"},
			},
		},
		Providers: []config.Provider{{Name: "p", Protocol: "openai-chat", BaseURL: "http://127.0.0.1:0/v1", APIKey: "k"}},
		Models:    []config.ModelConfig{{ModelName: "m", Model: "m-wire", Provider: "p", Enabled: true}},
	}
	cfg.Classifiers.Limits.MaxRules = 1
	cfg.Security.Permissions.Agents = config.AreaPermissions{DirMode: "0750", FileMode: "0640"}

	host := agent.NewClassifierHost()
	svc := newClassifierService(host)
	t.Cleanup(func() { closeClassifiers(svc) })

	al, loopErr := agent.NewAgentLoop(cfg, bus.NewMessageBus(), providers.NewUnconfiguredProvider(), nil)
	if loopErr != nil {
		t.Fatalf("NewAgentLoop: %v", loopErr)
	}
	t.Cleanup(al.GetRegistry().Close)
	alice, _ := al.GetRegistry().GetConfigured("alice")
	bob, _ := al.GetRegistry().GetConfigured("bob")
	if _, ok := alice.Tools.Get("classifier_create"); !ok {
		t.Fatal("alice (switch on) has no classifier_create: the service was not installed before the loop built its tools")
	}
	if _, ok := bob.Tools.Get("classifier_create"); ok {
		t.Fatal("bob (switch off) has classifier_create")
	}
	alice.Tools.Register(listTool{})

	host.Bind(al)
	scopes := host.Scopes()
	want := filepath.Join(alice.Workspace, classifierfs.BaseDirName)
	if len(scopes) != 1 || scopes[0].AgentID != "alice" || scopes[0].BaseDirectory != want {
		t.Fatalf("Scopes = %+v, want alice at %s", scopes, want)
	}
	scope := scopes[0]

	def := func(rules ...string) []byte {
		b, err := json.Marshal(map[string]any{
			"id": "inbox", "enabled": true, "trigger": map[string]any{"every_seconds": 60},
			"source":   map[string]any{"tool": "probe_list", "items": "items", "id": "id"},
			"classify": map[string]any{"instructions": "Sort.", "rules": rules, "categories": []any{map[string]any{"name": "keep"}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	err := svc.Validate(context.Background(), scope, def("one", "two"), false)
	if err == nil || !strings.Contains(err.Error(), "classify.rules has 2 rules; this install allows 1.") {
		t.Fatalf("Validate above classifiers.limits.max_rules = %v", err)
	}
	if _, err = svc.Create(context.Background(), scope, def("one")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	for path, mode := range map[string]os.FileMode{
		scope.BaseDirectory: 0o750,
		filepath.Join(scope.BaseDirectory, "inbox"+classifierfs.DefinitionExt): 0o640,
	} {
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("stat %s: %v", path, statErr)
		}
		if got := info.Mode().Perm(); got != mode {
			t.Errorf("%s mode = %o, want %o (the agents area)", path, got, mode)
		}
	}
	sums, err := svc.List(scope)
	if err != nil || len(sums) != 1 || sums[0].ID != "inbox" || sums[0].Owner != "alice" {
		t.Fatalf("List = %+v, %v; want inbox owned by alice", sums, err)
	}

	// A tool's error is redacted with the host's redaction before it is
	// shown (here a live validation's refusal).
	const key = "sk-live-0123456789abcdefghij"
	alice.Tools.Register(secretTool{msg: "401 Unauthorized: api_key " + key})
	secret, err := json.Marshal(map[string]any{
		"id": "secret", "enabled": true, "trigger": map[string]any{"every_seconds": 60},
		"source":   map[string]any{"tool": "probe_secret", "items": "items", "id": "id"},
		"classify": map[string]any{"instructions": "Sort.", "categories": []any{map[string]any{"name": "keep"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = svc.Validate(context.Background(), scope, secret, true)
	if err == nil || strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), "source probe_secret failed: 401 Unauthorized") {
		t.Errorf("Validate with a failing source = %v, want the error with the key redacted", err)
	}
}

// secretTool is a source tool whose error carries a credential.
type secretTool struct{ msg string }

func (secretTool) Name() string               { return "probe_secret" }
func (secretTool) Description() string        { return "fails" }
func (secretTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (s secretTool) Execute(context.Context, map[string]any) *tools.ToolResult {
	return tools.ErrorResult(s.msg)
}
