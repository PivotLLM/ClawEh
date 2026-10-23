// ClawEh
// License: MIT

package config

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/logger"
)

// Saving a mount named after a workspace folder, in any case, is refused with
// one sentence naming the agent and the mount; nothing is written.
func TestStoreUpdate_ReservedMountRefused(t *testing.T) {
	for _, r := range ReservedWorkspaceNames {
		for _, name := range []string{r, strings.ToUpper(r[:1]) + r[1:]} {
			t.Run(name, func(t *testing.T) {
				s := newTestStore(t)
				err := s.Update(func(c *Config) error {
					c.Agents.List[0].Mounts = []MountConfig{{Name: name, Path: t.TempDir()}}
					return nil
				})
				if verr, ok := errors.AsType[*ValidationError](err); !ok || verr == nil {
					t.Fatalf("err = %v, want a ValidationError", err)
				}
				want := `Main's mount "` + name + `" uses a reserved name; choose another name.`
				if err.Error() != want {
					t.Errorf("err = %q, want %q", err.Error(), want)
				}
				if len(s.Current().Agents.List[0].Mounts) != 0 {
					t.Error("refused mount became current")
				}
			})
		}
	}
}

// A normal mount name is saved.
func TestStoreUpdate_NormalMountSaved(t *testing.T) {
	s := newTestStore(t)
	if err := s.Update(func(c *Config) error {
		c.Agents.List[0].Mounts = []MountConfig{{Name: "notes", Path: t.TempDir()}}
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got := s.Current().Agents.List[0].Mounts; len(got) != 1 || got[0].Name != "notes" {
		t.Errorf("mounts = %+v", got)
	}
}

// A config that already has a reserved mount loads: the mount is set aside
// with a warning naming agent and mount, and an unrelated save, or the one
// that renames it, is not refused.
func TestLoadConfig_ReservedMountIgnored(t *testing.T) {
	var buf bytes.Buffer
	restore := logger.RedirectForTest(&buf)
	defer restore()

	dir := t.TempDir()
	t.Setenv("CLAW_HOME", dir)
	path := filepath.Join(dir, "config.json")
	ext := t.TempDir()
	doc := `{"agents":{"list":[{"id":"alice","mounts":[` +
		`{"name":"Files","path":"` + ext + `"},{"name":"notes","path":"` + ext + `"}]}]}}`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	cfg := s.Current()
	if got := cfg.IgnoredMounts(); len(got) != 1 || got[0] != (IgnoredMount{Agent: "alice", Mount: "Files"}) {
		t.Errorf("IgnoredMounts = %+v", got)
	}
	eff := cfg.AgentByID("alice").EffectiveMounts(dir)
	if len(eff) != 1 || eff[0].Name != "notes" {
		t.Errorf("EffectiveMounts = %+v, want only notes", eff)
	}
	log := buf.String()
	if !strings.Contains(log, "mount ignored") || !strings.Contains(log, "alice") || !strings.Contains(log, "Files") {
		t.Errorf("no warning naming agent and mount:\n%s", log)
	}

	if err := s.Update(func(c *Config) error {
		c.Agents.List[0].Name = "Alice"
		c.Agents.List[0].Mounts[0].Path = t.TempDir()
		return nil
	}); err != nil {
		t.Fatalf("unrelated save refused: %v", err)
	}
	if err := s.Update(func(c *Config) error {
		c.Agents.List[0].Mounts[0].Name = "work"
		return nil
	}); err != nil {
		t.Fatalf("rename refused: %v", err)
	}
	if got := s.Current().IgnoredMounts(); len(got) != 0 {
		t.Errorf("after rename IgnoredMounts = %+v", got)
	}
}
