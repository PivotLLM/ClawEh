// ClawEh
// License: MIT

// Package capabilities pins what a production-shaped configuration lets each
// agent do. The fixture under testdata models a real install: HTTP and CLI
// providers, CLI models that still carry the permission-bypass flag in
// extra_args, agents with Fusion on and Fusion services (and groups within
// them) named in mcp_tools, MCP servers, a name that is both an MCP server and
// a Fusion service, and deny lists. The test resolves, per agent, the native
// tools, suites, Fusion tools registered through the real engine and the MCP
// grants, and per CLI model the exact command line and environment, and diffs
// the result against effective.golden.
//
// A behaviour change under tool gating, allowlists, suite switches, provider
// arguments or their defaults fails this test until the golden file is
// regenerated (UPDATE_GOLDEN=1 go test ./tests/capabilities/), so the change is
// visible in review and carries its BREAKING changelog entry. Two regressions
// this would have caught: the bypass flag becoming opt-in (2026-09-26) and the
// per-service Fusion gating lost when MCPFusion was folded in (2026-07-09).
package capabilities

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/forum"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/internal/gateway"
	"github.com/PivotLLM/ClawEh/tools"
	toolsforum "github.com/PivotLLM/ClawEh/tools/forum"
	"github.com/PivotLLM/ClawEh/tools/fusion"
)

const goldenPath = "testdata/effective.golden"

func TestEffectiveCapabilities(t *testing.T) {
	home := t.TempDir()
	copyFile(t, "testdata/fixture.json", filepath.Join(home, "config.json"), 0o600)
	copyTree(t, "testdata/fusion", filepath.Join(home, "fusion"))
	t.Setenv("CLAW_HOME", home)

	cfg, err := config.LoadConfig(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	gateway.RegisterToolProvidersForTest()
	svc := forum.New(forum.Host{Messenger: nopForumHost{}, Agents: nopForumHost{}, Notifier: nopForumHost{}, Logger: nopForumHost{}})
	toolsforum.SetService(svc)
	t.Cleanup(func() {
		toolsforum.SetService(nil)
		if closeErr := svc.Close(context.Background()); closeErr != nil {
			t.Errorf("forum service Close: %v", closeErr)
		}
	})

	got := render(t, cfg)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if werr := os.WriteFile(goldenPath, []byte(got), 0o644); werr != nil {
			t.Fatal(werr)
		}
		t.Logf("wrote %s", goldenPath)
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v (run with UPDATE_GOLDEN=1 to create it)", err)
	}
	if string(want) != got {
		t.Errorf("effective capabilities changed.\n%s\nIf the change is intended, regenerate with UPDATE_GOLDEN=1, add the BREAKING changelog entry, and review the diff of %s.",
			diff(string(want), got), goldenPath)
	}
}

