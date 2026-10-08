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
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/PivotLLM/toolspec"
	"github.com/google/uuid"
)

// svcToolHost is a fake ToolHost: the agent "denied" has no forum
// permission; only paths inside the workspace are readable.
type svcToolHost struct {
	base, workspace string
}

func (h *svcToolHost) Scope(call *toolspec.ToolCall) (Scope, error) {
	if call.AgentID == "denied" {
		return Scope{}, errors.New("agent denied may not use the forum tools")
	}
	return Scope{AgentID: call.AgentID, BaseDirectory: h.base}, nil
}

func (h *svcToolHost) ReadAllowed(_, abs string) error {
	if !strings.HasPrefix(abs, h.workspace+string(filepath.Separator)) {
		return errors.New("outside the agent's workspace")
	}
	return nil
}

// ResolveFile resolves a reference against the workspace; "denied/..." is
// refused as the file tools would refuse it.
func (h *svcToolHost) ResolveFile(_, ref string) (string, error) {
	if strings.HasPrefix(ref, "denied/") {
		return "", errors.New("the agent may not read it or it does not exist")
	}
	if filepath.IsAbs(ref) {
		return ref, nil
	}
	return filepath.Join(h.workspace, ref), nil
}

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

// newForum creates a forum through the tools, imports cfg into it unless
// cfg is empty, and returns its ID.
func (st *svcTools) newForum(cfg string) string {
	st.t.Helper()
	out := st.ok("new", nil)
	id := strings.TrimSuffix(strings.TrimPrefix(out, "Forum "), " created.")
	if !validForumID(id) {
		st.t.Fatalf("new said %q", out)
	}
	if cfg != "" {
		st.ok("config_import", map[string]any{"id": id, "config": svcConfigMap(st.t, cfg)})
	}
	return id
}

// launch launches a new forum of svcConfigJSON through the tools and
// returns the ID.
func (st *svcTools) launch() string {
	st.t.Helper()
	id := st.newForum(svcConfigJSON)
	if out := st.ok("launch", map[string]any{"id": id}); out != svcLaunched(st.e.ref(id), 1) {
		st.t.Fatalf("launch said %q", out)
	}
	st.e.ctrls.get(st.t, id)
	return id
}

// export returns a forum's configuration through config_export.
func (st *svcTools) export(id string) map[string]any {
	st.t.Helper()
	return svcConfigMap(st.t, st.ok("config_export", map[string]any{"id": id}))
}

// svcObject is v as a JSON object; it fails the test for anything else.
func svcObject(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("%v is not a JSON object", v)
	}
	return m
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
	want := []string{
		"cancel", "config_export", "config_import", "config_template", "config_update",
		"delete", "launch", "models", "new", "pause", "readme", "results", "resume", "status", "validate",
	}
	if !slices.Equal(names, want) {
		t.Errorf("tools = %v, want %v", names, want)
	}
	for name, d := range st.defs {
		if refers := strings.HasSuffix(d.Description, ". "+readFirst); refers == (name == "readme") {
			t.Errorf("%s description = %q; every tool but readme ends with %q", name, d.Description, readFirst)
		}
	}
	for _, name := range []string{"config_export", "validate", "launch", "pause", "resume", "cancel", "delete"} {
		ps := st.defs[name].Parameters
		if len(ps) != 1 || ps[0].Name != "id" || !ps[0].Required {
			t.Errorf("%s parameters = %+v", name, ps)
		}
	}
	if ps := st.defs["results"].Parameters; len(ps) != 2 || ps[0].Name != "id" || !ps[0].Required || ps[1].Name != "run" || ps[1].Required || ps[1].Type != "integer" {
		t.Errorf("results parameters = %+v", ps)
	}
	if ps := st.defs["status"].Parameters; len(ps) != 2 || ps[0].Name != "id" || ps[0].Required || ps[1].Name != "run" || ps[1].Required {
		t.Errorf("status parameters = %+v", ps)
	}
	for name, second := range map[string]string{"config_template": "name", "config_import": "config", "config_update": "changes"} {
		ps := st.defs[name].Parameters
		if len(ps) != 2 || ps[0].Name != "id" || ps[1].Name != second || !ps[0].Required || !ps[1].Required {
			t.Errorf("%s parameters = %+v", name, ps)
		}
	}
	if ps := st.defs["new"].Parameters; len(ps) != 0 {
		t.Errorf("new parameters = %+v", ps)
	}
}

