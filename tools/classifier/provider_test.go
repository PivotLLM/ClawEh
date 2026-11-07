// ClawEh
// License: MIT

package classifier

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	classifierpkg "github.com/PivotLLM/ClawEh/classifier"
	"github.com/PivotLLM/ClawEh/classifier/classifierfs"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/tools"
)

// toolNames are the eleven classifier tools, as the provider names them
// (the "classifier_" prefix is added when they are mounted).
var toolNames = []string{
	"readme", "list", "get", "create", "update", "delete",
	"validate", "status", "test", "rerun", "activity",
}

// scopeConfig has Alice with the classifiers switch on and Bob without it.
func scopeConfig() *config.Config {
	c := &config.Config{}
	c.Agents.List = []config.AgentConfig{
		{ID: "Alice", Classifiers: &config.AgentClassifiersConfig{Enabled: true, Model: "m"}},
		{ID: "bob", Classifiers: &config.AgentClassifiersConfig{Enabled: false, Model: "m"}},
	}
	return c
}

// nopHost lets the service exist; the provider tests never run a classifier.
type nopHost struct{}

func (nopHost) Has(string, string) error { return nil }
func (nopHost) Call(context.Context, string, string, map[string]any) (string, error) {
	return "[]", nil
}

func (nopHost) Complete(context.Context, string, string, string) (string, string, error) {
	return "", "", classifierpkg.ErrNoModel
}
func (nopHost) Debugf(string, ...any) {}
func (nopHost) Infof(string, ...any)  {}
func (nopHost) Warnf(string, ...any)  {}
func (nopHost) Errorf(string, ...any) {}

func withService(t *testing.T) {
	t.Helper()
	svc := classifierpkg.New(classifierpkg.Host{Tools: nopHost{}, Model: nopHost{}, Logger: nopHost{}})
	SetService(svc)
	t.Cleanup(func() {
		if err := svc.Close(context.Background()); err != nil {
			t.Errorf("Close: %v", err)
		}
		SetService(nil)
	})
}

// Scope is the agent and <workspace>/classifiers; it is refused for an
// agent without the switch and for a forum participant.
func TestToolHostScope(t *testing.T) {
	ws := t.TempDir()
	alice := &toolHost{cfg: scopeConfig(), agentID: "alice", workspace: ws}
	got, err := alice.Scope(&global.ToolCall{})
	want := classifierpkg.Scope{AgentID: "alice", BaseDirectory: filepath.Join(ws, classifierfs.BaseDirName)}
	if err != nil || got != want {
		t.Fatalf("Scope = %+v, %v; want %+v", got, err, want)
	}
	clone := &toolHost{cfg: scopeConfig(), agentID: "alice", workspace: ws, clone: "1a2b3c4d"}
	if got, err := clone.Scope(&global.ToolCall{}); err != nil || got.AgentID != "alice" || got.Clone != "1a2b3c4d" {
		t.Errorf("Scope of a clone = %+v, %v; want alice with the clone named", got, err)
	}
	bob := &toolHost{cfg: scopeConfig(), agentID: "bob", workspace: ws}
	if _, err := bob.Scope(&global.ToolCall{}); err == nil {
		t.Error("Scope of an agent with classifiers off succeeded")
	}
	participant := &toolHost{cfg: scopeConfig(), agentID: "alice", workspace: ws, purpose: tools.TempPurposeForum}
	if _, err := participant.Scope(&global.ToolCall{}); err == nil {
		t.Error("Scope of a forum participant succeeded")
	}
}

// The provider registers the eleven tools for an agent with the switch on,
// and nothing without the switch, without a service, without a workspace or
// for a forum participant.
func TestProviderGating(t *testing.T) {
	cfg := scopeConfig()
	deps := func(id, purpose, ws string) global.Deps {
		return global.Deps{Cfg: cfg, AgentID: id, Host: tools.ToolDeps{Workspace: ws, TempPurpose: purpose}}
	}
	ws := t.TempDir()

	SetService(nil)
	if defs := GlobalProvider.RegisterTools(deps("alice", "", ws)); len(defs) != 0 {
		t.Fatalf("tools without a service: %d", len(defs))
	}
	withService(t)

	defs := GlobalProvider.RegisterTools(deps("alice", "", ws))
	names := make([]string, 0, len(defs))
	for _, d := range defs {
		names = append(names, d.Name)
	}
	slices.Sort(names)
	wantNames := slices.Clone(toolNames)
	slices.Sort(wantNames)
	if !slices.Equal(names, wantNames) {
		t.Fatalf("alice (classifiers on) got %v, want %v", names, wantNames)
	}
	for _, tc := range []struct {
		what string
		deps global.Deps
	}{
		{"switch off", deps("bob", "", ws)},
		{"forum participant", deps("alice", tools.TempPurposeForum, ws)},
		{"no workspace", deps("alice", "", "")},
		{"enumeration (no config)", global.Deps{AgentID: "alice"}},
	} {
		if defs := GlobalProvider.RegisterTools(tc.deps); len(defs) != 0 {
			t.Errorf("%s: got %d tools, want none", tc.what, len(defs))
		}
	}
}

// Mounted as the gateway mounts it, the suite is gated by `classifiers`
// and its tools are named classifier_*.
func TestMountedSuite(t *testing.T) {
	withService(t)
	cfg := scopeConfig()
	p := tools.NamespacedProvider(Namespace, GlobalProvider)
	sp, ok := p.(tools.SuiteProvider)
	if Namespace+"_" != classifierpkg.ToolPrefix {
		t.Fatalf("tools are mounted as %s_*, the core refuses %s*", Namespace, classifierpkg.ToolPrefix)
	}
	if !ok || sp.Suite() != Suite || Suite != "classifiers" {
		t.Fatalf("suite = %v (%v), want classifiers", sp, ok)
	}
	built := p.Build(tools.ToolDeps{Cfg: cfg, AgentID: "alice", Workspace: t.TempDir()})
	if len(built) != len(toolNames) {
		t.Fatalf("built %d tools, want %d", len(built), len(toolNames))
	}
	for _, tl := range built {
		if bare, ok := strings.CutPrefix(tl.Name(), "classifier_"); !ok || !slices.Contains(toolNames, bare) {
			t.Errorf("tool %s is not a classifier_* tool", tl.Name())
		}
	}
	if built := p.Build(tools.ToolDeps{Cfg: cfg, AgentID: "bob", Workspace: t.TempDir()}); len(built) != 0 {
		t.Errorf("bob (classifiers off) built %d tools", len(built))
	}
}
