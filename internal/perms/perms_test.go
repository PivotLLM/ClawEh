//go:build !windows

/******************************************************************************
 * Copyright (c) 2026 Tenebris Technologies Inc.                              *
 * Please see LICENSE file for details.                                       *
 ******************************************************************************/

package perms

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture lays out a data directory with every kind of file the walk must
// tighten and every kind it must leave alone, all created 0644 under a 0755
// directory. It returns the directory and the config path (0600 unless the
// caller loosens it).
func fixture(t *testing.T) (dir, config string) {
	t.Helper()
	dir = t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, rel := range append(sensitiveFiles, harmlessFiles...) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		// Umask may have narrowed the create mode; pin it.
		if err := os.Chmod(p, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A symlink named like a database, pointing at a harmless file: the walk
	// must neither follow it nor chmod through it.
	if err := os.Symlink(filepath.Join(dir, "agents/alice/notes.md"), filepath.Join(dir, "link.db")); err != nil {
		t.Fatal(err)
	}
	config = filepath.Join(dir, "config.json")
	if err := os.WriteFile(config, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(config, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, config
}

var sensitiveFiles = []string{
	"state/gateway.db",
	"state/gateway.db-wal",
	"state/gateway.db-shm",
	"state/fusion-tokens.db",
	"state/devices.json",
	"credentials.json",
	"tokens/webui",
	"tokens/nested/service",
	"tls/server.key",
	"agents/alice/sessions/archive.sqlite3",
	"agents/alice/workspace/my-secret.txt",
	"agents/bob/API_TOKEN",
}

var harmlessFiles = []string{
	"media/attachment.db",   // media tree is skipped
	"logs/token.log",        // logs tree is skipped
	"agents/alice/notes.md", // no sensitive pattern
	"tls/server.crt",        // certs are public
	"state/nested/x.json",   // only state/*.json, one level
	"agents/alice/logs/token.log",
	"agents/alice/workspace/media/secret.db",
}

func mode(t *testing.T, p string) os.FileMode {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

type logRec struct {
	msgs []string
}

func (l *logRec) log(msg string, fields map[string]any) {
	if path, ok := fields["path"].(string); ok {
		msg += " " + path
	}
	l.msgs = append(l.msgs, msg)
}

func TestEnforceTightensSensitiveFilesAndDir(t *testing.T) {
	dir, config := fixture(t)
	var rec logRec

	if err := Enforce(dir, config, rec.log); err != nil {
		t.Fatalf("Enforce: %v", err)
	}

	if got := mode(t, dir); got != 0o700 {
		t.Errorf("data dir mode = %04o, want 0700", got)
	}
	for _, rel := range sensitiveFiles {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if got := mode(t, p); got != 0o600 {
			t.Errorf("%s mode = %04o, want 0600", rel, got)
		}
	}
	for _, rel := range harmlessFiles {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if got := mode(t, p); got != 0o644 {
			t.Errorf("%s mode = %04o, want untouched 0644", rel, got)
		}
	}
	// The symlink's target is harmless and must not have been chmod'ed via the link.
	if got := mode(t, filepath.Join(dir, "agents/alice/notes.md")); got != 0o644 {
		t.Errorf("symlink target mode = %04o, want 0644: the walk followed link.db", got)
	}

	wantLogged := len(sensitiveFiles) + 1 // files plus the directory
	if len(rec.msgs) != wantLogged {
		t.Errorf("logged %d changes, want %d:\n%s", len(rec.msgs), wantLogged, strings.Join(rec.msgs, "\n"))
	}

	// Second run: nothing left to do, nothing logged.
	rec.msgs = nil
	if err := Enforce(dir, config, rec.log); err != nil {
		t.Fatalf("second Enforce: %v", err)
	}
	if len(rec.msgs) != 0 {
		t.Errorf("second run logged %d changes, want 0: %v", len(rec.msgs), rec.msgs)
	}
}

func TestEnforceRefusesLooseConfig(t *testing.T) {
	for _, m := range []os.FileMode{0o644, 0o640, 0o604, 0o660} {
		t.Run(m.String(), func(t *testing.T) {
			dir, config := fixture(t)
			if err := os.Chmod(config, m); err != nil {
				t.Fatal(err)
			}
			err := Enforce(dir, config, nil)
			if err == nil {
				t.Fatalf("Enforce accepted a %04o config", m)
			}
			want := "chmod 600 " + config
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not carry the fix %q", err, want)
			}
			// Refusal happens before the walk: nothing else was changed.
			if got := mode(t, filepath.Join(dir, "state/gateway.db")); got != 0o644 {
				t.Errorf("gateway.db mode = %04o after a refused start, want 0644", got)
			}
			// The config itself is never chmod'ed on the operator's behalf.
			if got := mode(t, config); got != m {
				t.Errorf("config mode = %04o, want %04o left alone", got, m)
			}
		})
	}
}

func TestEnforceMissingConfigAndDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fresh")
	if err := Enforce(dir, filepath.Join(dir, "config.json"), nil); err != nil {
		t.Fatalf("Enforce on a fresh install: %v", err)
	}
	if got := mode(t, dir); got != 0o700 {
		t.Errorf("created data dir mode = %04o, want 0700", got)
	}
}

func TestCheckReportsWithoutChanging(t *testing.T) {
	dir, config := fixture(t)
	if err := os.Chmod(config, 0o644); err != nil {
		t.Fatal(err)
	}

	findings, err := Check(dir, config)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	got := map[string]Finding{}
	for _, f := range findings {
		got[f.Path] = f
	}

	want := make([]string, 0, 2+len(sensitiveFiles))
	want = append(want, dir, config)
	for _, rel := range sensitiveFiles {
		want = append(want, filepath.Join(dir, filepath.FromSlash(rel)))
	}
	for _, p := range want {
		f, ok := got[p]
		if !ok {
			t.Errorf("missing finding for %s", p)
			continue
		}
		if f.Want&0o077 != 0 {
			t.Errorf("%s: Want %04o still grants group/other", p, f.Want)
		}
		delete(got, p)
	}
	for p := range got {
		t.Errorf("unexpected finding %s", p)
	}

	// Report-only: every mode is as the fixture left it.
	if got := mode(t, dir); got != 0o755 {
		t.Errorf("Check changed the data dir to %04o", got)
	}
	if got := mode(t, filepath.Join(dir, "state/gateway.db")); got != 0o644 {
		t.Errorf("Check changed gateway.db to %04o", got)
	}
}

func TestCheckCleanInstall(t *testing.T) {
	dir, config := fixture(t)
	if err := Enforce(dir, config, nil); err != nil {
		t.Fatal(err)
	}
	findings, err := Check(dir, config)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("Check after Enforce found %d offenders: %+v", len(findings), findings)
	}
}

func TestWalkCap(t *testing.T) {
	dir, config := fixture(t)
	old := walkLimit
	walkLimit = 3
	t.Cleanup(func() { walkLimit = old })

	findings, err := Check(dir, config)
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("Check err = %v, want ErrTruncated", err)
	}
	// dir and config are reported regardless; the walk itself stopped early.
	if len(findings) < 1 || len(findings) > 3+1 {
		t.Errorf("got %d findings under a cap of 3, want between 1 and 4", len(findings))
	}

	var rec logRec
	if err := Enforce(dir, config, rec.log); err != nil {
		t.Fatalf("Enforce under cap: %v", err)
	}
	if !strings.Contains(strings.Join(rec.msgs, "\n"), "entry cap") {
		t.Errorf("Enforce did not log the truncation: %v", rec.msgs)
	}
}

