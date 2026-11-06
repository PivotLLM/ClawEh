// ClawEh
// License: MIT

package config

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/logger"
)

func TestPermissionsValidate(t *testing.T) {
	cases := []struct {
		name    string
		p       PermissionsConfig
		wantErr string // "" = valid
	}{
		{"empty is filled at load", PermissionsConfig{}, ""},
		{"the defaults", DefaultPermissions(), ""},
		{"operator setup", PermissionsConfig{
			Logs:   AreaPermissions{DirMode: "0750", FileMode: "0640"},
			Agents: AreaPermissions{DirMode: "0750", FileMode: "0640"},
		}, ""},
		{"three digits", PermissionsConfig{Default: AreaPermissions{DirMode: "750", FileMode: "640"}}, ""},
		{"other write allowed", PermissionsConfig{Common: AreaPermissions{DirMode: "0777", FileMode: "0666"}}, ""},
		{"no owner bits allowed", PermissionsConfig{Backup: AreaPermissions{DirMode: "0050", FileMode: "0040"}}, ""},
		{"not octal", PermissionsConfig{Root: AreaPermissions{DirMode: "0780"}}, "security.permissions.root.dir_mode"},
		{"too short", PermissionsConfig{Secrets: AreaPermissions{FileMode: "64"}}, "security.permissions.secrets.file_mode"},
		{"too long", PermissionsConfig{Logs: AreaPermissions{DirMode: "00750"}}, "security.permissions.logs.dir_mode"},
		{"not a number", PermissionsConfig{Backup: AreaPermissions{FileMode: "rw-"}}, "security.permissions.backup.file_mode"},
		{"setgid", PermissionsConfig{Agents: AreaPermissions{DirMode: "2750"}}, "setuid, setgid and sticky"},
		{"sticky on bin", PermissionsConfig{Bin: AreaPermissions{FileMode: "1700"}}, "security.permissions.bin.file_mode"},
		{"sign is not a digit", PermissionsConfig{Default: AreaPermissions{DirMode: "+755"}}, "use 3 or 4 octal digits"},
		{"letter first", PermissionsConfig{Media: AreaPermissions{FileMode: "x640"}}, "use 3 or 4 octal digits"},
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
		Default: AreaPermissions{DirMode: "0750", FileMode: "0640"},
		Logs:    AreaPermissions{DirMode: "0755"},
		Agents:  AreaPermissions{DirMode: "0770", FileMode: "0660"},
	}
	for _, c := range []struct {
		area string
		want Modes
	}{
		{"", Modes{0o750, 0o640}},          // the default area
		{"nonsense", Modes{0o750, 0o640}},  // the default area
		{AreaLogs, Modes{0o755, 0o600}},    // an unset key is the built-in, not the default area's
		{AreaAgents, Modes{0o770, 0o660}},  // both its own
		{AreaBin, Modes{0o700, 0o700}},     // built-in bin pair
		{AreaSecrets, Modes{0o700, 0o600}}, // built-in
		{AreaRoot, Modes{0o700, 0o600}},    // built-in
	} {
		if got := p.Modes(c.area); got != c.want {
			t.Errorf("Modes(%q) = %v, want %v", c.area, got, c.want)
		}
	}
	want := "root 0700/0600; secrets 0700/0600; bin 0700/0700; default 0750/0640; logs 0755/0600; " +
		"agents 0770/0660; media 0700/0600; common 0700/0600; backup 0700/0600"
	if got := p.Summary(); got != want {
		t.Errorf("Summary = %q, want %q", got, want)
	}
}

// TestPermissionsOtherAccess: every mode with a non-zero other digit is
// listed by key; group bits are not.
func TestPermissionsOtherAccess(t *testing.T) {
	if got := DefaultPermissions().OtherAccess(); len(got) != 0 {
		t.Errorf("defaults: %v, want none", got)
	}
	p := DefaultPermissions()
	p.Logs = AreaPermissions{DirMode: "0755", FileMode: "0640"}
	p.Media.FileMode = "0604"
	got := p.OtherAccess()
	want := []string{
		"security.permissions.logs.dir_mode is 0755",
		"security.permissions.media.file_mode is 0604",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("OtherAccess = %v, want %v", got, want)
	}
}

