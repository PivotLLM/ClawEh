// ClawEh
// License: MIT

package forum

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/config"
	forumpkg "github.com/PivotLLM/ClawEh/forum"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/tools"
	toolsagents "github.com/PivotLLM/ClawEh/tools/agents"
	"github.com/PivotLLM/ClawEh/tools/files"
)

func scopeConfig() *config.Config {
	c := &config.Config{}
	c.Agents.Defaults.MaxSubagentDepth = 3
	c.Agents.List = []config.AgentConfig{{ID: "alice", Forum: true}, {ID: "bob"}}
	return c
}

func callAt(depth int) *global.ToolCall {
	return &global.ToolCall{Ctx: toolsagents.WithSpawnDepth(context.Background(), depth)}
}

// Scope is the agent and <workspace>/forums, and is refused inside a forum
// turn (a forum participant, or the maximum sub-agent depth) and for an
// agent without the switch.
func TestToolHostScope(t *testing.T) {
	ws := t.TempDir()
	alice := &toolHost{cfg: scopeConfig(), agentID: "alice", workspace: ws}

	got, err := alice.Scope(callAt(2))
	if err != nil || got != (forumpkg.Scope{AgentID: "alice", BaseDirectory: filepath.Join(ws, BaseDirName)}) {
		t.Fatalf("Scope below the maximum depth = %+v, %v", got, err)
	}
	if _, err := alice.Scope(callAt(3)); !errors.Is(err, forumpkg.ErrForumDepth) {
		t.Errorf("Scope at the maximum depth = %v, want ErrForumDepth", err)
	}
	participant := &toolHost{cfg: scopeConfig(), agentID: "alice", workspace: ws, purpose: tools.TempPurposeForum}
	if _, err := participant.Scope(callAt(0)); !errors.Is(err, forumpkg.ErrForumTurn) {
		t.Errorf("Scope of a forum participant = %v, want ErrForumTurn", err)
	}
	bob := &toolHost{cfg: scopeConfig(), agentID: "bob", workspace: ws}
	if _, err := bob.Scope(callAt(0)); err == nil || errors.Is(err, forumpkg.ErrForumTurn) {
		t.Errorf("Scope of an agent with forum off = %v, want a plain refusal", err)
	}
}

// The provider registers nothing without the switch or without a service.
func TestProviderGating(t *testing.T) {
	cfg := scopeConfig()
	deps := func(id string) global.Deps {
		return global.Deps{Cfg: cfg, AgentID: id, Host: tools.ToolDeps{Workspace: t.TempDir()}}
	}
	SetService(nil)
	if defs := GlobalProvider.RegisterTools(deps("alice")); len(defs) != 0 {
		t.Fatalf("tools without a service: %d", len(defs))
	}
	svc := forumpkg.New(forumpkg.Host{Messenger: nopHost{}, Agents: nopHost{}, Notifier: nopHost{}, Logger: nopHost{}})
	t.Cleanup(func() {
		if err := svc.Close(context.Background()); err != nil {
			t.Errorf("Close: %v", err)
		}
		SetService(nil)
	})
	SetService(svc)
	if defs := GlobalProvider.RegisterTools(deps("alice")); len(defs) != 15 {
		t.Fatalf("alice (forum on) got %d tools, want 15", len(defs))
	}
	if defs := GlobalProvider.RegisterTools(deps("bob")); len(defs) != 0 {
		t.Fatalf("bob (forum off) got %d tools", len(defs))
	}
	participant := global.Deps{Cfg: cfg, AgentID: "alice", Host: tools.ToolDeps{Workspace: t.TempDir(), TempPurpose: tools.TempPurposeForum}}
	if defs := GlobalProvider.RegisterTools(participant); len(defs) != 0 {
		t.Fatalf("a forum participant acting as alice got %d tools", len(defs))
	}
	if defs := GlobalProvider.RegisterTools(global.Deps{}); len(defs) != 0 {
		t.Fatalf("enumeration pass listed %d tools", len(defs))
	}
}

