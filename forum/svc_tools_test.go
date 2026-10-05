// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/PivotLLM/toolspec"
	"github.com/google/uuid"
)

// svcToolHost is a fake ToolHost: the agent "denied" has no forum
// permission; files resolve against the workspace and only paths inside it
// are readable.
type svcToolHost struct {
	base, workspace string
	wsErr           error
	remote          bool
}

func (h *svcToolHost) Remote(*toolspec.ToolCall) bool { return h.remote }

func (h *svcToolHost) Scope(call *toolspec.ToolCall) (Scope, error) {
	if call.AgentID == "denied" {
		return Scope{}, errors.New("agent denied may not use the forum tools")
	}
	return Scope{AgentID: call.AgentID, BaseDirectory: h.base}, nil
}

func (h *svcToolHost) ResolveFile(agentID, ref string) (string, error) {
	abs := ref
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(h.workspace, ref)
	}
	if err := h.ReadAllowed(agentID, abs); err != nil {
		return "", err
	}
	return abs, nil
}

func (h *svcToolHost) ReadAllowed(_, abs string) error {
	if !strings.HasPrefix(abs, h.workspace+string(filepath.Separator)) {
		return errors.New("outside the agent's workspace")
	}
	return nil
}

func (h *svcToolHost) Workspace(string) (string, error) { return h.workspace, h.wsErr }

// svcTools is the tool suite over an svcEnv's service.
type svcTools struct {
	t    *testing.T
	e    *svcEnv
	host *svcToolHost
	defs map[string]toolspec.ToolDefinition
}

func svcToolSetup(t *testing.T) *svcTools {
	t.Helper()
	e := svcSetup(t)
	host := &svcToolHost{base: e.scope.BaseDirectory, workspace: e.workspace}
	defs := map[string]toolspec.ToolDefinition{}
	for _, d := range Tools(e.svc, host) {
		defs[d.Name] = d
	}
	return &svcTools{t: t, e: e, host: host, defs: defs}
}

// internal checks that a host failure (errSvcHost) is a tool error with
// the fixed sentence want, the detail being logged, not shown.
func (st *svcTools) internal(name string, args map[string]any, want string) {
	st.t.Helper()
	res, err := st.call(name, args)
	if err != nil || res == nil || !res.IsError || res.ForLLM != want || !errors.Is(res.Err, errSvcHost) {
		st.t.Errorf("%s with a host failure = %+v, %v; want %q", name, res, err, want)
	}
	if !st.e.logger.has(errSvcHost.Error()) {
		st.t.Errorf("%s: the host failure was not logged", name)
	}
}

// call invokes a tool as agent "launcher".
func (st *svcTools) call(name string, args map[string]any) (*toolspec.Result, error) {
	return st.callAs("launcher", name, args)
}

func (st *svcTools) callAs(agent, name string, args map[string]any) (*toolspec.Result, error) {
	st.t.Helper()
	d, ok := st.defs[name]
	if !ok {
		st.t.Fatalf("no tool %q", name)
	}
	if args == nil {
		args = map[string]any{}
	}
	return d.Handler(&toolspec.ToolCall{Ctx: st.t.Context(), Args: args, AgentID: agent, Channel: "test", ChatID: "chat-1"})
}

// ok calls a tool and fails unless it succeeded; it returns ForLLM.
func (st *svcTools) ok(name string, args map[string]any) string {
	st.t.Helper()
	res, err := st.call(name, args)
	if err != nil || res == nil || res.IsError {
		st.t.Fatalf("%s(%v) = %+v, %v", name, args, res, err)
	}
	return res.ForLLM
}

// refused calls a tool and fails unless it returned a tool error whose
// message contains want; it returns the message.
func (st *svcTools) refused(name string, args map[string]any, want string) string {
	st.t.Helper()
	res, err := st.call(name, args)
	if err != nil || res == nil || !res.IsError || !strings.Contains(res.ForLLM, want) {
		st.t.Fatalf("%s(%v) = %+v, %v; want a tool error containing %q", name, args, res, err, want)
	}
	return res.ForLLM
}

