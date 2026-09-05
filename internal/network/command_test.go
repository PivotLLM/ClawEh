package network

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

// runCommand executes `claw network` with args against the temp CLAW_HOME set
// up by the caller, returning combined output. It drives the real cobra command
// so the argument parsing, the flag, and the default-when-bare behaviour are all
// covered rather than just the helpers underneath.
func runCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := NewNetworkCommand()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)

	// The command prints with fmt.Printf, so capture os.Stdout as well.
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	execErr := cmd.Execute()
	_ = w.Close()
	os.Stdout = orig
	var stdout bytes.Buffer
	if _, err := stdout.ReadFrom(r); err != nil {
		t.Fatal(err)
	}
	return stdout.String() + buf.String(), execErr
}

// TestCommandBareSetsPrivateRanges is the headline behaviour: an operator who
// has locked themselves out types `claw network` and nothing else. If this ever
// becomes a no-op or a help dump, the recovery path is gone.
func TestCommandBareSetsPrivateRanges(t *testing.T) {
	seedConfig(t)

	out, err := runCommand(t)
	if err != nil {
		t.Fatalf("`network` error = %v", err)
	}
	got, err := CurrentAllowlist()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(config.PrivateNetworkCIDRs) {
		t.Fatalf("allowlist = %v, want %v", got, config.PrivateNetworkCIDRs)
	}
	for i := range got {
		if got[i] != config.PrivateNetworkCIDRs[i] {
			t.Fatalf("allowlist = %v, want %v", got, config.PrivateNetworkCIDRs)
		}
	}
	if !strings.Contains(out, "10.0.0.0/8") {
		t.Errorf("output does not name what it set: %q", out)
	}
}

// TestCommandShowDoesNotMutate matters because --show is what an operator runs
// first, while still locked out. A read that silently rewrites the config would
// be an unpleasant surprise.
func TestCommandShowDoesNotMutate(t *testing.T) {
	path := seedConfig(t)
	if _, err := ApplyAllowlist(ParseAllowlist("192.168.1.0/24")); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	out, err := runCommand(t, "--show")
	if err != nil {
		t.Fatalf("`network --show` error = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("`network --show` rewrote the config")
	}
	if !strings.Contains(out, "192.168.1.0/24") {
		t.Errorf("--show did not report the current allowlist: %q", out)
	}
}

// TestCommandShowWinsOverAnArgument pins the precedence, so `network --show any`
// reports rather than quietly opening the port to the world.
func TestCommandShowWinsOverAnArgument(t *testing.T) {
	seedConfig(t)
	if _, err := runCommand(t, "--show", "any"); err != nil {
		t.Fatalf("error = %v", err)
	}
	got, err := CurrentAllowlist()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("--show with an argument changed the allowlist to %v", got)
	}
}

func TestCommandExplicitSpecs(t *testing.T) {
	for _, tc := range []struct {
		arg  string
		want []string
	}{
		{"192.168.1.0/24", []string{"192.168.1.0/24"}},
		{"10.0.0.0/8,192.168.1.0/24", []string{"10.0.0.0/8", "192.168.1.0/24"}},
		{"any", []string{config.AllowAnyAddress}},
		{"none", nil},
	} {
		t.Run(tc.arg, func(t *testing.T) {
			seedConfig(t)
			if _, err := runCommand(t, tc.arg); err != nil {
				t.Fatalf("`network %s` error = %v", tc.arg, err)
			}
			got, err := CurrentAllowlist()
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("`network %s` → %v, want %v", tc.arg, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("`network %s` → %v, want %v", tc.arg, got, tc.want)
				}
			}
		})
	}
}

// TestCommandWildcardWarns keeps the "no password on the other side of this"
// warning attached to the one option that exposes the WebUI to everything.
func TestCommandWildcardWarns(t *testing.T) {
	seedConfig(t)
	out, err := runCommand(t, "any")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(out, "WARNING") {
		t.Errorf("`network any` printed no warning: %q", out)
	}
}

func TestCommandRejectsInvalidInput(t *testing.T) {
	seedConfig(t)
	if _, err := runCommand(t, "192.168.1.0"); err == nil {
		t.Fatal("`network 192.168.1.0` (no prefix length) should fail")
	}
	got, err := CurrentAllowlist()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("a rejected argument still changed the allowlist to %v", got)
	}
}

// TestCommandRejectsExtraArguments guards the arity: `network 10.0.0.0/8
// 192.168.1.0/24` (space instead of comma) must be an error rather than
// silently applying only the first.
func TestCommandRejectsExtraArguments(t *testing.T) {
	seedConfig(t)
	if _, err := runCommand(t, "10.0.0.0/8", "192.168.1.0/24"); err == nil {
		t.Fatal("two positional arguments should be rejected; CIDRs are comma-separated")
	}
}

func TestCommandName(t *testing.T) {
	if got := NewNetworkCommand().Name(); got != "network" {
		t.Fatalf("command name = %q, want %q", got, "network")
	}
}