// ReadAllowed follows the agent's file permissions, and the host answers
// only for the agent it was built for.
func TestToolHostFiles(t *testing.T) {
	ws := t.TempDir()
	cfg := scopeConfig()
	cfg.Agents.Defaults.RestrictToWorkspace = true
	inside := filepath.Join(ws, "files", "forum.json")
	if err := os.MkdirAll(filepath.Dir(inside), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inside, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret.json")
	if err := os.WriteFile(outside, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &toolHost{cfg: cfg, agentID: "alice", workspace: ws}
	if err := h.ReadAllowed("alice", inside); err != nil {
		t.Errorf("ReadAllowed(inside) = %v", err)
	}
	if err := h.ReadAllowed("alice", filepath.Join(ws, "files", "missing.md")); err != nil {
		t.Errorf("ReadAllowed(missing) = %v, want nil: the forum reports a missing file itself", err)
	}
	if err := h.ReadAllowed("alice", outside); err == nil {
		t.Error("ReadAllowed accepted a file outside the workspace")
	}
	if _, err := h.ResolveFile("bob", "files/forum.json"); err == nil {
		t.Error("the host answered for another agent")
	}
}

// nopHost satisfies the forum host interfaces for a service that runs
// nothing.
type nopHost struct{}

func (nopHost) Ask(context.Context, string, string, time.Duration) (forumpkg.Reply, error) {
	return forumpkg.Reply{}, errors.New("unused")
}
func (nopHost) Exists(context.Context, string) (bool, error)                 { return false, nil }
func (nopHost) MayTarget(context.Context, string, string) (bool, error)      { return false, nil }
func (nopHost) Models(context.Context, string) ([]forumpkg.ModelInfo, error) { return nil, nil }
func (nopHost) CreateClone(context.Context, forumpkg.CloneSpec) (string, error) {
	return "", errors.New("unused")
}

func (nopHost) CreateFresh(context.Context, forumpkg.FreshSpec) (string, error) {
	return "", errors.New("unused")
}
func (nopHost) Delete(context.Context, string, string) error { return nil }
func (nopHost) Touch(context.Context, string, string) error  { return nil }
func (nopHost) ForumFinished(context.Context, forumpkg.Origin, *forumpkg.Result) error {
	return nil
}
func (nopHost) Debugf(string, ...any) {}
func (nopHost) Infof(string, ...any)  {}
func (nopHost) Warnf(string, ...any)  {}
func (nopHost) Errorf(string, ...any) {}

// modelsHost is nopHost whose agents all have the model "m".
type modelsHost struct{ nopHost }

func (modelsHost) Models(context.Context, string) ([]forumpkg.ModelInfo, error) {
	return []forumpkg.ModelInfo{{Name: "m"}}, nil
}

// A source file resolves exactly as the agent's file tools read it: from
// the workspace, a configured mount and the maestro mount, and a mount wins
// over a workspace folder of the same name just as it does for the tools.
func TestSourcesResolveLikeTheFileTools(t *testing.T) {
	ws, docsMount, maestroMount := t.TempDir(), t.TempDir(), t.TempDir()
	write := func(p, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(ws, "files", "brief.md"), "workspace brief")
	write(filepath.Join(ws, "docs", "chapter.md"), "workspace folder")
	write(filepath.Join(docsMount, "chapter.md"), "mounted chapter")
	write(filepath.Join(maestroMount, "notes.md"), "maestro notes")
	files.SetMountsForWorkspace(ws, []files.MountSpec{
		{Name: "docs", Path: docsMount},
		{Name: config.MaestroMountName, Path: maestroMount},
	})
	t.Cleanup(func() { files.SetMountsForWorkspace(ws, nil) })
	cfg := scopeConfig()
	cfg.Agents.Defaults.RestrictToWorkspace = true
	h := &toolHost{cfg: cfg, agentID: "alice", workspace: ws}

	refs := map[string]string{"brief": "files/brief.md", "chapter": "docs/chapter.md", "notes": "maestro/notes.md"}
	raw := `{"version": 1, "name": "mounts",
	  "brief": {"purpose": "p", "task": "t"},
	  "sources": {
	    "brief":   {"decode": "markdown", "file": "files/brief.md"},
	    "chapter": {"decode": "markdown", "file": "docs/chapter.md"},
	    "notes":   {"decode": "markdown", "file": "maestro/notes.md"}},
	  "participants": {"reader": {"model": "m"}},
	  "limits": {"max_calls": 2, "max_duration_seconds": 60, "call_timeout_seconds": 30,
	    "max_attempts_per_turn": 1, "max_parallel_calls": 1},
	  "layers": [{"id": "read", "participants": ["reader"], "instructions": "Read.",
	    "inputs": [{"from": "source:brief"}, {"from": "source:chapter"}, {"from": "source:notes"}],
	    "delivery": "after_round", "max_rounds": 1, "output": {"format": "text"}}]}`
	fc, err := forumpkg.Decode([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if err = forumpkg.ValidateStatic(fc); err != nil {
		t.Fatal(err)
	}
	res, err := forumpkg.Preflight(context.Background(), fc, forumpkg.PreflightEnv{
		Launcher:    "alice",
		Agents:      modelsHost{},
		Schemas:     forumpkg.JSONSchemaValidator{},
		ResolveFile: func(ref string) (string, error) { return h.ResolveFile("alice", ref) },
		ReadAllowed: func(abs string) error { return h.ReadAllowed("alice", abs) },
	})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	reader := files.NewReader(cfg, ws)
	for id, ref := range refs {
		want, err := reader.ReadFile(ref)
		if err != nil {
			t.Fatalf("file tools reading %s: %v", ref, err)
		}
		if got := res.SourceContents[id]; string(got) != string(want) {
			t.Errorf("source %s (%s) = %q, the file tools read %q", id, ref, got, want)
		}
	}
	if got := string(res.SourceContents["chapter"]); got != "mounted chapter" {
		t.Errorf("docs/chapter.md = %q, want the mount's file", got)
	}
}