// launch launches svcConfigJSON inline through the tool and returns the ID.
func (st *svcTools) launch() string {
	st.t.Helper()
	out := st.ok("launch", map[string]any{"config": svcConfigMap(st.t, svcConfigJSON)})
	id := strings.TrimSuffix(strings.TrimPrefix(out, "Forum "), " launched.")
	if !validForumID(id) {
		st.t.Fatalf("launch said %q", out)
	}
	st.e.ctrls.get(st.t, id)
	return id
}

func svcConfigMap(t *testing.T, cfg string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(cfg), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestSvcToolDefinitions(t *testing.T) {
	st := svcToolSetup(t)
	names := make([]string, 0, len(st.defs))
	for name, d := range st.defs {
		names = append(names, name)
		if d.Handler == nil || d.Category != "forum" || d.Description == "" {
			t.Errorf("tool %s = %+v", name, d)
		}
	}
	slices.Sort(names)
	want := []string{"cancel", "delete", "launch", "models", "pause", "results", "resume", "status", "validate"}
	if !slices.Equal(names, want) {
		t.Errorf("tools = %v, want %v", names, want)
	}
	for _, name := range []string{"pause", "resume", "cancel", "results", "delete"} {
		ps := st.defs[name].Parameters
		if len(ps) != 1 || ps[0].Name != "id" || !ps[0].Required {
			t.Errorf("%s parameters = %+v", name, ps)
		}
	}
}

func TestSvcToolsRequireScope(t *testing.T) {
	st := svcToolSetup(t)
	for name := range st.defs {
		res, err := st.callAs("denied", name, map[string]any{"id": uuid.NewString(), "config": map[string]any{}})
		if err == nil || res != nil {
			t.Errorf("%s as a denied agent = %+v, %v", name, res, err)
		}
	}
}

func TestSvcToolModels(t *testing.T) {
	st := svcToolSetup(t)
	var got []ModelInfo
	if err := json.Unmarshal([]byte(st.ok("models", nil)), &got); err != nil || len(got) != 2 || got[1].Name != "large" {
		t.Errorf("models = %v (%v)", got, err)
	}
	st.e.agents.mu.Lock()
	st.e.agents.models["launcher"] = nil
	st.e.agents.mu.Unlock()
	if out := st.ok("models", nil); out != "[]" {
		t.Errorf("no models = %q, want []", out)
	}
	st.e.agents.mu.Lock()
	st.e.agents.createErr["models"] = errSvcHost
	st.e.agents.mu.Unlock()
	st.internal("models", nil, "The models could not be listed because of an internal error.")
}

func TestSvcToolValidate(t *testing.T) {
	st := svcToolSetup(t)
	file := filepath.Join(st.e.workspace, "forum.json")
	if err := os.WriteFile(file, []byte(svcConfigJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	bad := strings.Replace(svcConfigJSON, `"model": "large"`, `"model": "huge"`, 1)

	if out := st.ok("validate", map[string]any{"config": svcConfigMap(t, svcConfigJSON)}); out != "The configuration is valid." {
		t.Errorf("inline = %q", out)
	}
	if out := st.ok("validate", map[string]any{"config_file": "forum.json"}); out != "The configuration is valid." {
		t.Errorf("file = %q", out)
	}
	if out := st.ok("validate", map[string]any{"config": json.RawMessage(svcConfigJSON)}); out != "The configuration is valid." {
		t.Errorf("raw JSON = %q", out)
	}
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{"both", map[string]any{"config": svcConfigMap(t, svcConfigJSON), "config_file": "forum.json"}, "Give either config or config_file, not both."},
		{"neither", map[string]any{}, "Give the configuration as config"},
		{"nulls are absent", map[string]any{"config": nil, "config_file": nil}, "Give the configuration as config"},
		{"config is a string", map[string]any{"config": svcConfigJSON}, "The config argument must be a JSON object, not a string"},
		{"config is a number", map[string]any{"config": 3.0}, "The config argument must be a JSON object."},
		{"config is raw non-object JSON", map[string]any{"config": json.RawMessage(`[1]`)}, "The config argument must be a JSON object."},
		{"config_file is not a string", map[string]any{"config_file": 7.0}, "The config_file argument must be a string."},
		{"config_file is empty", map[string]any{"config_file": ""}, "The config_file argument is empty."},
		{"config_file outside the workspace", map[string]any{"config_file": "/etc/passwd"}, `The configuration file "/etc/passwd" cannot be used: outside the agent's workspace.`},
		{"config_file missing", map[string]any{"config_file": "missing.json"}, `The configuration file "missing.json" cannot be read`},
		{"invalid configuration", map[string]any{"config": svcConfigMap(t, bad)}, "participants.bob.model"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st.refused("validate", tt.args, tt.want)
		})
	}
	if len(st.e.forumIDs()) != 0 || len(st.e.agents.createdIDs()) != 0 {
		t.Error("validate created something")
	}

	st.host.wsErr = errSvcHost
	st.internal("validate", map[string]any{"config": svcConfigMap(t, svcConfigJSON)}, "The forum could not be validated because of an internal error.")
}

func TestSvcToolLaunch(t *testing.T) {
	st := svcToolSetup(t)
	id := st.launch()
	snap, err := st.e.store(id).ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snap.Origin != (Origin{AgentID: "launcher", Channel: "test", ChatID: "chat-1"}) {
		t.Errorf("origin = %+v", snap.Origin)
	}

	sub := filepath.Join(st.e.workspace, "cfg")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "forum.json"), []byte(svcSimpleJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	out := st.ok("launch", map[string]any{"config_file": "cfg/forum.json"})
	if !strings.HasPrefix(out, "Forum ") || !strings.HasSuffix(out, " launched.") {
		t.Errorf("launch from a file = %q", out)
	}
	// A config_file's relative sources resolve against its own directory.
	withDoc := strings.Replace(svcConfigJSON, `"file": "doc.md"`, `"file": "missing-here.md"`, 1)
	if err := os.WriteFile(filepath.Join(sub, "doc-forum.json"), []byte(withDoc), 0o600); err != nil {
		t.Fatal(err)
	}
	st.refused("launch", map[string]any{"config_file": "cfg/doc-forum.json"}, "sources.doc.file")

	st.refused("launch", map[string]any{}, "Give the configuration")
	st.refused("launch", map[string]any{"config": map[string]any{"version": 1.0}}, "invalid configuration")
	if n := len(st.e.forumIDs()); n != 2 {
		t.Errorf("%d forums, want 2", n)
	}

	st.e.agents.mu.Lock()
	st.e.agents.createErr["clone:bob"] = errSvcHost
	st.e.agents.mu.Unlock()
	st.internal("launch", map[string]any{"config": svcConfigMap(t, svcConfigJSON)}, "The forum could not be launched because of an internal error.")
}

func TestSvcToolStatus(t *testing.T) {
	st := svcToolSetup(t)
	if out := st.ok("status", nil); out != "[]" {
		t.Errorf("no forums = %q", out)
	}
	id := st.launch()
	var list []Summary
	if err := json.Unmarshal([]byte(st.ok("status", map[string]any{"id": ""})), &list); err != nil || len(list) != 1 || list[0].ForumID != id {
		t.Errorf("list = %+v (%v)", list, err)
	}
	var sum Summary
	if err := json.Unmarshal([]byte(st.ok("status", map[string]any{"id": id})), &sum); err != nil || sum.ForumID != id || sum.Status != StatusRunning {
		t.Errorf("summary = %+v (%v)", sum, err)
	}
	missing := uuid.NewString()
	if msg := st.refused("status", map[string]any{"id": missing}, "was not found"); msg != fmt.Sprintf("Forum %s was not found.", missing) {
		t.Errorf("not found = %q", msg)
	}
	st.refused("status", map[string]any{"id": 5.0}, "The id argument must be a string.")
}

func TestSvcToolLifecycle(t *testing.T) {
	st := svcToolSetup(t)
	id := st.launch()
	args := map[string]any{"id": id}
	st.e.running(id)

	if msg := st.refused("delete", args, "pause or cancel it before deleting it"); msg != fmt.Sprintf("Forum %s is running; pause or cancel it before deleting it.", id) {
		t.Errorf("delete running = %q", msg)
	}
	if out := st.ok("pause", args); out != fmt.Sprintf("Forum %s is pausing; it pauses once its active turns finish.", id) {
		t.Errorf("pause = %q", out)
	}
	st.e.settled(id, StatusPaused)
	if out := st.ok("resume", args); out != fmt.Sprintf("Forum %s is running.", id) {
		t.Errorf("resume = %q", out)
	}
	st.e.running(id)

	var res struct {
		Directory string `json:"directory"`
		ForumID   string `json:"forum_id"`
		Complete  bool   `json:"complete"`
	}
	if err := json.Unmarshal([]byte(st.ok("results", args)), &res); err != nil {
		t.Fatal(err)
	}
	if res.Directory != filepath.Join(st.e.scope.BaseDirectory, id) || res.ForumID != id || res.Complete {
		t.Errorf("results = %+v", res)
	}

	if out := st.ok("cancel", args); out != fmt.Sprintf("Forum %s is being cancelled; its partial work is kept.", id) {
		t.Errorf("cancel = %q", out)
	}
	st.e.settled(id, StatusCancelled)
	if msg := st.refused("pause", args, "cannot be paused"); msg != fmt.Sprintf("Forum %s is cancelled and cannot be paused.", id) {
		t.Errorf("pause cancelled = %q", msg)
	}
	st.refused("resume", args, "cannot be resumed")
	if out := st.ok("delete", args); out != fmt.Sprintf("Forum %s is deleted.", id) {
		t.Errorf("delete = %q", out)
	}
	if msg := st.refused("results", args, "was not found"); msg != fmt.Sprintf("Forum %s was not found.", id) {
		t.Errorf("results after delete = %q", msg)
	}

	for _, name := range []string{"pause", "resume", "cancel", "results", "delete"} {
		st.refused(name, nil, "The id argument is required: the forum ID returned by launch.")
		st.refused(name, map[string]any{"id": "  "}, "The id argument is required")
	}
}

func TestSvcToolLockedForum(t *testing.T) {
	st := svcToolSetup(t)
	id := st.launch()
	st.ok("pause", map[string]any{"id": id})
	st.e.settled(id, StatusPaused)
	other := st.e.store(id)
	if err := other.Lock(); err != nil {
		t.Fatal(err)
	}
	defer other.Unlock()
	if msg := st.refused("delete", map[string]any{"id": id}, "in use"); msg != fmt.Sprintf("Forum %s is in use by another process; try again later.", id) {
		t.Errorf("locked = %q", msg)
	}
}

func TestSvcToolError(t *testing.T) {
	id := uuid.NewString()
	agent := uuid.NewString()
	tests := []struct {
		name string
		err  error
		id   string
		tool string
		want string
		// logged, when set, must appear in the log (the detail the agent
		// is not shown).
		logged string
	}{
		{"host failure", errSvcHost, id, "pause", "Forum " + id + " could not be paused because of an internal error.", errSvcHost.Error()},
		{"host failure without an id", fmt.Errorf("open /secret/path: %w", errSvcHost), "", "launch", "The forum could not be launched because of an internal error.", "/secret/path"},
		{"not found", fmt.Errorf("%w: %s", ErrNotFound, id), id, "results", "Forum " + id + " was not found.", ""},
		{"not found without an id", ErrNotFound, "", "status", "The forum was not found.", ""},
		{"locked", ErrLocked, id, "delete", "Forum " + id + " is in use by another process; try again later.", ""},
		{"corrupt", fmt.Errorf("%w: /base/x/output.md digest abc", ErrCorrupt), id, "resume", "Forum " + id + " is damaged and cannot be used.", "/base/x/output.md digest abc"},
		{"state", invalidState("forum %s is completed and cannot be paused", id), id, "pause", "Forum " + id + " is completed and cannot be paused.", ""},
		{"run ended", runEnded(id, "paused"), id, "pause", "Forum " + id + " has stopped running and cannot be paused by this run.", ""},
		{"bare state", fmt.Errorf("append commit: %w: resumed expected at seq 3", ErrInvalidState), id, "resume", "Forum " + id + " cannot be resumed in its current state.", "expected at seq 3"},
		{
			"agents left", fmt.Errorf("wrapped: %w", &agentsLeftError{forumID: id, agents: []string{agent}, err: errSvcHost}), id, "delete",
			"Forum " + id + " was not deleted because its temporary agent " + agent + " could not be deleted; try again later.", errSvcHost.Error(),
		},
		{"closed", errClosed, id, "resume", "Forums cannot be started or changed while the service is shutting down.", ""},
		{"schemas", fmt.Errorf("%w (the configuration names schemas: s)", ErrSchemasUnavailable), "", "launch", "JSON Schema validation is not available (the configuration names schemas: s).", ""},
		{"one whole-document issue", argIssue("give either config or config_file, not both"), "", "launch", "Give either config or config_file, not both.", ""},
		{"issues", &ValidationError{Issues: []Issue{{Path: "a", Message: "x"}, {Path: "b", Message: "y"}}}, "", "validate", "invalid configuration:\na: x\nb: y", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := svcSetup(t)
			ts := &toolSuite{svc: e.svc}
			res, err := ts.fail(tt.err, tt.id, tt.tool)
			if err != nil || res == nil || !res.IsError || res.ForLLM != tt.want || !errors.Is(res.Err, tt.err) {
				t.Fatalf("fail = %+v, %v; want %q", res, err, tt.want)
			}
			if tt.logged != "" && !e.logger.has(tt.logged) {
				t.Errorf("the detail %q was not logged", tt.logged)
			}
		})
	}
}