// render writes the effective capabilities in a stable text form.
func render(t *testing.T, cfg *config.Config) string {
	t.Helper()
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }

	catalog := nativeCatalog()
	servers := sortedKeys(cfg.Tools.MCP.Servers)

	agents := append([]config.AgentConfig(nil), cfg.Agents.List...)
	sort.Slice(agents, func(i, j int) bool { return agents[i].ID < agents[j].ID })
	for i := range agents {
		a := &agents[i]
		w("agent %s\n", a.ID)
		w("  suites: cogmem=%s maestro=%s fusion=%s forum=%s\n",
			onOff(cfg.AgentSuiteEnabled(a.ID, "cogmem")), onOff(cfg.AgentSuiteEnabled(a.ID, "maestro")), onOff(cfg.AgentSuiteEnabled(a.ID, "fusion")),
			onOff(cfg.AgentSuiteEnabled(a.ID, "forum")))

		var native []string
		for _, name := range catalog {
			if a.IsToolAllowed(name) {
				native = append(native, name)
			}
		}
		w("  native tools: %s\n", list(native))

		defs := fusion.GlobalProvider.RegisterTools(global.Deps{Cfg: cfg, AgentID: a.ID})
		fusionTools := make([]string, 0, len(defs))
		for _, d := range defs {
			name := d.Name
			if a.IsToolDenied(name) {
				name += " (denied)"
			}
			fusionTools = append(fusionTools, name)
		}
		sort.Strings(fusionTools)
		w("  fusion tools: %s\n", list(fusionTools))

		forumDefs := toolsforum.GlobalProvider.RegisterTools(global.Deps{
			Cfg: cfg, AgentID: a.ID, Host: tools.ToolDeps{Cfg: cfg, AgentID: a.ID, Workspace: t.TempDir()},
		})
		forumTools := make([]string, 0, len(forumDefs))
		for _, d := range forumDefs {
			name := toolsforum.Suite + "_" + d.Name
			if a.IsToolDenied(name) {
				name += " (denied)"
			}
			forumTools = append(forumTools, name)
		}
		sort.Strings(forumTools)
		w("  forum tools: %s\n", list(forumTools))

		mcp := make([]string, 0, len(servers))
		for _, s := range servers {
			mcp = append(mcp, fmt.Sprintf("%s=%s", s, yesNo(a.MCPToolAllowed("mcp_"+s+"_probe"))))
		}
		w("  mcp servers: %s\n", list(mcp))
		w("  mcp_tools: %s\n", list(a.MCPTools))
		w("  deny_tools: %s\n", list(a.DenyTools))
	}

	w("models\n")
	models := append([]config.ModelConfig(nil), cfg.Models...)
	sort.Slice(models, func(i, j int) bool { return models[i].ModelName < models[j].ModelName })
	for _, m := range models {
		prov, err := cfg.GetProvider(m.Provider)
		if err != nil {
			w("  %s: provider %q: %v\n", m.ModelName, m.Provider, err)
			continue
		}
		if !config.IsCLIProtocol(prov.Protocol) {
			w("  %s: %s via %s (%s)\n", m.ModelName, m.Model, prov.Name, prov.Protocol)
			continue
		}
		args := config.CLIArgs(prov.Protocol, prov.BypassRestrictions, m.ExtraArgs)
		env := sortedKeys(config.CLIEnv(prov.Protocol, m.Env))
		w("  %s: %s via %s (%s) bypass=%s\n", m.ModelName, m.Model, prov.Name, prov.Protocol, onOff(prov.BypassRestrictions))
		w("    argv: %s\n", list(args))
		w("    env: %s\n", list(env))
	}
	return b.String()
}

// nativeCatalog is every tool the registered non-suite providers describe, by
// published name, sorted. Suite tools (cogmem, maestro, fusion, forum) are gated by
// their switches, not the per-tool allowlist, and are reported separately.
func nativeCatalog() []string {
	var names []string
	for _, p := range tools.GetProviders() {
		if sp, ok := p.(tools.SuiteProvider); ok && sp.Suite() != "" {
			continue
		}
		for _, d := range p.Describe() {
			names = append(names, d.Name)
		}
	}
	sort.Strings(names)
	return names
}

func list(items []string) string {
	if len(items) == 0 {
		return "(none)"
	}
	return strings.Join(items, ", ")
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// diff is a line-level report of the first divergence and both sides after it.
func diff(want, got string) string {
	wl, gl := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(wl) || i < len(gl); i++ {
		var a, b string
		if i < len(wl) {
			a = wl[i]
		}
		if i < len(gl) {
			b = gl[i]
		}
		if a != b {
			return fmt.Sprintf("first difference at line %d:\n  golden: %q\n  now:    %q", i+1, a, b)
		}
	}
	return "no line difference (trailing content)"
}

func copyFile(t *testing.T, src, dst string, mode os.FileMode) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, mode); err != nil {
		t.Fatal(err)
	}
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		copyFile(t, filepath.Join(src, e.Name()), filepath.Join(dst, e.Name()), 0o600)
	}
}

// nopForumHost lets the forum service exist so the forum tools register; it
// runs nothing.
type nopForumHost struct{}

func (nopForumHost) Ask(context.Context, string, string, time.Duration) (forum.Reply, error) {
	return forum.Reply{}, errors.New("unused")
}
func (nopForumHost) Exists(context.Context, string) (bool, error)            { return false, nil }
func (nopForumHost) MayTarget(context.Context, string, string) (bool, error) { return false, nil }
func (nopForumHost) Models(context.Context, string) ([]forum.ModelInfo, error) {
	return nil, nil
}

func (nopForumHost) CreateClone(context.Context, forum.CloneSpec) (string, error) {
	return "", errors.New("unused")
}

func (nopForumHost) CreateFresh(context.Context, forum.FreshSpec) (string, error) {
	return "", errors.New("unused")
}
func (nopForumHost) Delete(context.Context, string, string) error { return nil }
func (nopForumHost) Touch(context.Context, string, string) error  { return nil }
func (nopForumHost) ForumFinished(context.Context, forum.Origin, *forum.Result) error {
	return nil
}
func (nopForumHost) Debugf(string, ...any) {}
func (nopForumHost) Infof(string, ...any)  {}
func (nopForumHost) Warnf(string, ...any)  {}
func (nopForumHost) Errorf(string, ...any) {}
