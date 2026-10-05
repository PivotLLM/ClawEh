// ClawEh
// License: MIT

package admin

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

// fakeTerminal is a scripted terminal: lines for the username prompt and one
// secret per password read.
type fakeTerminal struct {
	out     bytes.Buffer
	secrets []string
	closed  bool
}

func (f *fakeTerminal) open(lines string) func(io.Writer) (*Terminal, error) {
	return func(io.Writer) (*Terminal, error) {
		return &Terminal{
			In:  strings.NewReader(lines),
			Out: &f.out,
			ReadPassword: func() ([]byte, error) {
				if len(f.secrets) == 0 {
					return nil, io.EOF
				}
				s := f.secrets[0]
				f.secrets = f.secrets[1:]
				return []byte(s), nil
			},
			Close: func() error { f.closed = true; return nil },
		}, nil
	}
}

func noEnv(string) string { return "" }

func env(user, pass string) func(string) string {
	return func(k string) string {
		switch k {
		case EnvUser:
			return user
		case EnvPassword:
			return pass
		}
		return ""
	}
}

func noTerminal(t *testing.T) func(io.Writer) (*Terminal, error) {
	t.Helper()
	return func(io.Writer) (*Terminal, error) {
		t.Error("terminal opened when it should not be")
		return nil, ErrNoTerminal
	}
}

func TestValidateUsername(t *testing.T) {
	for _, tc := range []struct {
		in string
		ok bool
	}{
		{"alice", true},
		{"", false},
		{"al ice", false},
		{strings.Repeat("a", 64), true},
		{strings.Repeat("a", 65), false},
	} {
		if err := ValidateUsername(tc.in); (err == nil) != tc.ok {
			t.Errorf("ValidateUsername(%q) err = %v, want ok=%v", tc.in, err, tc.ok)
		}
	}
}

func TestEnsureAccount_ExistingFileUntouched(t *testing.T) {
	home := t.TempDir()
	path := Path(home)
	if err := Write(path, "carol", "carol's long password"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	user, created, err := EnsureAccount(EnsureOptions{
		Home:         home,
		Getenv:       env("mallory", "another long password"),
		OpenTerminal: noTerminal(t),
		Chown:        func(string) error { t.Error("chown called for an existing file"); return nil },
	})
	if err != nil || user != "carol" || created {
		t.Fatalf("EnsureAccount = %q, %v, %v; want carol, false, nil", user, created, err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("existing credentials file was rewritten")
	}
}

func TestEnsureAccount_UnusableFileIsNotOverwritten(t *testing.T) {
	home := t.TempDir()
	path := Path(home)
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := EnsureAccount(EnsureOptions{Home: home, Getenv: env("dave", "a long enough password"), OpenTerminal: noTerminal(t)})
	if perr, ok := errors.AsType[*PermissionError](err); !ok || perr.Path != path {
		t.Fatalf("EnsureAccount = %v; want the permission error", err)
	}
	if data, rerr := os.ReadFile(path); rerr != nil || string(data) != "{}" {
		t.Fatalf("file changed: %q, %v", data, rerr)
	}
}

func TestEnsureAccount_FromEnvironment(t *testing.T) {
	home := t.TempDir()
	var chowned string
	user, created, err := EnsureAccount(EnsureOptions{
		Home:         home,
		Getenv:       env(" erin ", "erin's long password"),
		OpenTerminal: noTerminal(t),
		Chown:        func(p string) error { chowned = p; return nil },
	})
	if err != nil || user != "erin" || !created {
		t.Fatalf("EnsureAccount = %q, %v, %v; want erin, true, nil", user, created, err)
	}
	path := Path(home)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != FileMode {
		t.Errorf("mode = %04o, want %04o", fi.Mode().Perm(), FileMode)
	}
	creds, err := Load(path)
	if err != nil || !creds.Verify("erin", "erin's long password") {
		t.Fatalf("Load/Verify: %v", err)
	}
	if chowned != path {
		t.Errorf("chown called with %q, want %q", chowned, path)
	}
}

func TestEnsureAccount_EnvironmentErrors(t *testing.T) {
	for _, tc := range []struct {
		name, user, pass, want string
	}{
		{"only user", "frank", "", "set both"},
		{"only password", "", "a long enough password", "set both"},
		{"short password", "frank", "short", EnvPassword},
		{"bad username", "fr ank", "a long enough password", EnvUser},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			_, _, err := EnsureAccount(EnsureOptions{Home: home, Getenv: env(tc.user, tc.pass), OpenTerminal: noTerminal(t)})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("EnsureAccount = %v; want an error containing %q", err, tc.want)
			}
			if _, serr := os.Stat(Path(home)); !errors.Is(serr, os.ErrNotExist) {
				t.Fatalf("credentials file written after an error: %v", serr)
			}
		})
	}
}

