// ClawEh
// License: MIT

package admin

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHashPassword_RoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=65536,t=3,p=1$") {
		t.Fatalf("unexpected PHC prefix: %s", hash)
	}
	ok, err := VerifyPassword(hash, "correct horse battery")
	if err != nil || !ok {
		t.Fatalf("VerifyPassword(correct) = %v, %v; want true, nil", ok, err)
	}
	ok, err = VerifyPassword(hash, "correct horse batterY")
	if err != nil || ok {
		t.Fatalf("VerifyPassword(wrong) = %v, %v; want false, nil", ok, err)
	}
}

func TestHashPassword_SaltIsFresh(t *testing.T) {
	a, err := HashPassword("same password here")
	if err != nil {
		t.Fatal(err)
	}
	b, err := HashPassword("same password here")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two hashes of the same password are identical: salt is not random")
	}
}

func TestVerifyPassword_RejectsMalformed(t *testing.T) {
	good, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(good, "$")
	for _, tc := range []struct {
		name string
		hash string
	}{
		{"empty", ""},
		{"bcrypt", "$2b$12$abcdefghijklmnopqrstuuabcdefghijklmnopqrstuvwxyz0123456789"},
		{"argon2i", strings.Replace(good, "argon2id", "argon2i", 1)},
		{"bad version", strings.Replace(good, "v=19", "v=16", 1)},
		{"missing params", strings.Join([]string{"", "argon2id", "v=19", "m=65536", parts[4], parts[5]}, "$")},
		{"zero threads", strings.Replace(good, "p=1", "p=0", 1)},
		{"unknown param", strings.Replace(good, "p=1", "p=1,x=2", 1)},
		{"bad salt", strings.Join([]string{"", "argon2id", "v=19", "m=65536,t=3,p=1", "!!!", parts[5]}, "$")},
		{"empty hash", strings.Join([]string{"", "argon2id", "v=19", "m=65536,t=3,p=1", parts[4], ""}, "$")},
		{"extra field", good + "$x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ok, err := VerifyPassword(tc.hash, "correct horse battery")
			if !errors.Is(err, ErrInvalidHash) || ok {
				t.Fatalf("VerifyPassword(%q) = %v, %v; want false, ErrInvalidHash", tc.hash, ok, err)
			}
		})
	}
}

func TestWriteAndLoad(t *testing.T) {
	path := Path(filepath.Join(t.TempDir(), "home"))
	if err := Write(path, "alice", "a long enough password"); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != FileMode {
		t.Fatalf("mode = %04o, want 0600", fi.Mode().Perm())
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Username != "alice" || c.Updated.IsZero() {
		t.Fatalf("loaded %+v", c)
	}
	if !c.Verify("alice", "a long enough password") {
		t.Fatal("Verify rejected the right credentials")
	}
	if c.Verify("bob", "a long enough password") {
		t.Fatal("Verify accepted the wrong username")
	}
	if c.Verify("alice", "a long enough passwordX") {
		t.Fatal("Verify accepted the wrong password")
	}
	if c.Verify("", "") {
		t.Fatal("Verify accepted empty credentials")
	}

	// Running `claw admin` again replaces the account.
	if err = Write(path, "bob", "another long password"); err != nil {
		t.Fatal(err)
	}
	c, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Username != "bob" || !c.Verify("bob", "another long password") || c.Verify("alice", "a long enough password") {
		t.Fatalf("second Write did not replace the account: %+v", c)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("temp file left behind: %v", entries)
	}
}

func TestLoad_Errors(t *testing.T) {
	dir := t.TempDir()

	if _, err := Load(Path(dir)); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("missing file: err = %v, want ErrNotConfigured", err)
	}

	loose := Path(filepath.Join(dir, "loose"))
	if err := Write(loose, "alice", "a long enough password"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(loose, 0o640); err != nil {
		t.Fatal(err)
	}
	_, err := Load(loose)
	var perr *PermissionError
	if !errors.As(err, &perr) {
		t.Fatalf("group-readable file: err = %v, want *PermissionError", err)
	}
	if perr.Fix() != "chmod 600 "+loose {
		t.Fatalf("Fix() = %q", perr.Fix())
	}

	corrupt := Path(filepath.Join(dir, "corrupt"))
	if err := os.MkdirAll(filepath.Dir(corrupt), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(corrupt, []byte(`{"username":"alice","password_hash":"$2b$nope"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(corrupt); !errors.Is(err, ErrInvalidHash) {
		t.Fatalf("corrupt hash: err = %v, want ErrInvalidHash", err)
	}

	noUser := Path(filepath.Join(dir, "nouser"))
	if err := os.MkdirAll(filepath.Dir(noUser), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(noUser, []byte(`{"username":" ","password_hash":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(noUser); err == nil {
		t.Fatal("empty username accepted")
	}
}