func TestEnsurePrivateFile(t *testing.T) {
	dir := t.TempDir()

	fresh := filepath.Join(dir, "fresh.db")
	if err := EnsurePrivateFile(fresh); err != nil {
		t.Fatalf("fresh: %v", err)
	}
	if got := mode(t, fresh); got != 0o600 {
		t.Errorf("fresh file mode = %04o, want 0600", got)
	}
	if fi, err := os.Stat(fresh); err != nil || fi.Size() != 0 {
		t.Errorf("fresh file: size=%d err=%v, want an empty file", fi.Size(), err)
	}

	existing := filepath.Join(dir, "existing.db")
	if err := os.WriteFile(existing, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(existing, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsurePrivateFile(existing); err != nil {
		t.Fatalf("existing: %v", err)
	}
	if got := mode(t, existing); got != 0o600 {
		t.Errorf("existing file mode = %04o, want 0600", got)
	}
	if data, err := os.ReadFile(existing); err != nil || string(data) != "payload" {
		t.Errorf("existing file content = %q err=%v, want untouched payload", data, err)
	}
}

func TestIsSensitive(t *testing.T) {
	cases := map[string]bool{
		"state/gateway.db":           true,
		"state/gateway.db-wal":       true,
		"state/gateway.db-shm":       true,
		"x/y/z.sqlite":               true,
		"x/y/z.sqlite3":              true,
		"x/y/z.sqlite-wal":           true,
		"credentials.json":           true,
		"deep/credentials.json":      true,
		"state/devices.json":         true,
		"state/deeper/devices.json":  false,
		"state/notes.txt":            false,
		"tokens/anything":            true,
		"tokens/sub/anything":        true,
		"tls/server.key":             true,
		"tls/server.crt":             false,
		"agents/alice/Secret-Notes":  true,
		"agents/alice/api_token.txt": true,
		"agents/alice/notes.md":      false,
	}
	for rel, want := range cases {
		if got := isSensitive(filepath.FromSlash(rel)); got != want {
			t.Errorf("isSensitive(%q) = %v, want %v", rel, got, want)
		}
	}
}
