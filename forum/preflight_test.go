// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPreflightExample runs the spec example through the whole check path:
// decodeConfig, validateStatic, runPreflight.
func TestPreflightExample(t *testing.T) {
	cfg, err := decodeConfig([]byte(cfgtExampleJSON))
	if err != nil {
		t.Fatal(err)
	}
	if err = validateStatic(cfg); err != nil {
		t.Fatal(err)
	}
	agents := cfgtNewAgents()
	res, err := runPreflight(context.Background(), cfg, cfgtEnv(agents))
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if len(res.Models) != 2 || res.Models["chair"] != "default" || res.Models["editor"] != "default" {
		t.Errorf("Models = %v, want chair and editor on default", res.Models)
	}
	if _, ok := res.Schemas["findings"]; !ok || len(res.Schemas) != 1 {
		t.Errorf("Schemas = %v", res.Schemas)
	}
	if _, ok := res.ModeratorSchemas["debate"]; !ok || len(res.ModeratorSchemas) != 1 {
		t.Errorf("ModeratorSchemas = %v", res.ModeratorSchemas)
	}
	if len(res.SourceContents) != 0 {
		t.Errorf("SourceContents = %v, want none (inline source)", res.SourceContents)
	}
	for _, call := range []string{"MayTarget launcher alice", "Exists alice", "MayTarget launcher bob", "Exists bob", "Models launcher"} {
		if !agents.called(call) {
			t.Errorf("Preflight did not call %s", call)
		}
	}
	if agents.called("Models bob") {
		t.Error("a clone without a model override needs no model lookup")
	}
}

func TestPreflightRejects(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(c *Config, a *cfgtAgents, env *preflightEnv)
		path   string
		want   []string
	}{
		{
			"existing agent not allowed", func(_ *Config, a *cfgtAgents, _ *preflightEnv) { delete(a.allowed, "alice") },
			"participants.alice.agent",
			[]string{`"alice"`, "may not use"},
		},
		{
			"existing agent missing", func(_ *Config, a *cfgtAgents, _ *preflightEnv) { delete(a.exists, "alice") },
			"participants.alice.agent",
			[]string{`"alice"`, "does not exist"},
		},
		{
			"clone source not allowed", func(_ *Config, a *cfgtAgents, _ *preflightEnv) { delete(a.allowed, "bob") },
			"participants.bob.clone",
			[]string{`"bob"`, "may not use"},
		},
		{
			"clone source missing", func(_ *Config, a *cfgtAgents, _ *preflightEnv) { delete(a.exists, "bob") },
			"participants.bob.clone",
			[]string{`"bob"`, "does not exist"},
		},
		{"clone model not the source's", func(c *Config, a *cfgtAgents, _ *preflightEnv) {
			c.Participants["bob"] = Participant{Clone: "bob", Model: "huge"}
			a.models["bob"] = []ModelInfo{{Name: "small"}}
		}, "participants.bob.model", []string{`"huge"`, `agent "bob"`, "small"}},
		{"clone model the launcher's only", func(c *Config, a *cfgtAgents, _ *preflightEnv) {
			c.Participants["bob"] = Participant{Clone: "bob", Model: "large"}
			a.models["bob"] = nil
		}, "participants.bob.model", []string{`"large"`, "none"}},
		{"fresh model unknown", func(c *Config, _ *cfgtAgents, _ *preflightEnv) {
			c.Participants["editor"] = Participant{Model: "gpt-x"}
		}, "participants.editor.model", []string{`"gpt-x"`, "launching agent", "default, large"}},
		{"fresh moderator model unknown", func(c *Config, _ *cfgtAgents, _ *preflightEnv) {
			c.Participants["chair"] = Participant{Model: "gpt-x"}
		}, "participants.chair.model", []string{`"gpt-x"`}},
		{"schema does not compile", func(c *Config, _ *cfgtAgents, _ *preflightEnv) {
			c.Schemas["findings"] = cfgtRaw(`{"type":7}`)
		}, "schemas.findings", []string{`"findings"`}},
		{"schema references outside itself", func(c *Config, _ *cfgtAgents, _ *preflightEnv) {
			c.Schemas["findings"] = cfgtRaw(`{"$ref":"https://example.com/s.json"}`)
		}, "schemas.findings", []string{"only references inside the schema"}},
		{"unused schema still compiles", func(c *Config, _ *cfgtAgents, _ *preflightEnv) {
			c.Schemas["spare"] = cfgtRaw(`{"minLength":"x"}`)
		}, "schemas.spare", []string{`"spare"`}},
		{
			"max_calls above ceiling", func(_ *Config, _ *cfgtAgents, env *preflightEnv) { env.HostLimits.MaxCalls = 29 },
			"limits.max_calls",
			[]string{"30", "host ceiling of 29"},
		},
		{
			"duration above ceiling", func(_ *Config, _ *cfgtAgents, env *preflightEnv) { env.HostLimits.MaxDurationSeconds = 600 },
			"limits.max_duration_seconds",
			[]string{"1800", "600"},
		},
		{
			"call timeout above ceiling", func(_ *Config, _ *cfgtAgents, env *preflightEnv) { env.HostLimits.CallTimeoutSeconds = 60 },
			"limits.call_timeout_seconds",
			[]string{"host ceiling of 60"},
		},
		{
			"attempts above ceiling", func(_ *Config, _ *cfgtAgents, env *preflightEnv) { env.HostLimits.MaxAttemptsPerTurn = 1 },
			"limits.max_attempts_per_turn",
			[]string{"host ceiling of 1"},
		},
		{
			"parallel above ceiling", func(_ *Config, _ *cfgtAgents, env *preflightEnv) { env.HostLimits.MaxParallelCalls = 1 },
			"limits.max_parallel_calls",
			[]string{"host ceiling of 1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := cfgtExample(t)
			agents := cfgtNewAgents()
			env := cfgtEnv(agents)
			tt.mutate(cfg, agents, &env)
			if err := validateStatic(cfg); err != nil {
				t.Fatalf("the mutated configuration must pass ValidateStatic: %v", err)
			}
			res, err := runPreflight(context.Background(), cfg, env)
			if res != nil {
				t.Error("Preflight returned a result alongside issues")
			}
			cfgtWantIssue(t, err, tt.path, tt.want...)
		})
	}
}