// TestPermissionsComplete: a load fills every missing key with its built-in
// default and lists it; a complete block adds nothing.
func TestPermissionsComplete(t *testing.T) {
	p := PermissionsConfig{Logs: AreaPermissions{FileMode: "0640"}}
	added := completePermissions(&p)
	if len(added) != 17 || !slices.Contains(added, "security.permissions.logs.dir_mode") ||
		slices.Contains(added, "security.permissions.logs.file_mode") {
		t.Errorf("added = %v, want the 17 missing keys", added)
	}
	if p.Logs.FileMode != "0640" || p.Bin.FileMode != "0700" || p.Root.DirMode != "0700" {
		t.Errorf("completed block = %+v", p)
	}
	again := p
	if added := completePermissions(&again); len(added) != 0 || again != p {
		t.Errorf("second complete added %v", added)
	}
	if DefaultPermissions() != func() PermissionsConfig { var d PermissionsConfig; completePermissions(&d); return d }() {
		t.Error("DefaultPermissions differs from an empty block completed")
	}
}

// TestLoadConfigPermissions: an invalid mode stops the load and names the
// key; a partial block is completed and its missing keys reported; a file
// with every key reports nothing.
func TestLoadConfigPermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"security":{"permissions":{"logs":{"dir_mode":"4777"}}}}`)
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "security.permissions.logs.dir_mode") {
		t.Fatalf("LoadConfig = %v, want a refusal naming security.permissions.logs.dir_mode", err)
	}

	write(`{"security":{"permissions":{"agents":{"dir_mode":"0750"}}}}`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got := cfg.PermissionModes(AreaAgents); got != (Modes{0o750, 0o600}) {
		t.Errorf("agents modes = %v, want 0750/0600", got)
	}
	added, dropped := cfg.PermissionsAdded()
	if len(dropped) != 0 {
		t.Errorf("dropped = %v, want none", dropped)
	}
	if !slices.Contains(added, "security.permissions.root.file_mode") || slices.Contains(added, "security.permissions.agents.dir_mode") {
		t.Errorf("added = %v", added)
	}

	full, err := json.Marshal(map[string]any{"security": map[string]any{"permissions": DefaultPermissions()}})
	if err != nil {
		t.Fatal(err)
	}
	write(string(full))
	cfg, err = LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if added, dropped := cfg.PermissionsAdded(); len(added)+len(dropped) != 0 {
		t.Errorf("complete file reported %v %v", added, dropped)
	}
}

// TestOldTopLevelPermissionKeys: a config holding only the pre-0.6.4
// top-level dir_mode and file_mode loads with every area at its default (the
// old keys are ignored), and the startup write of the block leaves them out
// of the file.
func TestOldTopLevelPermissionKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"security":{"permissions":{"dir_mode":"0750","file_mode":"0640"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if got := store.Current().Security.Permissions; got != DefaultPermissions() {
		t.Errorf("permissions = %+v, want every area at its default", got)
	}
	added, dropped, err := store.SavePermissionDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 18 || !slices.Equal(dropped, []string{"security.permissions.dir_mode", "security.permissions.file_mode"}) {
		t.Errorf("added %v dropped %v", added, dropped)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Security struct {
			Permissions map[string]any `json:"permissions"`
		} `json:"security"`
	}
	if err = json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"dir_mode", "file_mode"} {
		if _, ok := doc.Security.Permissions[gone]; ok {
			t.Errorf("old top-level %s still in the file", gone)
		}
	}
	if len(doc.Security.Permissions) != 9 {
		t.Errorf("block = %v, want the nine areas", doc.Security.Permissions)
	}
}

// TestSavePermissionDefaults: the store writes the completed block once,
// keeping the default_config marker and secret references, and a reload of
// the written file has nothing left to add.
func TestSavePermissionDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	t.Setenv("CLAW_TEST_PERMS_KEY", "sk-secret")
	body := `{"default_config":true,"gateway":{"tls":{}},"tools":{"web":{"brave":{"api_key":"env:CLAW_TEST_PERMS_KEY"}}},` +
		`"security":{"permissions":{"logs":{"dir_mode":"0750"}}}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	added, dropped, err := store.SavePermissionDefaults()
	if err != nil {
		t.Fatalf("SavePermissionDefaults: %v", err)
	}
	if len(added) != 17 || len(dropped) != 0 {
		t.Errorf("added %v dropped %v", added, dropped)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{`"default_config": true`, `"env:CLAW_TEST_PERMS_KEY"`, `"bin": {`} {
		if !strings.Contains(text, want) {
			t.Errorf("written config lacks %s", want)
		}
	}
	if strings.Contains(text, "sk-secret") {
		t.Error("the secret value was written in place of its reference")
	}
	if added, dropped, saveErr := store.SavePermissionDefaults(); saveErr != nil || len(added)+len(dropped) != 0 {
		t.Errorf("second save: %v %v %v", added, dropped, saveErr)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if added, dropped := cfg.PermissionsAdded(); len(added)+len(dropped) != 0 {
		t.Errorf("written file still lacks %v %v", added, dropped)
	}
	if got := cfg.PermissionModes(AreaLogs).Dir; got != 0o750 {
		t.Errorf("logs dir mode = %04o, want the configured 0750", got)
	}
}

// TestSavePermissionDefaultsOnlyTouchesTheBlock: the startup write changes
// security.permissions and nothing else: a value set only through an env
// variable is not written, an unknown key is kept, and every other key keeps
// its value (no defaults are added).
func TestSavePermissionDefaultsOnlyTouchesTheBlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	t.Setenv("CLAW_TOOLS_WEB_BRAVE_API_KEY", "sk-from-env")
	body := `{
  "zz_unknown": {"kept": [1, 2]},
  "logging": {"level": "warn"},
  "security": {"message_prefix": "hi", "permissions": {"file_mode": "0640", "logs": {"dir_mode": "0750"}}}
}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if store.Current().Tools.Web.Brave.APIKey != "sk-from-env" {
		t.Fatal("env variable not applied at load; the test proves nothing")
	}
	if _, _, err = store.SavePermissionDefaults(); err != nil {
		t.Fatalf("SavePermissionDefaults: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "sk-from-env") {
		t.Error("the env-sourced key was written to config.json")
	}
	var before, after map[string]any
	if err = json.Unmarshal([]byte(body), &before); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &after); err != nil {
		t.Fatal(err)
	}
	obj := func(v any) map[string]any {
		t.Helper()
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("not an object: %v", v)
		}
		return m
	}
	beforeSec, afterSec := obj(before["security"]), obj(after["security"])
	perms := obj(afterSec["permissions"])
	delete(beforeSec, "permissions")
	delete(afterSec, "permissions")
	if !reflect.DeepEqual(before, after) {
		t.Errorf("keys outside security.permissions changed:\nbefore %v\nafter  %v", before, after)
	}
	if _, ok := perms["file_mode"]; ok {
		t.Error("old top-level file_mode kept")
	}
	logs := obj(perms["logs"])
	if logs["dir_mode"] != "0750" || logs["file_mode"] != "0600" {
		t.Errorf("logs = %v, want its own dir_mode and the default file_mode", logs)
	}
	if bin := obj(perms["bin"]); bin["file_mode"] != "0700" {
		t.Errorf("bin = %v, want the default added", bin)
	}
}

// TestStoreUpdateKeepsPermissions: a save that replaces the whole config
// without the block (a raw PUT) writes the full default block back, and one
// that keeps it keeps its values.
func TestStoreUpdateKeepsPermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"security":{"permissions":{"logs":{"dir_mode":"0750","file_mode":"0640"}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Update(func(c *Config) error { c.Logging.Level = "debug"; return nil }); err != nil {
		t.Fatalf("Update: %v", err)
	}
	p, err := ReadPermissions(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Modes(AreaLogs); got != (Modes{0o750, 0o640}) {
		t.Errorf("logs after an unrelated save = %v, want 0750/0640", got)
	}
	if err = store.Update(func(c *Config) error { c.Security.Permissions = PermissionsConfig{}; return nil }); err != nil {
		t.Fatalf("Update: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"secrets": {`) {
		t.Error("a save without the block did not write the defaults back")
	}
}

