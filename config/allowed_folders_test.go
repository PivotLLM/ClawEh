// ClawEh
// License: MIT

package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/logger"
)

// Saving an allowed folder whose path is not absolute is refused with one
// sentence naming the agent and the path; nothing is written.
func TestStoreUpdate_RelativeAllowedFolderRefused(t *testing.T) {
	for _, p := range []string{"notes", "./notes", ""} {
		t.Run(p, func(t *testing.T) {
			s := newTestStore(t)
			err := s.Update(func(c *Config) error {
				c.Agents.List[0].AllowedFolders = []AllowedFolderConfig{{Path: p}}
				return nil
			})
			if verr, ok := errors.AsType[*ValidationError](err); !ok || verr == nil {
				t.Fatalf("err = %v, want a ValidationError", err)
			}
			want := `Main's allowed folder "` + p + `" must be an absolute path.`
			if err.Error() != want {
				t.Errorf("err = %q, want %q", err.Error(), want)
			}
			if len(s.Current().Agents.List[0].AllowedFolders) != 0 {
				t.Error("refused folder became current")
			}
		})
	}
}

// A path with a line break or other control character is refused on save,
// naming the agent and the folder. One already in the file blocks no other
// save, is skipped by the tools and is listed as ignored with its reason.
func TestStoreUpdate_ControlCharAllowedFolder(t *testing.T) {
	dir := t.TempDir()
	bad := dir + "\nsub"
	s := newTestStore(t)
	err := s.Update(func(c *Config) error {
		c.Agents.List[0].AllowedFolders = []AllowedFolderConfig{{Path: bad, Notify: true}}
		return nil
	})
	want := `Main's allowed folder "` + dir + `\nsub" contains a line break or other control character.`
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if _, ok := errors.AsType[*ValidationError](err); !ok {
		t.Errorf("err = %T, want a ValidationError", err)
	}

	// Already in the file (a hand edit).
	cfg, err := LoadConfig(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Agents.List[0].AllowedFolders = []AllowedFolderConfig{{Path: bad, Notify: true}, {Path: dir}}
	if err = writeRawConfig(s.Path(), cfg); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Reload(); err != nil {
		t.Fatal(err)
	}
	if err = s.Update(func(c *Config) error { c.Agents.List[0].Name = "Alice"; return nil }); err != nil {
		t.Fatalf("an unrelated save was blocked: %v", err)
	}
	cur := s.Current()
	if got := cur.Agents.List[0].CleanAllowedFolders(); len(got) != 1 || got[0].Path != dir {
		t.Errorf("CleanAllowedFolders = %+v, want only %s", got, dir)
	}
	ignored := cur.IgnoredAllowedFolders()
	if len(ignored) != 1 || ignored[0].Path != bad || ignored[0].Reason != FolderControlChar {
		t.Errorf("IgnoredAllowedFolders = %+v", ignored)
	}
}

// An absolute path is saved, with its notify flag.
func TestStoreUpdate_AllowedFolderSaved(t *testing.T) {
	s := newTestStore(t)
	dir := t.TempDir()
	if err := s.Update(func(c *Config) error {
		c.Agents.List[0].AllowedFolders = []AllowedFolderConfig{{Path: dir, Notify: true}}
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got := s.Current().Agents.List[0].AllowedFolders; len(got) != 1 || got[0] != (AllowedFolderConfig{Path: dir, Notify: true}) {
		t.Errorf("allowed folders = %+v", got)
	}
}

// A note of up to 100 characters (counted as characters, not bytes) is saved;
// a longer one is refused with one sentence naming the agent and the folder,
// and nothing is written.
func TestStoreUpdate_AllowedFolderNote(t *testing.T) {
	dir := t.TempDir()
	t.Run("at the limit", func(t *testing.T) {
		s := newTestStore(t)
		note := strings.Repeat("é", MaxAllowedFolderNoteChars)
		if err := s.Update(func(c *Config) error {
			c.Agents.List[0].AllowedFolders = []AllowedFolderConfig{{Path: dir, Note: note}}
			return nil
		}); err != nil {
			t.Fatalf("Update: %v", err)
		}
		if got := s.Current().Agents.List[0].AllowedFolders[0].Note; got != note {
			t.Errorf("note = %q, want %q", got, note)
		}
	})
	t.Run("too long", func(t *testing.T) {
		s := newTestStore(t)
		err := s.Update(func(c *Config) error {
			c.Agents.List[0].AllowedFolders = []AllowedFolderConfig{{Path: dir, Note: strings.Repeat("a", MaxAllowedFolderNoteChars+1)}}
			return nil
		})
		if verr, ok := errors.AsType[*ValidationError](err); !ok || verr == nil {
			t.Fatalf("err = %v, want a ValidationError", err)
		}
		want := `Main's note for "` + dir + `" is longer than 100 characters.`
		if err.Error() != want {
			t.Errorf("err = %q, want %q", err.Error(), want)
		}
		if len(s.Current().Agents.List[0].AllowedFolders) != 0 {
			t.Error("refused note became current")
		}
	})
}

// A too-long note already in the file (a hand edit) does not block an
// unrelated save.
func TestStoreUpdate_LongNoteInFileTolerated(t *testing.T) {
	s := newTestStore(t)
	dir := t.TempDir()
	s.cur.Agents.List[0].AllowedFolders = []AllowedFolderConfig{{Path: dir, Note: strings.Repeat("a", 150)}}
	if err := s.Update(func(c *Config) error {
		c.Agents.List[0].Name = "Alice"
		return nil
	}); err != nil {
		t.Fatalf("an unrelated save was refused: %v", err)
	}
}

// CleanAllowedFolders keeps absolute paths once each, cleaned, with their
// trimmed notes; AllowedFolderPaths follows it.
func TestCleanAllowedFolders(t *testing.T) {
	a := &AgentConfig{AllowedFolders: []AllowedFolderConfig{
		{Path: "/opt/audio/in/", Note: "  inbound audio notes "},
		{Path: "relative"},
		{Path: "/opt/audio/in", Note: "second entry"},
		{Path: " /srv/src", Notify: true},
	}}
	got := a.CleanAllowedFolders()
	want := []AllowedFolderConfig{
		{Path: "/opt/audio/in", Note: "inbound audio notes"},
		{Path: "/srv/src", Notify: true},
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("CleanAllowedFolders = %+v, want %+v", got, want)
	}
	if p := a.AllowedFolderPaths(); len(p) != 2 || p[0] != "/opt/audio/in" || p[1] != "/srv/src" {
		t.Errorf("AllowedFolderPaths = %v", p)
	}
	if (*AgentConfig)(nil).CleanAllowedFolders() != nil {
		t.Error("nil agent must have no folders")
	}
}

// A config with the retired mounts list loads with each entry migrated to an
// allowed folder (path and notify kept), one warning per entry naming the
// agent and the path, and the next save writes allowed_folders only.
func TestLoadConfig_MigratesMounts(t *testing.T) {
	var buf bytes.Buffer
	restore := logger.RedirectForTest(&buf)
	defer restore()

	dir := t.TempDir()
	t.Setenv("CLAW_HOME", dir)
	path := filepath.Join(dir, "config.json")
	src := filepath.Join(dir, "src")
	ref := filepath.Join(dir, "ref")
	doc := `{"agents":{"list":[{"id":"alice","mounts":[` +
		`{"name":"notes","path":"` + src + `","notify":true,"writable":true},` +
		`{"name":"ref","path":"` + ref + `"}]}]}}`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	alice := s.Current().AgentByID("alice")
	want := []AllowedFolderConfig{{Path: src, Notify: true}, {Path: ref}}
	if len(alice.AllowedFolders) != 2 || alice.AllowedFolders[0] != want[0] || alice.AllowedFolders[1] != want[1] {
		t.Errorf("allowed folders = %+v, want %+v", alice.AllowedFolders, want)
	}
	if alice.LegacyMounts != nil {
		t.Errorf("legacy mounts kept: %+v", alice.LegacyMounts)
	}
	log := buf.String()
	for _, p := range []string{src, ref} {
		if !strings.Contains(log, "allowed_folders") || !strings.Contains(log, p) || !strings.Contains(log, "alice") {
			t.Errorf("no warning naming alice and %s:\n%s", p, log)
		}
	}
	if strings.Contains(log, "unknown config key") {
		t.Errorf("mounts reported as an unknown key:\n%s", log)
	}

	if err = s.Update(func(c *Config) error {
		c.Agents.List[0].Name = "Alice"
		return nil
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Agents struct {
			List []map[string]json.RawMessage `json:"list"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	a := saved.Agents.List[0]
	if _, ok := a["mounts"]; ok {
		t.Errorf("saved config still has mounts: %s", raw)
	}
	if _, ok := a["allowed_folders"]; !ok {
		t.Errorf("saved config has no allowed_folders: %s", raw)
	}
}

// A migrated mount whose path is relative loads, and does not block an
// unrelated save.
func TestLoadConfig_MigratedRelativeMountTolerated(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAW_HOME", dir)
	path := filepath.Join(dir, "config.json")
	doc := `{"agents":{"list":[{"id":"alice","mounts":[{"name":"notes","path":"notes"}]}]}}`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := s.Update(func(c *Config) error {
		c.Agents.List[0].Name = "Alice"
		return nil
	}); err != nil {
		t.Fatalf("unrelated save refused: %v", err)
	}
}

// A mount that was read-only, on a folder the account can write, gets a
// second warning naming the agent and the folder; a writable mount does not.
func TestLoadConfig_MigratedReadOnlyMountNowWritable(t *testing.T) {
	var buf bytes.Buffer
	restore := logger.RedirectForTest(&buf)
	defer restore()

	dir := t.TempDir()
	t.Setenv("CLAW_HOME", dir)
	path := filepath.Join(dir, "config.json")
	ro := filepath.Join(dir, "ref")
	rw := filepath.Join(dir, "src")
	for _, d := range []string{ro, rw} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	doc := `{"agents":{"list":[{"id":"alice","mounts":[` +
		`{"name":"ref","path":"` + ro + `"},{"name":"src","path":"` + rw + `","writable":true}]}]}}`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(path); err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	var gained []string
	for line := range strings.SplitSeq(buf.String(), "\n") {
		if strings.Contains(line, "now writable by the agent") {
			gained = append(gained, line)
		}
	}
	if len(gained) != 1 || !strings.Contains(gained[0], ro) || !strings.Contains(gained[0], "alice") {
		t.Errorf("want one warning naming alice and %s, got %q", ro, gained)
	}
}