func TestSvcToolsRequireScope(t *testing.T) {
	st := svcToolSetup(t)
	for name := range st.defs {
		res, err := st.callAs("denied", name, map[string]any{"id": uuid.NewString()})
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
	id := st.newForum(svcConfigJSON)
	if out := st.ok("validate", map[string]any{"id": id}); out != "The configuration is valid." {
		t.Errorf("valid configuration = %q", out)
	}
	bad := strings.Replace(svcConfigJSON, `"model": "large"`, `"model": "huge"`, 1)
	outside := strings.Replace(svcConfigJSON, `"file": "doc.md"`, `"file": "/etc/passwd"`, 1)
	refused := strings.Replace(svcConfigJSON, `"file": "doc.md"`, `"file": "denied/doc.md"`, 1)
	missing := uuid.NewString()
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{"an empty configuration", map[string]any{"id": st.newForum("")}, "invalid configuration"},
		{"an invalid configuration", map[string]any{"id": st.newForum(bad)}, "participants.bob.model"},
		{"a source the agent may not read", map[string]any{"id": st.newForum(outside)}, "sources.doc.file"},
		{"a source the file tools refuse", map[string]any{"id": st.newForum(refused)}, `"denied/doc.md" cannot be used: the agent may not read it or it does not exist`},
		{"no id", map[string]any{}, "The id argument is required: the forum ID returned by forum_new."},
		{"an unknown forum", map[string]any{"id": missing}, "Forum " + missing + " was not found."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st.refused("validate", tt.args, tt.want)
		})
	}
	if len(st.e.agents.createdIDs()) != 0 {
		t.Error("validate created agents")
	}
	for _, id := range st.e.forumIDs() {
		if st.e.store(id).RunNumber() != 0 {
			t.Errorf("validate launched %s", id)
		}
	}
	// A running forum's configuration can still be validated.
	launched := st.launch()
	if out := st.ok("validate", map[string]any{"id": launched}); out != "The configuration is valid." {
		t.Errorf("validate a running forum = %q", out)
	}
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
	if msg := st.refused("launch", map[string]any{"id": id}, "running"); msg != "Forum "+st.e.ref(id)+" is running; pause or cancel it first." {
		t.Errorf("launch a running forum = %q", msg)
	}

	invalid := st.newForum(`{"version": 1}`)
	st.refused("launch", map[string]any{"id": invalid}, "invalid configuration")
	if st.e.store(invalid).RunNumber() != 0 {
		t.Error("a refused launch left a run")
	}
	st.refused("launch", map[string]any{}, "The id argument is required")

	failing := st.newForum(svcConfigJSON)
	st.e.agents.mu.Lock()
	st.e.agents.createErr["clone:bob"] = errSvcHost
	st.e.agents.mu.Unlock()
	st.internal("launch", map[string]any{"id": failing}, "Forum "+st.e.ref(failing)+" could not be launched because of an internal error.")
	if st.e.store(failing).RunNumber() != 0 {
		t.Error("a failed launch left a run")
	}
}

func TestSvcToolNew(t *testing.T) {
	st := svcToolSetup(t)
	id := st.newForum("")
	if got := st.ok("config_export", map[string]any{"id": id}); got != "{}\n" {
		t.Errorf("a new forum's configuration = %q, want {}", got)
	}
	var sum Summary
	if err := json.Unmarshal([]byte(st.ok("status", map[string]any{"id": id})), &sum); err != nil ||
		sum.Status != StatusNew || sum.ForumID != id || sum.Name != "" || sum.Runs != 0 {
		t.Errorf("status of a new forum = %+v (%v)", sum, err)
	}
	if !slices.Equal(st.e.forumIDs(), []string{id}) || len(st.e.agents.createdIDs()) != 0 {
		t.Error("new created something besides the forum")
	}
	st.refused("new", map[string]any{"name": "x"}, "Unknown argument name; this tool takes no arguments.")
}