// svcTurnHost refuses every call as made from inside a forum turn.
type svcTurnHost struct{ svcToolHost }

func (h *svcTurnHost) Scope(*toolspec.ToolCall) (Scope, error) {
	return Scope{}, fmt.Errorf("agent alice is in a forum turn: %w", ErrForumTurn)
}

// Every forum tool is refused inside a forum turn, before the service is
// reached.
func TestSvcToolsRefusedInsideAForumTurn(t *testing.T) {
	e := svcSetup(t)
	host := &svcTurnHost{svcToolHost{base: e.scope.BaseDirectory, workspace: e.workspace}}
	args := map[string]any{"id": uuid.NewString(), "config": map[string]any{"version": 1}}
	defs := Tools(e.svc, host)
	if len(defs) != 9 {
		t.Fatalf("%d tools", len(defs))
	}
	for _, d := range defs {
		res, err := d.Handler(&toolspec.ToolCall{AgentID: "alice", Args: args, Ctx: t.Context()})
		if err != nil || res == nil || !res.IsError || res.ForLLM != "Forum tools are not available inside a forum turn." || !errors.Is(res.Err, ErrForumTurn) {
			t.Errorf("%s inside a forum turn = %+v, %v", d.Name, res, err)
		}
	}
	if ids := e.forumIDs(); len(ids) != 0 {
		t.Errorf("a forum was launched from inside a forum turn: %v", ids)
	}
}

// svcDepthHost refuses every call as made at the maximum sub-agent depth.
type svcDepthHost struct{ svcToolHost }

func (h *svcDepthHost) Scope(*toolspec.ToolCall) (Scope, error) {
	return Scope{}, fmt.Errorf("agent alice at depth 3 of 3: %w", ErrForumDepth)
}

// A call at the maximum sub-agent depth is refused with its own sentence.
func TestSvcToolsRefusedAtMaximumDepth(t *testing.T) {
	e := svcSetup(t)
	host := &svcDepthHost{svcToolHost{base: e.scope.BaseDirectory, workspace: e.workspace}}
	for _, d := range Tools(e.svc, host) {
		res, err := d.Handler(&toolspec.ToolCall{AgentID: "alice", Args: map[string]any{"id": uuid.NewString()}, Ctx: t.Context()})
		if err != nil || res == nil || !res.IsError || res.ForLLM != "Forum tools are not available at the maximum sub-agent depth." || !errors.Is(res.Err, ErrForumDepth) {
			t.Errorf("%s at the maximum depth = %+v, %v", d.Name, res, err)
		}
	}
}