func TestPreflightDoesNotProbeForbiddenAgents(t *testing.T) {
	agents := cfgtNewAgents()
	delete(agents.allowed, "alice")
	_, err := runPreflight(context.Background(), cfgtExample(t), cfgtEnv(agents))
	cfgtWantIssue(t, err, "participants.alice.agent", "may not use")
	if agents.called("Exists alice") {
		t.Error("existence of an agent the launcher may not use must not be checked (or revealed)")
	}
}

func TestPreflightAccepts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(c *Config, a *cfgtAgents, env *preflightEnv)
		check  func(t *testing.T, r *resolvedConfig)
	}{
		{"clone model override recorded", func(c *Config, _ *cfgtAgents, _ *preflightEnv) {
			c.Participants["bob"] = Participant{Clone: "bob", Model: "large"}
		}, func(t *testing.T, r *resolvedConfig) {
			t.Helper()
			if r.Models["bob"] != "large" {
				t.Errorf("Models[bob] = %q", r.Models["bob"])
			}
			if _, ok := r.Models["alice"]; ok {
				t.Error("an existing agent has no forum model")
			}
		}},
		{"participants of disabled layers are not checked", func(c *Config, _ *cfgtAgents, _ *preflightEnv) {
			c.Participants["ghost"] = Participant{Agent: "ghost"}
			c.Participants["spare"] = Participant{Model: "gpt-x"}
			c.Layers = append(c.Layers, Layer{
				ID: "extra", Enabled: new(bool), Participants: []string{"ghost", "spare"},
				Instructions: "x", Delivery: DeliveryAfterRound, MaxRounds: 1, Output: Output{Format: FormatText},
			})
		}, func(t *testing.T, r *resolvedConfig) {
			t.Helper()
			if _, ok := r.Models["spare"]; ok {
				t.Error("an unused fresh participant must not be resolved")
			}
		}},
		{"moderator of a disabled layer is not checked", func(c *Config, _ *cfgtAgents, _ *preflightEnv) {
			c.Participants["chair"] = Participant{Model: "gpt-x"}
			off := false
			c.Layers[1].Enabled = &off
		}, func(t *testing.T, r *resolvedConfig) {
			t.Helper()
			if len(r.ModeratorSchemas) != 0 {
				t.Errorf("ModeratorSchemas = %v, want none for a disabled layer", r.ModeratorSchemas)
			}
		}},
		{"limits equal to ceilings", func(_ *Config, _ *cfgtAgents, env *preflightEnv) {
			env.HostLimits = Limits{MaxCalls: 30, MaxDurationSeconds: 1800, CallTimeoutSeconds: 300, MaxAttemptsPerTurn: 2, MaxParallelCalls: 2}
		}, nil},
		{"zero ceiling is no ceiling", func(_ *Config, _ *cfgtAgents, env *preflightEnv) {
			env.HostLimits = Limits{MaxCalls: 100}
		}, nil},
		{"moderator with assessment and directed", func(c *Config, _ *cfgtAgents, _ *preflightEnv) {
			c.Layers[1].Moderator.Schema = "findings"
			c.Layers[1].Moderator.AllowDirected = true
		}, func(t *testing.T, r *resolvedConfig) {
			t.Helper()
			s := string(r.ModeratorSchemas["debate"])
			if !strings.Contains(s, `"assessment"`) || !strings.Contains(s, `"directed"`) {
				t.Errorf("effective schema lacks assessment or directed: %s", s)
			}
		}},
		{"no schemas and no moderator", func(c *Config, _ *cfgtAgents, _ *preflightEnv) {
			c.Schemas = nil
			c.Layers[0].Output.Schema = ""
			c.Layers[1].Moderator = nil
		}, func(t *testing.T, r *resolvedConfig) {
			t.Helper()
			if len(r.ModeratorSchemas) != 0 {
				t.Errorf("ModeratorSchemas = %v, want none", r.ModeratorSchemas)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := cfgtExample(t)
			agents := cfgtNewAgents()
			env := cfgtEnv(agents)
			tt.mutate(cfg, agents, &env)
			if err := validateStatic(cfg); err != nil {
				t.Fatalf("ValidateStatic: %v", err)
			}
			res, err := runPreflight(context.Background(), cfg, env)
			if err != nil {
				t.Fatalf("Preflight: %v", err)
			}
			if tt.check != nil {
				tt.check(t, res)
			}
		})
	}
}

