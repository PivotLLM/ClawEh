// ClawEh
// License: MIT

package agentreg

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/global"
)

// TestCreateFresh_Modes: the options select the mode, the mode decides the
// cognitive memory whatever the configuration says, and the system prompt is
// the creator's or DefaultSystemPrompt.
func TestCreateFresh_Modes(t *testing.T) {
	tests := []struct {
		name       string
		cfg        config.AgentConfig
		opts       []Option
		wantMode   Mode
		wantPrompt string
	}{
		{"default has memory even when the config says off", config.AgentConfig{Cogmem: new(false)}, nil, ModeMemory, DefaultSystemPrompt},
		{"system prompt", config.AgentConfig{}, []Option{WithSystemPrompt("You are Bob.")}, ModeMemory, "You are Bob."},
		{"without memory, even when the config says on", config.AgentConfig{Cogmem: new(true)}, []Option{WithoutMemory()}, ModeNoMemory, DefaultSystemPrompt},
		{"single shot", config.AgentConfig{}, []Option{SingleShot()}, ModeSingleShot, DefaultSystemPrompt},
		{"single shot wins over without memory", config.AgentConfig{}, []Option{WithoutMemory(), SingleShot()}, ModeSingleShot, DefaultSystemPrompt},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := mustNew(t, testConfig(t), newFakeHost())
			spec := mustGet(t, r, mustCreate(t, r, tc.cfg, tc.opts...)).spec
			if !spec.Fresh || spec.Mode != tc.wantMode || spec.SystemPrompt != tc.wantPrompt {
				t.Fatalf("spec fresh %v mode %q prompt %q; want fresh, %q, %q", spec.Fresh, spec.Mode, spec.SystemPrompt, tc.wantMode, tc.wantPrompt)
			}
			if got, want := spec.Config.CognitiveMemoryEnabled(), tc.wantMode == ModeMemory; got != want {
				t.Fatalf("cognitive memory %v, want %v", got, want)
			}
			if spec.SingleShot() != (tc.wantMode == ModeSingleShot) {
				t.Fatalf("SingleShot() = %v for mode %q", spec.SingleShot(), spec.Mode)
			}
		})
	}
}

// TestCreate_FreshOptionRefusals: a clone takes its prompt and memory from its
// source, so the fresh options are refused for it; a blank prompt is refused.
func TestCreate_FreshOptionRefusals(t *testing.T) {
	r := mustNew(t, testConfig(t), newFakeHost())
	for name, opt := range map[string]Option{
		"WithSystemPrompt": WithSystemPrompt("x"),
		"WithoutMemory":    WithoutMemory(),
		"SingleShot":       SingleShot(),
	} {
		if _, err := r.Create(config.AgentConfig{}, CloneOf("alice"), opt); err == nil {
			t.Errorf("a clone with %s was created", name)
		}
	}
	for _, p := range []string{"", "  \n"} {
		if _, err := r.Create(config.AgentConfig{}, WithSystemPrompt(p)); err == nil {
			t.Errorf("a fresh agent with system prompt %q was created", p)
		}
	}
	if got := len(r.ListTemp()); got != 0 {
		t.Fatalf("refused creations left %d temporary agents", got)
	}
}

// TestCloneSpec_HasNoFreshFields: a clone is not fresh and carries no mode or
// prompt of its own.
func TestCloneSpec_HasNoFreshFields(t *testing.T) {
	r := mustNew(t, testConfig(t), newFakeHost())
	spec := mustGet(t, r, mustCreate(t, r, config.AgentConfig{}, CloneOf("alice"))).spec
	if spec.Fresh || spec.Mode != "" || spec.SystemPrompt != "" || spec.SingleShot() {
		t.Fatalf("clone spec = %+v", spec)
	}
}