// svcStopped cancels a running forum through the tools and waits for it.
func (st *svcTools) stopped(id string) {
	st.t.Helper()
	st.ok("cancel", map[string]any{"id": id})
	st.e.settled(id, StatusCancelled)
}

func TestSvcToolConfigTemplate(t *testing.T) {
	st := svcToolSetup(t)
	id := st.newForum(svcSimpleJSON)
	for _, tpl := range templates {
		if out := st.ok("config_template", map[string]any{"id": id, "name": tpl.name}); out != "Forum "+tpl.name+" ("+id+") now has the "+tpl.name+" template's configuration." {
			t.Errorf("config_template %s = %q", tpl.name, out)
		}
		raw, _ := templateConfig(tpl.name)
		if got := st.export(id); !reflect.DeepEqual(got, svcConfigMap(t, raw)) {
			t.Errorf("after config_template %s the configuration is %v", tpl.name, got)
		}
	}
	st.refused("config_template", map[string]any{"id": id, "name": "debate"}, `There is no template "debate"; use writing or council.`)
	st.refused("config_template", map[string]any{"id": id, "name": 3.0}, "The name argument must be a string.")
	launched := st.launch()
	st.refused("config_template", map[string]any{"id": launched, "name": "council"},
		"Forum "+st.e.ref(launched)+" is running; pause or cancel it first.")
	st.stopped(launched)
	st.ok("config_template", map[string]any{"id": launched, "name": "council"})
}

func TestSvcToolConfigImport(t *testing.T) {
	st := svcToolSetup(t)
	id := st.newForum("")
	if out := st.ok("config_import", map[string]any{"id": id, "config": svcConfigMap(t, svcSimpleJSON)}); out != "Forum svc-simple ("+id+") now has the imported configuration." {
		t.Errorf("config_import = %q", out)
	}
	if got := st.export(id); !reflect.DeepEqual(got, svcConfigMap(t, svcSimpleJSON)) {
		t.Errorf("imported configuration = %v", got)
	}
	// A configuration need not be valid while it is being set up.
	if out := st.ok("config_import", map[string]any{"id": id, "config": json.RawMessage(`{"name": "half done"}`)}); !strings.HasSuffix(out, "imported configuration.") {
		t.Errorf("config_import of raw JSON = %q", out)
	}
	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"a string", map[string]any{"id": id, "config": svcSimpleJSON}, "The config argument must be a JSON object, not a string; pass the object itself."},
		{"an empty string", map[string]any{"id": id, "config": ""}, `Config is empty; pass the config as a JSON object, e.g. {"version":1,"name":"...","brief":{"purpose":"...","task":"..."}}.`},
		{"a number", map[string]any{"id": id, "config": 3.0}, "The config argument must be a JSON object."},
		{"raw non-object JSON", map[string]any{"id": id, "config": json.RawMessage(`[1]`)}, "The config argument must be a JSON object."},
		{"missing", map[string]any{"id": id}, "The config argument is required: a JSON object."},
		{"no id", map[string]any{"config": map[string]any{}}, "The id argument is required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st.refused("config_import", tc.args, tc.want)
		})
	}
	launched := st.launch()
	st.refused("config_import", map[string]any{"id": launched, "config": map[string]any{}},
		"Forum "+st.e.ref(launched)+" is running; pause or cancel it first.")
	st.stopped(launched)
	st.ok("config_import", map[string]any{"id": launched, "config": map[string]any{}})
}