func TestPreflightHostErrors(t *testing.T) {
	for _, method := range []string{"MayTarget", "Exists", "Models"} {
		t.Run(method, func(t *testing.T) {
			agents := cfgtNewAgents()
			agents.errOn = method
			_, err := runPreflight(context.Background(), cfgtExample(t), cfgtEnv(agents))
			if !errors.Is(err, errCfgtHost) {
				t.Fatalf("want the host error, got %v", err)
			}
			if isIssues := errors.As(err, new(*ValidationError)); isIssues {
				t.Error("a host failure is not a configuration issue")
			}
		})
	}
}

func TestPreflightRequiresHost(t *testing.T) {
	cfg := cfgtExample(t)
	if _, err := runPreflight(context.Background(), cfg, preflightEnv{Launcher: "launcher"}); err == nil {
		t.Error("nil Agents accepted")
	}
	if _, err := runPreflight(context.Background(), cfg, preflightEnv{Agents: cfgtNewAgents()}); err == nil {
		t.Error("empty launcher accepted")
	}
}

// cfgtFileEnv lays out a workspace with source files and returns an env
// whose ReadAllowed admits exactly the workspace.
func cfgtFileEnv(t *testing.T) (preflightEnv, string, string) {
	t.Helper()
	root := t.TempDir()
	ws := filepath.Join(root, "workspace")
	outside := filepath.Join(root, "outside")
	for _, dir := range []string{filepath.Join(ws, "docs", "sub"), outside} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		filepath.Join(ws, "docs", "report.md"):   "# Proposal\n",
		filepath.Join(ws, "facts.json"):          `{"cost": 3}`,
		filepath.Join(ws, "broken.json"):         `{"cost": `,
		filepath.Join(ws, "dup.json"):            `{"cost": 1, "cost": 2}`,
		filepath.Join(ws, "two.json"):            `{} {}`,
		filepath.Join(outside, "secret.txt"):     "secret",
		filepath.Join(ws, "docs", "sub", "x.md"): "x",
	}
	for p, content := range files {
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(ws, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(ws, "docs", "report.md"), filepath.Join(ws, "inside-link.md")); err != nil {
		t.Fatal(err)
	}
	env := cfgtEnv(cfgtNewAgents())
	env.ResolveFile = func(ref string) (string, error) { return filepath.Join(ws, filepath.FromSlash(ref)), nil }
	env.ReadAllowed = func(p string) error {
		if p == ws || strings.HasPrefix(p, ws+string(filepath.Separator)) {
			return nil
		}
		return errors.New("outside the workspace")
	}
	return env, ws, outside
}

