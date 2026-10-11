// ClawEh
// License: MIT

package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// builtinList names a top-level list LoadConfig fills with built-in defaults
// when its key is absent.
type builtinList struct {
	name  string
	path  []string // JSON key path
	clear func(c *Config, empty bool)
	count func(c *Config) int
}

var builtinLists = []builtinList{
	{
		name: "providers",
		path: []string{"providers"},
		clear: func(c *Config, empty bool) {
			c.Providers = nil
			if empty {
				c.Providers = []Provider{}
			}
		},
		count: func(c *Config) int { return len(c.Providers) },
	},
	{
		name: "models",
		path: []string{"models"},
		clear: func(c *Config, empty bool) {
			c.Models = nil
			if empty {
				c.Models = []ModelConfig{}
			}
		},
		count: func(c *Config) int { return len(c.Models) },
	},
	{
		name: "agents.list",
		path: []string{"agents", "list"},
		clear: func(c *Config, empty bool) {
			c.Agents.List = nil
			if empty {
				c.Agents.List = []AgentConfig{}
			}
		},
		count: func(c *Config) int { return len(c.Agents.List) },
	},
}

// rawKey returns the raw JSON at path in the file, and whether the key exists.
func rawKey(t *testing.T, file string, path []string) (string, bool) {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for i, key := range path {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(data, &obj); err != nil {
			t.Fatalf("decode %v: %v", path[:i], err)
		}
		v, ok := obj[key]
		if !ok {
			return "", false
		}
		data = v
	}
	return string(data), true
}

// TestStore_EmptiedBuiltinListStaysEmpty: deleting every entry of a list that
// has built-in defaults must write the key as [], so the next load keeps it
// empty instead of bringing the defaults back.
func TestStore_EmptiedBuiltinListStaysEmpty(t *testing.T) {
	for _, l := range builtinLists {
		for _, empty := range []bool{false, true} {
			name := l.name + "/nil"
			if empty {
				name = l.name + "/empty"
			}
			t.Run(name, func(t *testing.T) {
				s := newTestStore(t)
				if err := s.Update(func(c *Config) error { l.clear(c, empty); return nil }); err != nil {
					t.Fatalf("Update: %v", err)
				}
				raw, ok := rawKey(t, s.Path(), l.path)
				if !ok || raw != "[]" {
					t.Fatalf("%s on disk = %q (present %v), want []", l.name, raw, ok)
				}
				fromDisk, err := LoadConfig(s.Path())
				if err != nil {
					t.Fatal(err)
				}
				if n := l.count(fromDisk); n != 0 {
					t.Fatalf("%s after reload has %d entries, want 0", l.name, n)
				}
			})
		}
	}
}

// TestLoadConfig_BuiltinListKeyPresence: an absent key gets the built-in
// defaults; a key present as [] or null stays empty.
func TestLoadConfig_BuiltinListKeyPresence(t *testing.T) {
	cases := []struct {
		name     string
		value    string // "" means the key is absent
		defaults bool
	}{
		{name: "absent", value: "", defaults: true},
		{name: "empty", value: "[]", defaults: false},
		{name: "null", value: "null", defaults: false},
	}
	for _, l := range builtinLists {
		for _, tc := range cases {
			t.Run(l.name+"/"+tc.name, func(t *testing.T) {
				dir := t.TempDir()
				t.Setenv("CLAW_HOME", dir)
				doc := `{}`
				if tc.value != "" {
					doc = tc.value
					for _, key := range slices.Backward(l.path) {
						doc = `{"` + key + `":` + doc + `}`
					}
				}
				p := filepath.Join(dir, "config.json")
				if err := os.WriteFile(p, []byte(doc), 0o600); err != nil {
					t.Fatal(err)
				}
				cfg, err := LoadConfig(p)
				if err != nil {
					t.Fatalf("LoadConfig: %v", err)
				}
				want := 0
				if tc.defaults {
					want = l.count(DefaultConfig())
					if want == 0 {
						t.Fatalf("built-in %s is empty; test needs a non-empty default", l.name)
					}
				}
				if got := l.count(cfg); got != want {
					t.Fatalf("%s has %d entries, want %d", l.name, got, want)
				}
			})
		}
	}
}