// TestPersistence_FreshModes: each mode, the custom prompt and the owner are
// saved and restored; the restored configuration's memory follows the mode.
func TestPersistence_FreshModes(t *testing.T) {
	cfg := testConfig(t)
	r := mustNew(t, cfg, newFakeHost())
	ids := map[string]Spec{
		mustCreate(t, r, config.AgentConfig{}, WithSystemPrompt("You are Bob."), OwnedBy("alice")): {Mode: ModeMemory, SystemPrompt: "You are Bob.", Owner: "alice"},
		mustCreate(t, r, config.AgentConfig{}, WithoutMemory()):                                    {Mode: ModeNoMemory, SystemPrompt: DefaultSystemPrompt},
		mustCreate(t, r, config.AgentConfig{}, SingleShot(), WithSystemPrompt("Translate.")):       {Mode: ModeSingleShot, SystemPrompt: "Translate."},
	}

	r2 := mustNew(t, cfg, newFakeHost())
	for id, want := range ids {
		got := mustGet(t, r2, id).spec
		if !got.Fresh || got.Mode != want.Mode || got.SystemPrompt != want.SystemPrompt || got.Owner != want.Owner {
			t.Fatalf("%s restored as %+v; want mode %q prompt %q owner %q", id, got, want.Mode, want.SystemPrompt, want.Owner)
		}
		if got.Config.CognitiveMemoryEnabled() != (want.Mode == ModeMemory) {
			t.Fatalf("%s restored with cognitive memory %v for mode %q", id, got.Config.CognitiveMemoryEnabled(), got.Mode)
		}
		if got.Workspace != freshWorkspace(got.StateDir) {
			t.Fatalf("%s restored with workspace %q", id, got.Workspace)
		}
	}

	// A reload rebuilds them with the same mode and prompt.
	if err := r2.Reload(context.Background(), cfg, newFakeHost().build, commitOK); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	for id, want := range ids {
		if got := mustGet(t, r2, id).spec; got.Mode != want.Mode || got.SystemPrompt != want.SystemPrompt {
			t.Fatalf("%s after reload: mode %q prompt %q", id, got.Mode, got.SystemPrompt)
		}
	}
}

// TestRestore_RejectsInvalidFreshRecords: a fresh record without a valid mode
// or a prompt is not restored and its directory is removed.
func TestRestore_RejectsInvalidFreshRecords(t *testing.T) {
	cfg := testConfig(t)
	internal := filepath.Join(cfg.DataDir(), global.InternalDir)
	ids := []string{
		"11111111-1111-1111-1111-111111111111", // no mode
		"22222222-2222-2222-2222-222222222222", // unknown mode
		"33333333-3333-3333-3333-333333333333", // no prompt
		"44444444-4444-4444-4444-444444444444", // valid
	}
	recs := []tempRecord{
		{ID: ids[0], Config: &config.AgentConfig{}, TTLSeconds: 3600, SystemPrompt: "p"},
		{ID: ids[1], Config: &config.AgentConfig{}, TTLSeconds: 3600, Mode: "forever", SystemPrompt: "p"},
		{ID: ids[2], Config: &config.AgentConfig{}, TTLSeconds: 3600, Mode: ModeMemory},
		{ID: ids[3], Config: &config.AgentConfig{}, TTLSeconds: 3600, Mode: ModeNoMemory, SystemPrompt: "p"},
	}
	for _, id := range ids {
		if err := os.MkdirAll(filepath.Join(internal, TempDirName, id), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	data, err := json.Marshal(stateFile{Version: stateFileVersion, Agents: recs})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(internal, StateFileName), data, 0o600); err != nil {
		t.Fatal(err)
	}

	r := mustNew(t, cfg, newFakeHost())
	for _, id := range ids[:3] {
		if _, ok := r.Get(id); ok {
			t.Fatalf("invalid record %s was restored", id)
		}
		if dirExists(filepath.Join(internal, TempDirName, id)) {
			t.Fatalf("invalid record %s left its directory", id)
		}
	}
	if got := mustGet(t, r, ids[3]).spec; got.Mode != ModeNoMemory || got.SystemPrompt != "p" {
		t.Fatalf("valid record restored as %+v", got)
	}
}