func TestSvcToolConfigUpdate(t *testing.T) {
	st := svcToolSetup(t)
	id := st.newForum("")
	st.ok("config_import", map[string]any{"id": id, "config": json.RawMessage(svcSimpleJSON)})
	changes := map[string]any{
		"name":   "renamed",
		"seed":   7.0,
		"brief":  map[string]any{"task": "Answer at length."},
		"layers": []any{map[string]any{"id": "only"}},
		"participants": map[string]any{
			"bob":    nil,
			"critic": map[string]any{"model": "large", "name": nil},
		},
	}
	if out := st.ok("config_update", map[string]any{"id": id, "changes": changes}); out != "The configuration of forum "+st.e.ref(id)+" is updated." {
		t.Errorf("config_update = %q", out)
	}
	got := st.export(id)
	want := svcConfigMap(t, svcSimpleJSON)
	want["name"] = "renamed"
	want["seed"] = 7.0
	svcObject(t, want["brief"])["task"] = "Answer at length."
	want["layers"] = []any{map[string]any{"id": "only"}}
	delete(svcObject(t, want["participants"]), "bob")
	svcObject(t, want["participants"])["critic"] = map[string]any{"model": "large"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("updated configuration =\n%v\nwant\n%v", got, want)
	}
	// Members keep their order: a merge patch does not reorder the document.
	exported := st.ok("config_export", map[string]any{"id": id})
	if strings.Index(exported, `"version"`) > strings.Index(exported, `"name"`) || strings.Index(exported, `"layers"`) > strings.Index(exported, `"seed"`) {
		t.Errorf("member order changed:\n%s", exported)
	}
	st.refused("config_update", map[string]any{"id": id, "changes": "x"}, "The changes argument must be a JSON object, not a string")
	for _, blank := range []string{"", "    "} {
		if msg := st.refused("config_update", map[string]any{"id": id, "changes": blank}, "empty"); msg != `Changes is empty; pass the changes as a JSON object, e.g. {"sources":{"topic":{"inline":"..."}}}.` {
			t.Errorf("blank changes = %q", msg)
		}
	}
	st.refused("config_update", map[string]any{"id": id}, "The changes argument is required")
	launched := st.launch()
	st.refused("config_update", map[string]any{"id": launched, "changes": map[string]any{"name": "x"}},
		"Forum "+st.e.ref(launched)+" is running; pause or cancel it first.")
	st.ok("pause", map[string]any{"id": launched})
	st.e.settled(launched, StatusPaused)
	st.ok("config_update", map[string]any{"id": launched, "changes": map[string]any{"name": "x"}})
	if got := st.export(launched)["name"]; got != "x" {
		t.Errorf("the paused forum's name after an update = %v", got)
	}
}

func TestSvcToolConfigExport(t *testing.T) {
	st := svcToolSetup(t)
	launched := st.launch()
	if got := st.export(launched); !reflect.DeepEqual(got, svcConfigMap(t, svcConfigJSON)) {
		t.Errorf("a running forum's configuration = %v", got)
	}
	st.ok("cancel", map[string]any{"id": launched})
	st.e.settled(launched, StatusCancelled)
	if got := st.export(launched); !reflect.DeepEqual(got, svcConfigMap(t, svcConfigJSON)) {
		t.Errorf("a finished forum's configuration = %v", got)
	}
	missing := uuid.NewString()
	st.refused("config_export", map[string]any{"id": missing}, "Forum "+missing+" was not found.")
}