func TestPreflightFileSources(t *testing.T) {
	tests := []struct {
		name    string
		src     Source
		noRead  bool
		path    string   // issue path; "" means accepted
		want    []string // issue substrings
		resolve string   // accepted: the file, relative to the workspace, whose content SourceContents holds
	}{
		{"markdown file", Source{Decode: FormatMarkdown, File: "docs/report.md"}, false, "", nil, "docs/report.md"},
		{"json file", Source{Decode: FormatJSON, File: "facts.json"}, false, "", nil, "facts.json"},
		{"symlink inside the workspace", Source{Decode: FormatText, File: "inside-link.md"}, false, "", nil, "docs/report.md"},
		{"missing file", Source{Decode: FormatText, File: "nope.txt"}, false, "sources.report.file", []string{`"nope.txt"`, "does not exist"}, ""},
		{"directory", Source{Decode: FormatText, File: "docs/sub"}, false, "sources.report.file", []string{"not a regular file"}, ""},
		{"symlink out of the workspace", Source{Decode: FormatText, File: "link.txt"}, false, "sources.report.file", []string{"links to", "may not read"}, ""},
		{"no read permission at all", Source{Decode: FormatText, File: "docs/report.md"}, true, "sources.report.file", []string{"not readable"}, ""},
		{"json file malformed", Source{Decode: FormatJSON, File: "broken.json"}, false, "sources.report.file", []string{`"broken.json"`, "not one valid JSON value"}, ""},
		{"json file with duplicate keys", Source{Decode: FormatJSON, File: "dup.json"}, false, "sources.report.file", []string{"duplicate key"}, ""},
		{"json file with two values", Source{Decode: FormatJSON, File: "two.json"}, false, "sources.report.file", []string{"trailing content"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, ws, _ := cfgtFileEnv(t)
			if tt.noRead {
				env.ReadAllowed = nil
			}
			cfg := cfgtExample(t)
			cfg.Sources["report"] = tt.src
			if err := validateStatic(cfg); err != nil {
				t.Fatalf("ValidateStatic: %v", err)
			}
			res, err := runPreflight(context.Background(), cfg, env)
			if tt.path != "" {
				cfgtWantIssue(t, err, tt.path, tt.want...)
				return
			}
			if err != nil {
				t.Fatalf("Preflight: %v", err)
			}
			want, err := os.ReadFile(filepath.Join(ws, filepath.FromSlash(tt.resolve)))
			if err != nil {
				t.Fatal(err)
			}
			if got := res.SourceContents["report"]; string(got) != string(want) {
				t.Errorf("SourceContents[report] = %q, want %q", got, want)
			}
		})
	}
}

