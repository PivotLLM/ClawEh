package install

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/app"
	"github.com/PivotLLM/ClawEh/global"
)

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != want {
		t.Errorf("%s: mode %o; want %o", path, got, want)
	}
}

func TestInstallMacApp_WritesBundle(t *testing.T) {
	appsDir := filepath.Join(t.TempDir(), "Applications") // missing: created
	bundle, err := installMacApp(appsDir, "/usr/local/bin/claw", "/Users/alice/.claw")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(appsDir, global.MacAppName+".app"); bundle != want {
		t.Fatalf("bundle %q; want %q", bundle, want)
	}
	contents := filepath.Join(bundle, "Contents")
	for _, dir := range []string{appsDir, bundle, contents, filepath.Join(contents, "MacOS"), filepath.Join(contents, "Resources")} {
		assertMode(t, dir, 0o755)
	}
	plistPath := filepath.Join(contents, "Info.plist")
	launcherPath := filepath.Join(contents, "MacOS", "launcher")
	iconPath := filepath.Join(contents, "Resources", "AppIcon.icns")
	assertMode(t, plistPath, 0o644)
	assertMode(t, launcherPath, 0o755)
	assertMode(t, iconPath, 0o644)

	plist, err := os.ReadFile(plistPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"<key>CFBundleIdentifier</key>\n\t<string>" + global.MacAppBundleID + "</string>",
		"<key>CFBundleName</key>\n\t<string>" + global.MacAppName + "</string>",
		"<key>CFBundleDisplayName</key>\n\t<string>" + global.MacAppName + "</string>",
		"<key>CFBundleExecutable</key>\n\t<string>launcher</string>",
		"<key>CFBundleIconFile</key>\n\t<string>AppIcon</string>",
		"<key>CFBundlePackageType</key>\n\t<string>APPL</string>",
		"<key>CFBundleShortVersionString</key>\n\t<string>" + app.SemVer() + "</string>",
		"<key>CFBundleVersion</key>\n\t<string>" + app.SemVer() + "</string>",
	} {
		if !strings.Contains(string(plist), want) {
			t.Errorf("Info.plist lacks %q:\n%s", want, plist)
		}
	}
	if strings.Contains(string(plist), "LSUIElement") {
		t.Error("Info.plist sets LSUIElement")
	}

	launcher, err := os.ReadFile(launcherPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "#!/bin/sh\n" +
		"# Opens the WebUI in the browser. Written by the installer.\n" +
		"CLAW_HOME='/Users/alice/.claw' exec '/usr/local/bin/claw' webui\n"
	if string(launcher) != want {
		t.Errorf("launcher =\n%s\nwant\n%s", launcher, want)
	}

	icon, err := os.ReadFile(iconPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(macAppIcon) == 0 || !bytes.Equal(icon, macAppIcon) {
		t.Error("the icon is not the embedded one")
	}
	if !isOurMacApp(bundle) {
		t.Error("the bundle written is not recognised as ours")
	}
}

// TestInstallMacApp_LauncherQuoting runs the launcher with /bin/sh against a
// stand-in binary whose path, like the data directory's, holds a space and
// a single quote.
func TestInstallMacApp_LauncherQuoting(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "Alice's bin")
	if err := os.Mkdir(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	targetBin := filepath.Join(binDir, "claw")
	stub := "#!/bin/sh\nprintf '%s\\n%s\\n' \"$CLAW_HOME\" \"$*\"\n"
	if err := os.WriteFile(targetBin, []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	clawHome := filepath.Join(root, "Bob's data", ".claw")

	bundle, err := installMacApp(filepath.Join(root, "Applications"), targetBin, clawHome)
	if err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(bundle, "Contents", "MacOS", "launcher")
	text, err := os.ReadFile(launcher)
	if err != nil {
		t.Fatal(err)
	}
	wantLine := `CLAW_HOME='` + strings.ReplaceAll(clawHome, "'", `'"'"'`) + `' exec '` +
		strings.ReplaceAll(targetBin, "'", `'"'"'`) + `' webui`
	if !strings.Contains(string(text), wantLine+"\n") {
		t.Errorf("launcher =\n%s\nwant the line\n%s", text, wantLine)
	}

	cmd := exec.Command("/bin/sh", launcher)
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("running the launcher: %v", err)
	}
	if want := clawHome + "\nwebui\n"; string(out) != want {
		t.Errorf("the binary saw %q; want %q", out, want)
	}
}

func TestInstallMacApp_ReplacesOurs(t *testing.T) {
	appsDir := t.TempDir()
	bundle, err := installMacApp(appsDir, "/usr/local/bin/claw", "/Users/alice/.claw")
	if err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(bundle, "Contents", "Resources", "stray.txt")
	if err = os.WriteFile(stray, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = installMacApp(appsDir, "/opt/claw/bin/claw", "/opt/claw"); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(stray); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stray file: %v; want it gone", err)
	}
	launcher, err := os.ReadFile(filepath.Join(bundle, "Contents", "MacOS", "launcher"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(launcher), "CLAW_HOME='/opt/claw' exec '/opt/claw/bin/claw' webui") {
		t.Errorf("launcher not rewritten:\n%s", launcher)
	}
}

// foreignApp is something that is not ours at the app's path: path holds
// data in the Applications folder appsDir.
type foreignApp struct {
	name    string
	appsDir string
	path    string
	data    []byte
}

// foreignApps puts a folder that is not ours and, in a second Applications
// folder, a plain file at the app's path.
func foreignApps(t *testing.T) []foreignApp {
	t.Helper()
	root := t.TempDir()
	folder := foreignApp{name: "folder", appsDir: filepath.Join(root, "folder"), data: []byte("<plist>com.example.bob</plist>")}
	folder.path = filepath.Join(macAppPath(folder.appsDir), "Contents", "Info.plist")
	file := foreignApp{name: "file", appsDir: filepath.Join(root, "file"), data: []byte("Bob's file")}
	file.path = macAppPath(file.appsDir)
	if err := os.MkdirAll(filepath.Dir(folder.path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(file.appsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []foreignApp{folder, file} {
		if err := os.WriteFile(f.path, f.data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return []foreignApp{folder, file}
}

// assertUntouched checks f still holds its content and nothing was added
// beside it.
func assertUntouched(t *testing.T, f foreignApp) {
	t.Helper()
	got, err := os.ReadFile(f.path)
	if err != nil || !bytes.Equal(got, f.data) {
		t.Errorf("%s: %q, %v; want %q untouched", f.path, got, err, f.data)
	}
	entries, err := os.ReadDir(filepath.Dir(f.path))
	if err != nil || len(entries) != 1 {
		t.Errorf("%s: %v, %v; want only %s", filepath.Dir(f.path), entries, err, filepath.Base(f.path))
	}
}

func TestInstallMacApp_LeavesForeign(t *testing.T) {
	for _, f := range foreignApps(t) {
		t.Run(f.name, func(t *testing.T) {
			_, err := installMacApp(f.appsDir, "/usr/local/bin/claw", "/Users/alice/.claw")
			if err == nil || !strings.Contains(err.Error(), "is not this app; left as it is") {
				t.Errorf("install = %v; want the refusal", err)
			}
			assertUntouched(t, f)
		})
	}
}

func TestUninstallMacApp(t *testing.T) {
	t.Run("ours", func(t *testing.T) {
		appsDir := t.TempDir()
		bundle, err := installMacApp(appsDir, "/usr/local/bin/claw", "/Users/alice/.claw")
		if err != nil {
			t.Fatal(err)
		}
		removed, err := uninstallMacApp(appsDir)
		if err != nil || removed != bundle {
			t.Fatalf("uninstall = %q, %v; want %q", removed, err, bundle)
		}
		if _, err := os.Lstat(bundle); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("bundle: %v; want it gone", err)
		}
	})
	t.Run("missing", func(t *testing.T) {
		removed, err := uninstallMacApp(t.TempDir())
		if err != nil || removed != "" {
			t.Errorf("uninstall = %q, %v; want nothing", removed, err)
		}
	})
	for _, f := range foreignApps(t) {
		t.Run("foreign "+f.name, func(t *testing.T) {
			removed, err := uninstallMacApp(f.appsDir)
			if err != nil || removed != "" {
				t.Errorf("uninstall = %q, %v; want nothing", removed, err)
			}
			assertUntouched(t, f)
		})
	}
}

func TestDefaultApplicationsDir(t *testing.T) {
	if got := defaultApplicationsDir(&TargetUser{Username: "alice", HomeDir: "/Users/alice", IsRoot: true}); got != "/Applications" {
		t.Errorf("system install: %q; want /Applications", got)
	}
	if got := defaultApplicationsDir(&TargetUser{Username: "alice", HomeDir: "/Users/alice"}); got != "/Users/alice/Applications" {
		t.Errorf("user install: %q; want /Users/alice/Applications", got)
	}
}

// snapshot maps every path under dir (relative) to its content, "dir" for a
// folder and "-> target" for a link.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		switch {
		case d.Type()&os.ModeSymlink != 0:
			target, lerr := os.Readlink(path)
			if lerr != nil {
				return lerr
			}
			files[rel] = "-> " + target
		case d.IsDir():
			files[rel] = "dir"
		default:
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			files[rel] = string(data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func assertSameSnapshot(t *testing.T, dir string, want map[string]string) {
	t.Helper()
	got := snapshot(t, dir)
	if len(got) != len(want) {
		t.Errorf("%s holds %d entries; want %d: %v", dir, len(got), len(want), got)
	}
	for path, data := range want {
		if got[path] != data {
			t.Errorf("%s/%s changed", dir, path)
		}
	}
}

// readOnlyApps makes appsDir read-only for the test, so the private build
// folder cannot be created in it.
func readOnlyApps(t *testing.T, appsDir string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores folder permissions")
	}
	if err := os.Chmod(appsDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(appsDir, 0o755); err != nil {
			t.Error(err)
		}
	})
}

func TestInstallMacApp_FailureLeavesNothing(t *testing.T) {
	appsDir := t.TempDir()
	readOnlyApps(t, appsDir)
	if _, err := installMacApp(appsDir, "/usr/local/bin/claw", "/Users/alice/.claw"); err == nil {
		t.Fatal("install into a read-only folder succeeded")
	}
	entries, err := os.ReadDir(appsDir)
	if err != nil || len(entries) != 0 {
		t.Errorf("Applications holds %v, %v; want nothing (no bundle, no build folder)", entries, err)
	}
}

func TestInstallMacApp_FailedReinstallKeepsOld(t *testing.T) {
	appsDir := t.TempDir()
	if _, err := installMacApp(appsDir, "/usr/local/bin/claw", "/Users/alice/.claw"); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, appsDir)
	readOnlyApps(t, appsDir)
	if _, err := installMacApp(appsDir, "/opt/claw/bin/claw", "/opt/claw"); err == nil {
		t.Fatal("install into a read-only folder succeeded")
	}
	assertSameSnapshot(t, appsDir, before)
}

func TestMacApp_SymlinkAtBundlePath(t *testing.T) {
	root := t.TempDir()
	elsewhere := filepath.Join(root, "elsewhere")
	ours, err := installMacApp(elsewhere, "/usr/local/bin/claw", "/Users/alice/.claw")
	if err != nil {
		t.Fatal(err)
	}
	appsDir := filepath.Join(root, "Applications")
	if err = os.Mkdir(appsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(ours, macAppPath(appsDir)); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, root)

	if _, err = installMacApp(appsDir, "/opt/claw/bin/claw", "/opt/claw"); err == nil || !strings.Contains(err.Error(), "is not this app") {
		t.Errorf("install = %v; want the refusal", err)
	}
	assertSameSnapshot(t, root, before)
	if removed, uerr := uninstallMacApp(appsDir); uerr != nil || removed != "" {
		t.Errorf("uninstall = %q, %v; want nothing", removed, uerr)
	}
	assertSameSnapshot(t, root, before)
}

func TestIsOurMacApp_PlistLinkedOutside(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "Info.plist")
	if err := os.WriteFile(outside, []byte(macAppInfoPlist()), 0o600); err != nil {
		t.Fatal(err)
	}
	bundle := macAppPath(filepath.Join(root, "Applications"))
	if err := os.MkdirAll(filepath.Join(bundle, "Contents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(bundle, "Contents", "Info.plist")); err != nil {
		t.Fatal(err)
	}
	if isOurMacApp(bundle) {
		t.Error("a bundle whose Info.plist links outside it counted as ours")
	}
}

func TestIsOurMacApp_Identifier(t *testing.T) {
	for _, tc := range []struct {
		name  string
		plist string
		want  bool
	}{
		{"ours", macAppInfoPlist(), true},
		{"whitespace between key and string", "<dict><key>CFBundleIdentifier</key>\n\t \n<string>" + global.MacAppBundleID + "</string></dict>", true},
		{"only in another key", "<dict><key>CFBundleIdentifier</key><string>com.example.bob</string>" +
			"<key>CFBundleName</key><string>" + global.MacAppBundleID + "</string></dict>", false},
		{"longer identifier", "<dict><key>CFBundleIdentifier</key><string>" + global.MacAppBundleID + ".helper</string></dict>", false},
		{"identifier in a comment", "<!-- " + global.MacAppBundleID + " --><dict></dict>", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bundle := macAppPath(t.TempDir())
			if err := os.MkdirAll(filepath.Join(bundle, "Contents"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(bundle, "Contents", "Info.plist"), []byte(tc.plist), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := isOurMacApp(bundle); got != tc.want {
				t.Errorf("isOurMacApp = %v; want %v", got, tc.want)
			}
		})
	}
}

// A build that cannot finish is an error, and the cleanup installMacApp runs
// after it removes the private folder and hands the error back.
func TestBuildMacApp_FailurePartWay(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "build")
	if err := os.Mkdir(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	// A file where the Contents folder goes.
	if err := os.WriteFile(filepath.Join(tmp, "Contents"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	buildErr := buildMacApp(tmp, "/Users/alice/bin/app", "/Users/alice/data")
	if buildErr == nil {
		t.Fatal("buildMacApp succeeded over an obstacle")
	}

	if got := removeMacAppTemp(tmp, buildErr); !errors.Is(got, buildErr) {
		t.Fatalf("removeMacAppTemp returned %v, want the build error %v", got, buildErr)
	}
	if _, err := os.Lstat(tmp); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the private folder is still there: %v", err)
	}
}

// buildMacApp writes only into the folder it was given, never through a
// link put in its place.
func TestBuildMacApp_RefusesLink(t *testing.T) {
	dir := t.TempDir()
	elsewhere := filepath.Join(dir, "elsewhere")
	if err := os.Mkdir(elsewhere, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "build")
	if err := os.Symlink(elsewhere, link); err != nil {
		t.Fatal(err)
	}

	if err := buildMacApp(link, "/Users/alice/bin/app", "/Users/alice/data"); err == nil {
		t.Fatal("buildMacApp wrote through a link")
	}
	entries, err := os.ReadDir(elsewhere)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("the link's target gained %d entries", len(entries))
	}
}