// The book use case: a forum set up for chapter 1 is exported, imported
// into a new forum, only the chapter source's file is changed, and the new
// forum is launched on chapter 2.
func TestSvcToolExportImportRoundTrip(t *testing.T) {
	st := svcToolSetup(t)
	if err := os.WriteFile(filepath.Join(st.e.workspace, "chapter2.md"), []byte("# Chapter 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	first := st.launch()
	exported := st.export(first)
	second := st.newForum("")
	st.ok("config_import", map[string]any{"id": second, "config": exported})
	st.ok("config_update", map[string]any{"id": second, "changes": map[string]any{
		"sources": map[string]any{"doc": map[string]any{"file": "chapter2.md"}},
	}})
	st.ok("validate", map[string]any{"id": second})
	if out := st.ok("launch", map[string]any{"id": second}); out != svcLaunched(st.e.ref(second), 1) {
		t.Fatalf("launch = %q", out)
	}
	st.e.ctrls.get(t, second)
	snap, err := st.e.store(second).ReadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	data, err := st.e.store(second).ReadFile(snap.Sources["doc"].File)
	if err != nil || string(data) != "# Chapter 2\n" {
		t.Errorf("the new forum's chapter = %q (%v)", data, err)
	}
	want := svcConfigMap(t, svcConfigJSON)
	svcObject(t, svcObject(t, want["sources"])["doc"])["file"] = "chapter2.md"
	if got := st.export(second); !reflect.DeepEqual(got, want) {
		t.Errorf("the new forum's configuration = %v", got)
	}
	if got := st.export(first); !reflect.DeepEqual(got, svcConfigMap(t, svcConfigJSON)) {
		t.Errorf("the first forum's configuration changed: %v", got)
	}
}

// Another agent cannot reach a forum: its forums live in its own workspace,
// and a forum whose owner record (forum-meta.json) or run names another
// launcher is refused as damaged, whether or not it has run, delete
// included.
func TestSvcToolOwnership(t *testing.T) {
	for _, launched := range []bool{false, true} {
		t.Run(fmt.Sprintf("launched=%v", launched), func(t *testing.T) {
			st := svcToolSetup(t)
			id := st.newForum(svcSimpleJSON)
			if launched {
				if out := st.ok("launch", map[string]any{"id": id}); out != svcLaunched(st.e.ref(id), 1) {
					t.Fatalf("launch = %q", out)
				}
				st.e.ctrls.get(t, id)
				st.stopped(id)
			}
			bobHost := &svcToolHost{base: filepath.Join(t.TempDir(), "forums"), workspace: st.e.workspace}
			bob := map[string]toolspec.ToolDefinition{}
			for _, d := range Tools(st.e.svc, bobHost) {
				bob[d.Name] = d
			}
			calls := map[string]map[string]any{
				"config_template": {"id": id, "name": "council"},
				"config_import":   {"id": id, "config": map[string]any{}},
				"config_update":   {"id": id, "changes": map[string]any{"name": "x"}},
				"config_export":   {"id": id},
				"validate":        {"id": id},
				"launch":          {"id": id},
				"status":          {"id": id},
				"results":         {"id": id},
				"pause":           {"id": id},
				"resume":          {"id": id},
				"cancel":          {"id": id},
				"delete":          {"id": id},
			}
			for name, args := range calls {
				res, err := bob[name].Handler(&toolspec.ToolCall{Ctx: t.Context(), Args: args, AgentID: "bob"})
				want := "Forum " + id + " was not found."
				if err != nil || res == nil || res.ForLLM != want {
					t.Errorf("%s by bob in his own scope = %+v, %v", name, res, err)
				}
			}
			// The same directory reached in bob's name (copied, or the scope
			// misconfigured) names the launcher as its owner.
			for name, args := range calls {
				res, err := st.callAs("bob", name, args)
				if err != nil || res == nil || !res.IsError || res.ForLLM != "Forum "+id+" is damaged and cannot be used." {
					t.Errorf("%s by bob on the launcher's forum = %+v, %v", name, res, err)
				}
			}
			if got := st.export(id); !reflect.DeepEqual(got, svcConfigMap(t, svcSimpleJSON)) {
				t.Errorf("the configuration changed: %v", got)
			}
			if !slices.Equal(st.e.forumIDs(), []string{id}) {
				t.Errorf("forums = %v, want the launcher's forum kept", st.e.forumIDs())
			}
		})
	}
}

func TestSvcToolStatus(t *testing.T) {
	st := svcToolSetup(t)
	if out := st.ok("status", nil); out != "[]" {
		t.Errorf("no forums = %q", out)
	}
	id := st.launch()
	later := st.newForum(`{"name": "later"}`)
	var list []Summary
	if err := json.Unmarshal([]byte(st.ok("status", map[string]any{"id": ""})), &list); err != nil || len(list) != 2 ||
		list[0].ForumID != later || list[0].Status != StatusNew || list[0].Name != "later" || list[1].ForumID != id ||
		list[1].Run != 1 || list[1].Runs != 1 {
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
	ref := "svc-test (" + id + ")"
	st.e.running(id)

	if msg := st.refused("delete", args, "pause or cancel it first"); msg != fmt.Sprintf("Forum %s is running; pause or cancel it first.", ref) {
		t.Errorf("delete running = %q", msg)
	}
	if out := st.ok("pause", args); out != fmt.Sprintf("Forum %s is pausing; it pauses once its active turns finish.", ref) {
		t.Errorf("pause = %q", out)
	}
	st.e.settled(id, StatusPaused)
	if out := st.ok("resume", args); out != fmt.Sprintf("Forum %s is running.", ref) {
		t.Errorf("resume = %q", out)
	}
	st.e.running(id)

	// The fake controller writes no transcript: none is named.
	var res ResultsView
	if err := json.Unmarshal([]byte(st.ok("results", args)), &res); err != nil {
		t.Fatal(err)
	}
	if res.Transcript != "" || res.ForumID != id || res.Run != 1 || res.Complete {
		t.Errorf("results without a transcript = %+v", res)
	}
	if err := st.e.store(id).AppendTranscript("# svc-test · run 1\n\n"); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(st.ok("results", args)), &res); err != nil {
		t.Fatal(err)
	}
	if res.Transcript != "forums/"+id+"/runs/1/transcript.md" {
		t.Errorf("results with a transcript = %+v", res)
	}

	if out := st.ok("cancel", args); out != fmt.Sprintf("Forum %s is being cancelled; its partial work is kept.", ref) {
		t.Errorf("cancel = %q", out)
	}
	st.e.settled(id, StatusCancelled)
	if msg := st.refused("pause", args, "cannot be paused"); msg != fmt.Sprintf("Forum %s is cancelled and cannot be paused.", ref) {
		t.Errorf("pause cancelled = %q", msg)
	}
	st.refused("resume", args, "cannot be resumed")
	if out := st.ok("delete", args); out != fmt.Sprintf("Forum %s is deleted.", ref) {
		t.Errorf("delete = %q", out)
	}
	if msg := st.refused("results", args, "was not found"); msg != fmt.Sprintf("Forum %s was not found.", id) {
		t.Errorf("results after delete = %q", msg)
	}

	for _, name := range []string{"pause", "resume", "cancel", "results", "delete"} {
		st.refused(name, nil, "The id argument is required: the forum ID returned by forum_new.")
		st.refused(name, map[string]any{"id": "  "}, "The id argument is required")
	}

	fresh := st.newForum(svcSimpleJSON)
	for _, name := range []string{"pause", "resume", "cancel", "results"} {
		if msg := st.refused(name, map[string]any{"id": fresh}, "launched"); msg != "Forum svc-simple ("+fresh+") has not been launched." {
			t.Errorf("%s of a new forum = %q", name, msg)
		}
	}
	if out := st.ok("delete", map[string]any{"id": fresh}); out != "Forum svc-simple ("+fresh+") is deleted." {
		t.Errorf("delete a new forum = %q", out)
	}
	if len(st.e.forumIDs()) != 0 {
		t.Errorf("forums left = %v", st.e.forumIDs())
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
	if msg := st.refused("delete", map[string]any{"id": id}, "in use"); msg != fmt.Sprintf("Forum %s is in use by another process; try again later.", st.e.ref(id)) {
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
			"agents left", fmt.Errorf("wrapped: %w", &agentsLeftError{forumID: id, ref: id, agents: []string{agent}, err: errSvcHost}), id, "delete",
			"Forum " + id + " was not deleted because its temporary agent " + agent + " could not be deleted; try again later.", errSvcHost.Error(),
		},
		{"closed", errClosed, id, "resume", "Forums cannot be started or changed while the service is shutting down.", ""},
		{"one whole-document issue", argIssue("the config argument must be a JSON object"), "", "config_import", "The config argument must be a JSON object.", ""},
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
	args := map[string]any{"id": uuid.NewString()}
	defs := Tools(e.svc, host)
	if len(defs) != 15 {
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
		if err != nil || res == nil || !res.IsError || res.ForLLM != "Forum tools are not available at the maximum sub-agent depth." || !errors.Is(res.Err, ErrForumDepth) || !isRefusal(res.Err) {
			t.Errorf("%s at the maximum depth = %+v, %v", d.Name, res, err)
		}
	}
}

// isRefusal mirrors the host's tools.IsRefusal (forum does not import tools).
func isRefusal(err error) bool {
	var r interface{ Refusal() bool }
	return errors.As(err, &r) && r.Refusal()
}

// An argument a tool does not declare is refused in one sentence naming it,
// and the operation does not run.
func TestSvcToolsRefuseUnknownArguments(t *testing.T) {
	st := svcToolSetup(t)
	for _, tc := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"status", map[string]any{"forum_id": "x"}, "Unknown argument forum_id; use id or run."},
		{"results", map[string]any{"forum_id": "x"}, "Unknown argument forum_id; use id or run."},
		{"models", map[string]any{"verbose": true}, "Unknown argument verbose; this tool takes no arguments."},
		{"readme", map[string]any{"name": "council"}, "Unknown argument name; use template."},
		{"validate", map[string]any{"config": "x", "file": "y"}, "Unknown arguments config, file; use id."},
		{"config_update", map[string]any{"id": "x", "patch": "y"}, "Unknown argument patch; use id or changes."},
	} {
		res, err := st.call(tc.tool, tc.args)
		if err != nil || res == nil || !res.IsError || res.ForLLM != tc.want {
			t.Errorf("%s(%v) = %+v, %v; want %q", tc.tool, tc.args, res, err, tc.want)
		}
	}
	// Declared arguments still work.
	if out := st.ok("status", map[string]any{}); !strings.HasPrefix(out, "[") {
		t.Errorf("status without arguments = %q, want the forum list", out)
	}
}

// Without arguments readme returns the guide and the template list; with a
// template its configuration verbatim; an unknown template is one sentence
// naming it and the valid names.
func TestSvcToolReadme(t *testing.T) {
	st := svcToolSetup(t)
	out := st.ok("readme", nil)
	if !strings.HasPrefix(out, "# Forums\n") || !strings.Contains(out, "\n## Templates\n") {
		t.Errorf("guide = %q", out)
	}
	for _, tpl := range templates {
		if !strings.Contains(out, "- `"+tpl.name+"`: "+tpl.description+"\n") {
			t.Errorf("guide does not list template %s", tpl.name)
		}
		want, err := readmeFS.ReadFile("readme/templates/" + tpl.name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		if got := st.ok("readme", map[string]any{"template": tpl.name}); got != string(want) {
			t.Errorf("template %s = %q, want the file verbatim", tpl.name, got)
		}
	}
	st.refused("readme", map[string]any{"template": "debate"}, `There is no template "debate"; use writing or council.`)
	st.refused("readme", map[string]any{"template": 3}, "The template argument must be a string.")
}

// Every template file is listed, and every listed template has a file.
func TestReadmeTemplatesInStep(t *testing.T) {
	entries, err := readmeFS.ReadDir("readme/templates")
	if err != nil {
		t.Fatal(err)
	}
	files := make([]string, 0, len(entries))
	listed := make([]string, 0, len(templates))
	for _, e := range entries {
		files = append(files, strings.TrimSuffix(e.Name(), ".json"))
	}
	for _, tpl := range templates {
		listed = append(listed, tpl.name)
	}
	slices.Sort(files)
	slices.Sort(listed)
	if !slices.Equal(files, listed) {
		t.Errorf("template files %v, listed %v", files, listed)
	}
}

// templateModelPlaceholder matches the model placeholders of the templates.
var templateModelPlaceholder = regexp.MustCompile(`"<(?:a model|model \d) from forum_models>"`)

// templatePlaceholder matches any JSON string of a template that is
// entirely a "<...>" placeholder.
var templatePlaceholder = regexp.MustCompile(`"\s*<[^<>"]+>\s*"`)

// A template passes forum_validate once its placeholders are filled;
// unfilled, validation reports every placeholder, naming every participant
// whose model is one and every source whose file or inline text is one.
func TestReadmeTemplatesValidate(t *testing.T) {
	st := svcToolSetup(t)
	for _, tpl := range templates {
		t.Run(tpl.name, func(t *testing.T) {
			raw, _ := templateConfig(tpl.name)
			cfg, err := Decode([]byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			placeholders := map[string]bool{}
			for id, p := range cfg.Participants {
				if templateModelPlaceholder.MatchString(`"` + p.Model + `"`) {
					placeholders[id] = true
				}
			}
			if len(placeholders) == 0 {
				t.Fatal("the template has no model placeholders")
			}
			id := st.newForum("")
			st.ok("config_template", map[string]any{"id": id, "name": tpl.name})
			msg := st.refused("validate", map[string]any{"id": id}, "participants.")
			if got, want := strings.Count(msg, "is still a placeholder; replace it."), len(templatePlaceholder.FindAllString(raw, -1)); got != want {
				t.Errorf("validation reports %d placeholders, the template has %d:\n%s", got, want, msg)
			}
			fill := map[string]any{}
			for pid := range placeholders {
				if !strings.Contains(msg, "participants."+pid+".model: is still a placeholder") {
					t.Errorf("validation does not name participant %s:\n%s", pid, msg)
				}
				fill[pid] = map[string]any{"model": "default"}
			}
			sources := map[string]any{}
			for sid, src := range cfg.Sources {
				var inline string // stays "" for a file source
				if src.Inline != nil && json.Unmarshal(src.Inline, &inline) != nil {
					t.Fatalf("source %s: inline is not a string", sid)
				}
				switch {
				case isPlaceholder(src.File):
					if !strings.Contains(msg, "sources."+sid+".file: is still a placeholder") {
						t.Errorf("validation does not name source %s:\n%s", sid, msg)
					}
					file := sid + ".md"
					if err := os.WriteFile(filepath.Join(st.e.workspace, file), []byte("Alice and Bob.\n"), 0o600); err != nil {
						t.Fatal(err)
					}
					sources[sid] = map[string]any{"file": file}
				case isPlaceholder(inline):
					if !strings.Contains(msg, "sources."+sid+".inline: is still a placeholder") {
						t.Errorf("validation does not name source %s:\n%s", sid, msg)
					}
					sources[sid] = map[string]any{"inline": "Should Alice or Bob <b>chair</b> the meeting, given a<b?"}
				}
			}
			st.ok("config_update", map[string]any{"id": id, "changes": map[string]any{"participants": fill, "sources": sources}})
			if out := st.ok("validate", map[string]any{"id": id}); out != "The configuration is valid." {
				t.Errorf("filled template: %s", out)
			}
		})
	}
}

// A refusal the agent can act on is marked as one (the host logs it as a
// warning); a damaged forum and an internal failure are not.
func TestSvcToolRefusalsAreMarked(t *testing.T) {
	st := svcToolSetup(t)
	missing := uuid.NewString()
	for name, args := range map[string]map[string]any{
		"status":        {"id": missing},
		"validate":      {"id": st.newForum("")},
		"config_update": {"id": missing, "changes": ""},
	} {
		res, err := st.call(name, args)
		if err != nil || res == nil || !res.IsError || !isRefusal(res.Err) {
			t.Errorf("%s = %+v, %v; want a refusal", name, res, err)
		}
	}
	for _, err := range []error{ErrCorrupt, errSvcHost} {
		if expectedFailure(err) {
			t.Errorf("%v counts as a refusal", err)
		}
	}
}