func TestPreflightReadAllowedSeesTheLexicalPath(t *testing.T) {
	env, ws, _ := cfgtFileEnv(t)
	var asked []string
	env.ReadAllowed = func(p string) error { asked = append(asked, p); return errors.New("denied") }
	cfg := cfgtExample(t)
	cfg.Sources["report"] = Source{Decode: FormatText, File: "docs/report.md"}
	_, err := runPreflight(context.Background(), cfg, env)
	cfgtWantIssue(t, err, "sources.report.file", "denied")
	if len(asked) != 1 || asked[0] != filepath.Join(ws, "docs", "report.md") {
		t.Errorf("ReadAllowed asked %v", asked)
	}
}

// A missing resolver, or one returning a relative path, is a host wiring
// error; a resolver refusing the reference is an issue naming the source.
func TestPreflightResolveFile(t *testing.T) {
	cfg := cfgtExample(t)
	cfg.Sources["report"] = Source{Decode: FormatText, File: "r.txt"}
	for name, resolve := range map[string]func(string) (string, error){
		"no resolver":       nil,
		"a relative result": func(ref string) (string, error) { return "relative/" + ref, nil },
	} {
		env := cfgtEnv(cfgtNewAgents())
		env.ResolveFile = resolve
		env.ReadAllowed = func(string) error { return nil }
		_, err := runPreflight(context.Background(), cfg, env)
		if err == nil || errors.As(err, new(*ValidationError)) {
			t.Errorf("%s: want a wiring error, got %v", name, err)
		}
	}
	env := cfgtEnv(cfgtNewAgents())
	env.ResolveFile = func(string) (string, error) { return "", errors.New("the agent may not read it") }
	env.ReadAllowed = func(string) error { return nil }
	_, err := runPreflight(context.Background(), cfg, env)
	cfgtWantIssue(t, err, "sources.report.file", `"r.txt" cannot be used: the agent may not read it`)
}

// A file source is read once, at runPreflight: the content handed over is
// what was checked, and a later change to the file does not reach it.
func TestPreflightReadsSourcesOnce(t *testing.T) {
	env, ws, _ := cfgtFileEnv(t)
	cfg := cfgtExample(t)
	cfg.Sources["report"] = Source{Decode: FormatMarkdown, File: "docs/report.md"}
	res, err := runPreflight(context.Background(), cfg, env)
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws, "docs", "report.md"), []byte("# Changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := string(res.SourceContents["report"]); got != "# Proposal\n" {
		t.Errorf("SourceContents[report] = %q, want the content read at Preflight", got)
	}
}

// The launching agent and a clone of it can read the forum's files, so each
// is refused in a forum with an anonymous input, and accepted in one without.
func TestPreflightLauncherWithAnonymousInput(t *testing.T) {
	for _, tc := range []struct {
		part Participant
		path string
		want string
	}{
		{Participant{Clone: "launcher"}, "participants.bob.clone", "A clone of launcher can read the forum's files, so it can't take part in an anonymous review"},
		{Participant{Agent: "launcher"}, "participants.bob.agent", "launcher can read the forum's files, so it can't take part in an anonymous review"},
	} {
		for _, anonymous := range []bool{false, true} {
			testPreflightLauncher(t, tc.part, anonymous, tc.path, tc.want)
		}
	}
}

// testPreflightLauncher runs runPreflight with part as participant bob and
// report reading review anonymously or not; want is the expected issue.
func testPreflightLauncher(t *testing.T, part Participant, anonymous bool, path, want string) {
	t.Helper()
	cfg := cfgtExample(t)
	cfg.Participants["bob"] = part
	cfg.Layers[2].Inputs[1].Anonymous = anonymous
	if err := validateStatic(cfg); err != nil {
		t.Fatal(err)
	}
	agents := cfgtNewAgents()
	agents.allowed["launcher"] = true
	_, err := runPreflight(context.Background(), cfg, cfgtEnv(agents))
	if !anonymous {
		if err != nil {
			t.Fatalf("%+v without an anonymous input: %v", part, err)
		}
		return
	}
	cfgtWantIssue(t, err, path, want)
}