// TestConfigSaveUsesRootFileMode: config.json is written with exactly
// root.file_mode, whatever the umask.
func TestConfigSaveUsesRootFileMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := DefaultConfig()
	cfg.Security.Permissions.Root.FileMode = "0640"
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o640 {
		t.Errorf("config.json = %04o, want 0640", got)
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
	cfg.Security.Permissions.Secrets = AreaPermissions{DirMode: "0710", FileMode: "0600"}
	private := Modes{Dir: 0o710, File: DefaultFileMode}
	for path, want := range map[string]Modes{
		filepath.Join(home, "agents", "bob"):           area,
		filepath.Join(ws, "sessions"):                  area,
		filepath.Join(home, "internal", "temp", "x"):   private, // the secrets area's
		filepath.Join(os.TempDir(), "claw-temp-agent"): private,
	} {
		if got := cfg.AgentModes(path); got != want {
			t.Errorf("AgentModes(%s) = %v, want %v", path, got, want)
		}
	}
}

// TestPermissionsOwnerFloor: a mode lacking its area's owner access is used
// with that access added (rwx/rw, bin r-x/r--), each such key is warned
// about once at load naming the configured and effective value, and the file
// keeps what the operator wrote. Modes meeting the floor are untouched and
// not warned about.
func TestPermissionsOwnerFloor(t *testing.T) {
	areas := []string{AreaRoot, AreaSecrets, AreaDefault, AreaLogs, AreaAgents, AreaMedia, AreaCommon, AreaBackup}
	for _, area := range areas {
		p := DefaultPermissions()
		a := permAreasByName(&p)[area]
		a.DirMode, a.FileMode = "0050", "0040"
		if got, want := p.Modes(area), (Modes{0o750, 0o640}); got != want {
			t.Errorf("%s: Modes = %v, want %v", area, got, want)
		}
		want := []string{
			"security.permissions." + area + ".dir_mode 0050 lacks owner access; using 0750",
			"security.permissions." + area + ".file_mode 0040 lacks owner access; using 0640",
		}
		if got := p.BelowFloor(); !slices.Equal(got, want) {
			t.Errorf("%s: BelowFloor = %v, want %v", area, got, want)
		}
	}

	bin := DefaultPermissions()
	bin.Bin = AreaPermissions{DirMode: "0500", FileMode: "0400"}
	if got := bin.Modes(AreaBin); got != (Modes{0o500, 0o400}) || len(bin.BelowFloor()) != 0 {
		t.Errorf("bin at its floor: Modes = %v, BelowFloor = %v", got, bin.BelowFloor())
	}
	bin.Bin = AreaPermissions{DirMode: "0000", FileMode: "0000"}
	if got := bin.Modes(AreaBin); got != (Modes{0o500, 0o400}) || len(bin.BelowFloor()) != 2 {
		t.Errorf("bin 0000: Modes = %v, BelowFloor = %v", got, bin.BelowFloor())
	}
	ok := DefaultPermissions()
	ok.Logs = AreaPermissions{DirMode: "0750", FileMode: "0640"}
	if got := ok.BelowFloor(); len(got) != 0 || ok.Modes(AreaLogs) != (Modes{0o750, 0o640}) {
		t.Errorf("modes at the floor: BelowFloor = %v, Modes = %v", got, ok.Modes(AreaLogs))
	}

	// At load: one WARN per key, the file untouched, the effective mode used.
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{"security":{"permissions":{"secrets":{"dir_mode":"0600","file_mode":"0600"}}}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	restore := logger.RedirectForTest(&buf)
	cfg, err := LoadConfig(path)
	restore()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.PermissionModes(AreaSecrets); got != (Modes{0o700, 0o600}) {
		t.Errorf("effective secrets modes = %v, want 0700/0600", got)
	}
	logged := buf.String()
	if n := strings.Count(logged, "lacks owner access"); n != 1 ||
		!strings.Contains(logged, "security.permissions.secrets.dir_mode 0600 lacks owner access; using 0700") {
		t.Errorf("log = %q, want one WARN for secrets.dir_mode", logged)
	}
	if cfg.Security.Permissions.Secrets.DirMode != "0600" {
		t.Errorf("configured value rewritten in memory: %q", cfg.Security.Permissions.Secrets.DirMode)
	}
}

// permAreasByName maps each area name to its pair in p.
func permAreasByName(p *PermissionsConfig) map[string]*AreaPermissions {
	out := map[string]*AreaPermissions{}
	for _, a := range permAreas(p) {
		out[a.name] = a.perm
	}
	return out
}
