// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// productWord matches the product name or short name used as a word in a
// string: "ClawEh", "Claw", "claw" — but not inside a longer identifier such as
// CLAW_HOME, mcp__claw__ or openclaw.
var productWord = regexp.MustCompile(`(?i)(^|[^a-z0-9_])claw(eh)?([^a-z0-9_]|$)`)

// fixedIdentifiers are names that contain the product's name but are not its
// label: real files, directories, services, protocol keys and repository
// paths that installs and other programs depend on. They keep their value
// when the product is renamed, so a string may carry them. Each is removed
// from a string before productWord is checked.
//
// To allow a new one, add the narrowest pattern that matches only the
// identifier (never a bare "claw"), with a comment saying what it names. A
// bare-word identifier (the binary file name, the systemd unit) is not
// allowed here: define it in global/identifiers.go, the one file this guard
// skips.
var fixedIdentifiers = []*regexp.Regexp{
	// Module path and repositories under the PivotLLM organisation
	// (github.com/PivotLLM/ClawEh, PivotLLM/ClawEh-skills).
	regexp.MustCompile(`PivotLLM/ClawEh[\w-]*`),
	// The macOS launchd job label and its plist.
	regexp.MustCompile(`com\.pivotllm\.claweh(\.plist)?`),
	// The data directory (~/.claw, a relative .claw) and the hidden temp
	// names made in it (.claw-backup-*, .claw-perm-test-*).
	regexp.MustCompile(`\.claw(-[\w*%-]+)?`),
	// The system-wide data directory.
	regexp.MustCompile(`/opt/claw\b`),
	// File names: the log, pid and lock files and the systemd unit.
	regexp.MustCompile(`\bclaw\.(log|pid|lock|service)\b`),
	// The companion OAuth helper binary, and file, cookie and protocol names
	// built on a "claw-" prefix: temp dirs and archives (claw-temp-agents,
	// claw-backup-<ts>.tar.gz, claw-upgrade-*, claw-dl-*, claw-launchd.log),
	// the WebSocket token subprotocol (claw-token) and the MCP bearer realm
	// (claw-mcp).
	regexp.MustCompile(`\bclaw-(auth|temp-agents|backup|upgrade|dl|launchd|token|mcp)\b`),
	// The names `upgrade` extracts a release archive's binaries to.
	regexp.MustCompile(`\bextracted-claw\b`),
}

// guardSkipFile is the one file of fixed identifiers that are bare words
// (global/identifiers.go); every value in it is documented there.
const guardSkipFile = "global/identifiers.go"

// TestProductName_OnlyInApp enforces that the product name lives only in this
// package: no non-test Go file elsewhere may hold "ClawEh", "Claw" or "claw"
// as a word in a string literal. Use Name, ShortName or BinaryName instead; a
// rename is then an edit to app.go only. Comments, struct tags and import
// paths are not checked.
func TestProductName_OnlyInApp(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	var hits []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != "." && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules" ||
				rel == "app" || rel == "web/frontend" || rel == "manual") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") || rel == guardSkipFile {
			return nil
		}
		hits = append(hits, productNameLiterals(t, path, rel)...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		t.Error(h)
	}
	if len(hits) > 0 {
		t.Log("Use app.Name() to identify the product, app.ShortName() in prose and " +
			"app.BinaryName() in commands to type. A real file, service or protocol name " +
			"belongs in fixedIdentifiers or global/identifiers.go.")
	}
}

// productNameLiterals returns "file:line: text" for each string literal in the
// file that names the product outside the fixed identifiers.
func productNameLiterals(t *testing.T, path, rel string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}
	// Struct tags are JSON keys and the like, not prose.
	tags := map[*ast.BasicLit]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		if fld, ok := n.(*ast.Field); ok && fld.Tag != nil {
			tags[fld.Tag] = true
		}
		return true
	})
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.ImportSpec:
			return false
		case *ast.BasicLit:
			if x.Kind != token.STRING || tags[x] {
				return true
			}
			s, err := strconv.Unquote(x.Value)
			if err == nil && namesProduct(s) {
				out = append(out, rel+":"+strconv.Itoa(fset.Position(x.Pos()).Line)+": "+strconv.Quote(s))
			}
		}
		return true
	})
	return out
}

// namesProduct reports whether s names the product once the fixed
// identifiers are taken out.
func namesProduct(s string) bool {
	for _, re := range fixedIdentifiers {
		s = re.ReplaceAllString(s, " ")
	}
	return productWord.MatchString(s)
}

// TestNamesProduct pins the guard's matching, so a loosened allow-list or
// pattern shows up here rather than as a silent gap.
func TestNamesProduct(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"ClawEh", true},
		{"Claw is shutting down", true},
		{"run: claw admin", true},
		{"claw's own state", true},
		{"restart claw.", true},
		{"collides with claw-managed fields", true},
		{"CLAW_HOME", false},
		{"mcp__claw__*", false},
		{"openclaw", false},
		{"clawhub.ai", false},
		{"clawdbot-gateway", false},
		{"github.com/PivotLLM/ClawEh/app", false},
		{"PivotLLM/ClawEh-skills/weather", false},
		{"com.pivotllm.claweh.plist", false},
		{"~/.claw/config.json", false},
		{".claw-backup-*", false},
		{"/opt/claw", false},
		{"logs/claw.log", false},
		{"claw.pid", false},
		{"/etc/systemd/system/claw.service", false},
		{"claw-auth version %s", false},
		{"claw-backup-<timestamp>.tar.gz", false},
		{"Bearer realm=\"claw-mcp\"", false},
		{"extracted-claw-auth", false},
		{"claw-auth is a helper for claw", true},
	}
	for _, tt := range tests {
		if got := namesProduct(tt.in); got != tt.want {
			t.Errorf("namesProduct(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
