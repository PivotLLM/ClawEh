// ClawEh
// License: MIT

package config

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPermissionsValidate(t *testing.T) {
	cases := []struct {
		name    string
		p       PermissionsConfig
		wantErr string // "" = valid
	}{
		{"empty keeps the defaults", PermissionsConfig{}, ""},
		{"operator setup", PermissionsConfig{
			Logs:   AreaPermissions{DirMode: "0750", FileMode: "0640"},
			Agents: AreaPermissions{DirMode: "0750", FileMode: "0640"},
		}, ""},
		{"three digits", PermissionsConfig{DirMode: "750", FileMode: "640"}, ""},
		{"group write allowed", PermissionsConfig{Common: AreaPermissions{DirMode: "0770", FileMode: "0660"}}, ""},
		{"other read allowed", PermissionsConfig{Media: AreaPermissions{DirMode: "0755", FileMode: "0644"}}, ""},
		{"not octal", PermissionsConfig{DirMode: "0780"}, "security.permissions.dir_mode"},
		{"too short", PermissionsConfig{FileMode: "64"}, "security.permissions.file_mode"},
		{"too long", PermissionsConfig{Logs: AreaPermissions{DirMode: "00750"}}, "security.permissions.logs.dir_mode"},
		{"not a number", PermissionsConfig{Backup: AreaPermissions{FileMode: "rw-"}}, "security.permissions.backup.file_mode"},
		{"setgid", PermissionsConfig{Agents: AreaPermissions{DirMode: "2750"}}, "setuid, setgid and sticky"},
		{"sign is not a digit", PermissionsConfig{DirMode: "+755"}, "use 3 or 4 octal digits"},
		{"letter first", PermissionsConfig{FileMode: "x640"}, "use 3 or 4 octal digits"},
		{"other write on a folder", PermissionsConfig{Logs: AreaPermissions{DirMode: "0757"}}, "other users may not have write access"},
		{"other write on a file", PermissionsConfig{Agents: AreaPermissions{FileMode: "0642"}}, "security.permissions.agents.file_mode"},
		{"folder without owner execute", PermissionsConfig{DirMode: "0650"}, "owner read, write and execute"},
		{"file without owner write", PermissionsConfig{Media: AreaPermissions{FileMode: "0440"}}, "owner read and write"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.p.Validate()
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("Validate = %v, want an error containing %q", err, c.wantErr)
			}
		})
	}
}

func TestPermissionsModes(t *testing.T) {
	p := PermissionsConfig{
		FileMode: "0640",
		Logs:     AreaPermissions{DirMode: "0750"},
		Agents:   AreaPermissions{DirMode: "0750", FileMode: "0600"},
	}
	for _, c := range []struct {
		area string
		want Modes
	}{
		{"", Modes{0o700, 0o640}},
		{AreaLogs, Modes{0o750, 0o640}},   // file mode from the top-level pair
		{AreaAgents, Modes{0o750, 0o600}}, // both its own
		{AreaMedia, Modes{0o700, 0o640}},  // all from the top-level pair
	} {
		if got := p.Modes(c.area); got != c.want {
			t.Errorf("Modes(%q) = %v, want %v", c.area, got, c.want)
		}
	}
	if got := (PermissionsConfig{}).Modes(AreaLogs); got != (Modes{DefaultDirMode, DefaultFileMode}) {
		t.Errorf("default Modes = %v, want 0700/0600", got)
	}
	if got, want := p.Summary(), "folders 0700, secrets 0600; logs 0750/0640; agents 0750/0600"; got != want {
		t.Errorf("Summary = %q, want %q", got, want)
	}
}

// TestLoadConfigRefusesBadPermissions: an invalid mode stops the load and
// names the key; a valid block round-trips.
func TestLoadConfigRefusesBadPermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"security":{"permissions":{"logs":{"dir_mode":"0777"}}}}`)
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "security.permissions.logs.dir_mode") {
		t.Fatalf("LoadConfig = %v, want a refusal naming security.permissions.logs.dir_mode", err)
	}
	write(`{"security":{"permissions":{"agents":{"dir_mode":"0750","file_mode":"0640"}}}}`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got := cfg.PermissionModes(AreaAgents); got != (Modes{0o750, 0o640}) {
		t.Errorf("agents modes = %v, want 0750/0640", got)
	}
}

// Within compares absolute paths, so a relative base dir still matches an
// absolute path under it.
func TestWithinRelative(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if !Within("agents", filepath.Join(wd, "agents", "alice")) {
		t.Error("relative dir did not contain the absolute path under it")
	}
	if Within("agents", filepath.Join(wd, "other")) {
		t.Error("relative dir contained a sibling")
	}
}

func TestAgentModes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAW_HOME", home)
	cfg := DefaultConfig()
	ws := filepath.Join(t.TempDir(), "alice")
	cfg.Agents.List = []AgentConfig{{ID: "alice", Workspace: ws}}
	cfg.Security.Permissions.Agents = AreaPermissions{DirMode: "0750", FileMode: "0640"}
	area := Modes{Dir: 0o750, File: fs.FileMode(0o640)}
	private := Modes{Dir: DefaultDirMode, File: DefaultFileMode}
	for path, want := range map[string]Modes{
		filepath.Join(home, "agents", "bob"):           area,
		filepath.Join(ws, "sessions"):                  area,
		filepath.Join(home, "internal", "temp", "x"):   private,
		filepath.Join(os.TempDir(), "claw-temp-agent"): private,
	} {
		if got := cfg.AgentModes(path); got != want {
			t.Errorf("AgentModes(%s) = %v, want %v", path, got, want)
		}
	}
}