func TestEnsureAccount_ChownFailureRemovesFile(t *testing.T) {
	home := t.TempDir()
	_, _, err := EnsureAccount(EnsureOptions{
		Home:   home,
		Getenv: env("gina", "a long enough password"),
		Chown:  func(string) error { return errors.New("chown refused") },
	})
	if err == nil || !strings.Contains(err.Error(), "chown refused") {
		t.Fatalf("EnsureAccount = %v", err)
	}
	if _, serr := os.Stat(Path(home)); !errors.Is(serr, os.ErrNotExist) {
		t.Fatalf("file left behind after a failed chown: %v", serr)
	}
}

func TestEnsureAccount_NoTerminal(t *testing.T) {
	home := t.TempDir()
	_, _, err := EnsureAccount(EnsureOptions{
		Home:   home,
		Getenv: noEnv,
		OpenTerminal: func(io.Writer) (*Terminal, error) {
			return nil, fmt.Errorf("%w: /dev/tty: no such device", ErrNoTerminal)
		},
	})
	if !errors.Is(err, ErrNoAccountNoTerminal) {
		t.Fatalf("EnsureAccount = %v; want ErrNoAccountNoTerminal", err)
	}
	for _, want := range []string{"no admin account and no terminal to create one", "export CLAW_ADMIN_USER and CLAW_ADMIN_PASSWORD", "run `claw admin` on the server", "rerun the installer"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if _, serr := os.Stat(Path(home)); !errors.Is(serr, os.ErrNotExist) {
		t.Fatalf("credentials file exists: %v", serr)
	}
}

func TestEnsureAccount_Interactive(t *testing.T) {
	home := t.TempDir()
	ft := &fakeTerminal{secrets: []string{
		"short",                                      // too short: asked again
		"harry's long password", "not the same one!", // mismatch: asked again
		"harry's long password", "harry's long password",
	}}
	user, created, err := EnsureAccount(EnsureOptions{
		Home:         home,
		Getenv:       noEnv,
		OpenTerminal: ft.open("har ry\nharry\n"), // whitespace: asked again
	})
	if err != nil || user != "harry" || !created {
		t.Fatalf("EnsureAccount = %q, %v, %v; want harry, true, nil", user, created, err)
	}
	if !ft.closed {
		t.Error("terminal not closed")
	}
	out := ft.out.String()
	for _, want := range []string{"needs an admin login", "Username: ", "must not contain whitespace", "at least 12 characters", "passwords do not match", "Confirm password: "} {
		if !strings.Contains(out, want) {
			t.Errorf("terminal output lacks %q:\n%s", want, out)
		}
	}
	creds, err := Load(Path(home))
	if err != nil || !creds.Verify("harry", "harry's long password") {
		t.Fatalf("Load/Verify: %v", err)
	}
}

func TestEnsureAccount_InteractiveGivesUp(t *testing.T) {
	home := t.TempDir()
	ft := &fakeTerminal{secrets: []string{
		"a long enough password", "mismatch number one",
		"a long enough password", "mismatch number two",
		"a long enough password", "mismatch number three",
	}}
	_, _, err := EnsureAccount(EnsureOptions{Home: home, Getenv: noEnv, OpenTerminal: ft.open("ivy\n")})
	if err == nil || !strings.Contains(err.Error(), "passwords do not match") {
		t.Fatalf("EnsureAccount = %v; want the mismatch error", err)
	}
	if _, serr := os.Stat(Path(home)); !errors.Is(serr, os.ErrNotExist) {
		t.Fatalf("credentials file written: %v", serr)
	}
}

func TestEnsureAccount_InteractiveEOF(t *testing.T) {
	// A closed terminal must end the prompt rather than loop.
	ft := &fakeTerminal{}
	_, _, err := EnsureAccount(EnsureOptions{Home: t.TempDir(), Getenv: noEnv, OpenTerminal: ft.open("")})
	if err == nil || !strings.Contains(err.Error(), "username is required") {
		t.Fatalf("EnsureAccount = %v", err)
	}
}

func TestPromptAccount_GivenUsernameNotReasked(t *testing.T) {
	ft := &fakeTerminal{}
	term, err := ft.open("")(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := PromptAccount(term, "bad name"); err == nil || !strings.Contains(err.Error(), "whitespace") {
		t.Fatalf("PromptAccount = %v", err)
	}
	if ft.out.Len() != 0 {
		t.Errorf("prompted despite an invalid given username: %q", ft.out.String())
	}
}
