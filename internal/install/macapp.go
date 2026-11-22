package install

import (
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/PivotLLM/ClawEh/app"
	"github.com/PivotLLM/ClawEh/fileutil"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/utils"
)

// macAppIcon is the icon of the app `install` adds to Applications on macOS.
//
//go:embed macapp/AppIcon.icns
var macAppIcon []byte

const (
	macAppDirMode  os.FileMode = 0o755
	macAppFileMode os.FileMode = 0o644
	macAppExecMode os.FileMode = 0o755
	// macAppExecutable is the launcher script's name in Contents/MacOS.
	macAppExecutable = "launcher"
	// macAppIconName is the icon's name in Contents/Resources, without .icns.
	macAppIconName = "AppIcon"
)

// applicationsDir is the Applications folder the app goes in for tu. It is
// replaced in tests.
var applicationsDir = defaultApplicationsDir

// defaultApplicationsDir is /Applications for a system install, else the
// person's own Applications folder.
func defaultApplicationsDir(tu *TargetUser) string {
	if tu.IsRoot {
		return "/Applications"
	}
	return filepath.Join(tu.HomeDir, "Applications")
}

// macAppPath is the app's bundle in appsDir.
func macAppPath(appsDir string) string {
	return filepath.Join(appsDir, global.MacAppName+".app")
}

// macAppPlistMax bounds how much of an Info.plist isOurMacApp reads.
const macAppPlistMax = 64 << 10

// macAppIdentifier matches the CFBundleIdentifier entry naming exactly
// global.MacAppBundleID.
var macAppIdentifier = regexp.MustCompile(`<key>CFBundleIdentifier</key>\s*<string>` +
	regexp.QuoteMeta(global.MacAppBundleID) + `</string>`)

// isOurMacApp reports whether bundle is a folder (not a link) whose
// Contents/Info.plist, a regular file inside it, gives global.MacAppBundleID
// as its CFBundleIdentifier.
func isOurMacApp(bundle string) bool {
	fi, err := os.Lstat(bundle)
	if err != nil || !fi.IsDir() {
		return false
	}
	root, err := os.OpenRoot(bundle)
	if err != nil {
		return false
	}
	defer utils.CloseQuietly(root)
	f, err := root.OpenFile(filepath.Join("Contents", "Info.plist"), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return false
	}
	defer utils.CloseQuietly(f)
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return false
	}
	data, err := io.ReadAll(io.LimitReader(f, macAppPlistMax))
	return err == nil && macAppIdentifier.Match(data)
}

// errNotOurMacApp is the refusal for something else at the app's path.
func errNotOurMacApp(bundle string) error {
	return fmt.Errorf("%s is not this app; left as it is", bundle)
}

// installMacApp writes the app into appsDir, replacing an earlier bundle of
// ours, and returns its path. Anything else at that path is left as it is
// and is an error. The bundle is built in a private folder in appsDir and
// renamed into place, so a failure leaves the earlier app as it was and
// nothing written through a path another account could change.
func installMacApp(appsDir, targetBin, clawHome string) (string, error) {
	bundle := macAppPath(appsDir)
	if err := fileutil.MkdirAllExact(appsDir, macAppDirMode); err != nil {
		return "", err
	}
	if err := checkMacAppTarget(bundle); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(appsDir, "."+global.MacAppName+"-*")
	if err != nil {
		return "", err
	}
	if err := buildMacApp(tmp, targetBin, clawHome); err != nil {
		return "", removeMacAppTemp(tmp, err)
	}
	if err := checkMacAppTarget(bundle); err != nil {
		return "", removeMacAppTemp(tmp, err)
	}
	if isOurMacApp(bundle) {
		if err := os.RemoveAll(bundle); err != nil {
			return "", removeMacAppTemp(tmp, err)
		}
	}
	if err := os.Rename(tmp, bundle); err != nil {
		return "", removeMacAppTemp(tmp, err)
	}
	return bundle, nil
}