// TestStore_AgentSkillsOnlyWhatIsListed: an agent has only the skills its list
// names. An absent key and an empty list both mean none and are saved the same
// way (no key); a listed skill is kept.
func TestStore_AgentSkillsOnlyWhatIsListed(t *testing.T) {
	cases := []struct {
		name    string
		value   []string
		wantRaw string // "" means the key is absent
		want    []string
	}{
		{name: "absent", value: nil},
		{name: "empty", value: []string{}},
		{name: "listed", value: []string{"weather"}, wantRaw: `["weather"]`, want: []string{"weather"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			err := s.Update(func(c *Config) error {
				c.Agents.List = []AgentConfig{{ID: "alice", Skills: tc.value}}
				return nil
			})
			if err != nil {
				t.Fatalf("Update: %v", err)
			}
			raw, ok := rawAgentKey(t, s.Path(), "skills")
			if tc.wantRaw == "" {
				if ok {
					t.Fatalf("skills on disk = %s, want absent", raw)
				}
			} else if !ok || raw != tc.wantRaw {
				t.Fatalf("skills on disk = %q (present %v), want %s", raw, ok, tc.wantRaw)
			}
			fromDisk, err := LoadConfig(s.Path())
			if err != nil {
				t.Fatal(err)
			}
			if got := fromDisk.Agents.List[0].Skills; !slices.Equal(got, tc.want) {
				t.Fatalf("skills from disk = %#v, want %#v", got, tc.want)
			}
		})
	}
}

// TestStore_AgentToolsOnlyWhatIsListed: an agent has only the tools its list
// names. An absent key and an empty list both mean none and are saved the same
// way (no key); a listed tool is kept and is the only one allowed.
func TestStore_AgentToolsOnlyWhatIsListed(t *testing.T) {
	cases := []struct {
		name    string
		value   []string
		wantRaw string // "" means the key is absent
		allowed []string
		denied  []string
	}{
		{name: "absent", value: nil, denied: []string{"file_read_lines", "cron_schedule", "shell_exec"}},
		{name: "empty", value: []string{}, denied: []string{"file_read_lines", "cron_schedule", "shell_exec"}},
		{
			name: "listed", value: []string{"file_read_lines"}, wantRaw: `["file_read_lines"]`,
			allowed: []string{"file_read_lines"}, denied: []string{"file_write", "cron_schedule", "shell_exec"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			err := s.Update(func(c *Config) error {
				c.Agents.List = []AgentConfig{{ID: "alice", Tools: tc.value}}
				return nil
			})
			if err != nil {
				t.Fatalf("Update: %v", err)
			}
			raw, ok := rawAgentKey(t, s.Path(), "tools")
			if tc.wantRaw == "" {
				if ok {
					t.Fatalf("tools on disk = %s, want absent", raw)
				}
			} else if !ok || raw != tc.wantRaw {
				t.Fatalf("tools on disk = %q (present %v), want %s", raw, ok, tc.wantRaw)
			}
			fromDisk, err := LoadConfig(s.Path())
			if err != nil {
				t.Fatal(err)
			}
			for src, cfg := range map[string]*Config{"store": s.Current(), "disk": fromDisk} {
				a := &cfg.Agents.List[0]
				for _, tool := range tc.allowed {
					if !a.IsToolAllowed(tool) {
						t.Errorf("%s: %s may not call listed tool %s", src, a.ID, tool)
					}
				}
				for _, tool := range tc.denied {
					if a.IsToolAllowed(tool) {
						t.Errorf("%s: %s may call unlisted tool %s", src, a.ID, tool)
					}
				}
			}
		})
	}
}

// rawAgentKey returns the raw JSON of key in the first agent of agents.list.
func rawAgentKey(t *testing.T, file, key string) (string, bool) {
	t.Helper()
	list, ok := rawKey(t, file, []string{"agents", "list"})
	if !ok {
		t.Fatal("agents.list missing")
	}
	var agents []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(list), &agents); err != nil || len(agents) == 0 {
		t.Fatalf("decode agents.list %s: %v", list, err)
	}
	v, ok := agents[0][key]
	if !ok {
		return "", false
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, v); err != nil {
		t.Fatal(err)
	}
	return buf.String(), true
}