// checkMacAppTarget refuses a bundle path holding something not ours.
func checkMacAppTarget(bundle string) error {
	if _, err := os.Lstat(bundle); err == nil {
		if !isOurMacApp(bundle) {
			return errNotOurMacApp(bundle)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// removeMacAppTemp removes the private build folder after err.
func removeMacAppTemp(tmp string, err error) error {
	if rmErr := os.RemoveAll(tmp); rmErr != nil {
		return errors.Join(err, rmErr)
	}
	return err
}

// buildMacApp writes the bundle's contents into tmp, the folder MkdirTemp
// just created, through an os.Root opened on it after checking it is that
// folder, and gives tmp the bundle's folder mode last.
func buildMacApp(tmp, targetBin, clawHome string) error {
	root, err := os.OpenRoot(tmp)
	if err != nil {
		return err
	}
	defer utils.CloseQuietly(root)
	named, err := os.Lstat(tmp)
	if err != nil {
		return err
	}
	opened, err := root.Stat(".")
	if err != nil {
		return err
	}
	if !named.IsDir() || !os.SameFile(named, opened) {
		return fmt.Errorf("%s changed while it was being written", tmp)
	}

	for _, dir := range []string{"Contents", filepath.Join("Contents", "MacOS"), filepath.Join("Contents", "Resources")} {
		if err := root.Mkdir(dir, macAppDirMode); err != nil {
			return err
		}
		if err := root.Chmod(dir, macAppDirMode); err != nil {
			return err
		}
	}
	files := []struct {
		name string
		data []byte
		mode os.FileMode
	}{
		{filepath.Join("Contents", "Info.plist"), []byte(macAppInfoPlist()), macAppFileMode},
		{filepath.Join("Contents", "MacOS", macAppExecutable), []byte(macAppLauncher(targetBin, clawHome)), macAppExecMode},
		{filepath.Join("Contents", "Resources", macAppIconName+".icns"), macAppIcon, macAppFileMode},
	}
	for _, f := range files {
		if err := root.WriteFile(f.name, f.data, f.mode); err != nil {
			return err
		}
		if err := root.Chmod(f.name, f.mode); err != nil {
			return err
		}
	}
	return root.Chmod(".", macAppDirMode)
}

// uninstallMacApp removes the app from appsDir when it is ours and returns
// the path removed, or "" when there was none of ours.
func uninstallMacApp(appsDir string) (string, error) {
	bundle := macAppPath(appsDir)
	if !isOurMacApp(bundle) {
		return "", nil
	}
	if err := os.RemoveAll(bundle); err != nil {
		return "", err
	}
	return bundle, nil
}

// macAppInfoPlist is the app's Contents/Info.plist.
func macAppInfoPlist() string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n")
	b.WriteString("<dict>\n")
	for _, kv := range [][2]string{
		{"CFBundleDevelopmentRegion", "en"},
		{"CFBundleDisplayName", global.MacAppName},
		{"CFBundleExecutable", macAppExecutable},
		{"CFBundleIconFile", macAppIconName},
		{"CFBundleIdentifier", global.MacAppBundleID},
		{"CFBundleInfoDictionaryVersion", "6.0"},
		{"CFBundleName", global.MacAppName},
		{"CFBundlePackageType", "APPL"},
		{"CFBundleShortVersionString", app.SemVer()},
		{"CFBundleVersion", app.SemVer()},
	} {
		fmt.Fprintf(&b, "\t<key>%s</key>\n\t<string>%s</string>\n", kv[0], kv[1])
	}
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

// macAppLauncher is the app's executable: a script that runs the installed
// binary's webui command against the install's data directory.
func macAppLauncher(targetBin, clawHome string) string {
	return "#!/bin/sh\n" +
		"# Opens the WebUI in the browser. Written by the installer.\n" +
		global.EnvVarHome + "=" + shellQuote(clawHome) + " exec " + shellQuote(targetBin) + " webui\n"
}
