// ClawEh - Session store CLI
// License: MIT

package sessions

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/PivotLLM/ctxengine/memory"
	"github.com/PivotLLM/ctxengine/session"
	"github.com/PivotLLM/spawnllm"

	"github.com/PivotLLM/ClawEh/config"
)

// eraseTestConfig is a two-agent install (Alice default, Bob) under a temp
// base dir, in the given session mode.
func eraseTestConfig(t *testing.T, mode string) *config.Config {
	t.Helper()
	return &config.Config{
		Agents: config.AgentsConfig{
			BaseDir: t.TempDir(),
			List: []config.AgentConfig{
				{ID: "alice", Name: "Alice", Default: true},
				{ID: "bob", Name: "Bob"},
			},
		},
		Session: config.SessionConfig{
			Mode:          mode,
			IdentityLinks: map[string][]string{"carol": {"telegram:555", "slack:u555"}},
		},
	}
}

func seedKeys(t *testing.T, cfg *config.Config, agentID string, keys ...string) string {
	t.Helper()
	dir := filepath.Join(cfg.Agents.BaseDir, agentID, "sessions")
	store, err := session.NewSQLiteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		if _, err := store.AddFullMessage(key, spawnllm.Message{Role: "user", Content: "hi"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

func exists(t *testing.T, dir, key string) bool {
	t.Helper()
	_, err := os.Stat(memory.ArchivePath(dir, key))
	if err == nil {
		return true
	}
	if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return false
}

// TestErase_ByChannelAndChat removes the sender's sessions in every agent and
// shape (direct, per-account direct, group, identity-linked per-user) and
// leaves other senders, other channels and the main session alone.
func TestErase_ByChannelAndChat(t *testing.T) {
	cfg := eraseTestConfig(t, "per-platform")
	aliceDir := seedKeys(t, cfg, "alice",
		"agent:alice:telegram:direct:555",
		"agent:alice:telegram:acct1:direct:555",
		"agent:alice:telegram:group:555",
		"agent:alice:direct:carol", // per-user key via identity_links
		"agent:alice:telegram:direct:777",
		"agent:alice:slack:direct:555",
		"agent:alice:main",
	)
	bobDir := seedKeys(t, cfg, "bob", "agent:bob:telegram:direct:555", "agent:bob:main")

	rep, err := Erase(cfg, EraseRequest{Channel: "Telegram", ChatID: "555"}, nil)
	if err != nil {
		t.Fatalf("Erase: %v", err)
	}
	slices.Sort(rep.Erased)
	want := []string{
		"agent:alice:direct:carol",
		"agent:alice:telegram:acct1:direct:555",
		"agent:alice:telegram:direct:555",
		"agent:alice:telegram:group:555",
		"agent:bob:telegram:direct:555",
	}
	if !slices.Equal(rep.Erased, want) {
		t.Fatalf("erased %v\nwant   %v", rep.Erased, want)
	}
	for _, key := range want[:4] {
		if exists(t, aliceDir, key) {
			t.Errorf("%s still on disk", key)
		}
	}
	if exists(t, bobDir, "agent:bob:telegram:direct:555") {
		t.Error("bob's session still on disk")
	}
	for _, key := range []string{"agent:alice:telegram:direct:777", "agent:alice:slack:direct:555", "agent:alice:main"} {
		if !exists(t, aliceDir, key) {
			t.Errorf("%s was deleted", key)
		}
	}
	if !exists(t, bobDir, "agent:bob:main") {
		t.Error("bob's main session was deleted")
	}
	if rep.Shared != "" {
		t.Errorf("Shared = %q under an isolating scope", rep.Shared)
	}
	if rep.Cogmem != CogmemNote {
		t.Errorf("Cogmem note missing: %q", rep.Cogmem)
	}
}

// TestErase_UnifiedSharedSession: under unified scope the sender's messages
// are in the routed agent's main session, which is reported and kept without
// All, and deleted with it.
func TestErase_UnifiedSharedSession(t *testing.T) {
	cfg := eraseTestConfig(t, "unified")
	aliceDir := seedKeys(t, cfg, "alice", "agent:alice:main")
	bobDir := seedKeys(t, cfg, "bob", "agent:bob:main")

	rep, err := Erase(cfg, EraseRequest{Channel: "telegram", ChatID: "555"}, nil)
	if err != nil {
		t.Fatalf("Erase: %v", err)
	}
	if len(rep.Erased) != 0 || rep.Shared != "agent:alice:main" {
		t.Fatalf("erased %v, shared %q; want nothing erased and alice's main reported", rep.Erased, rep.Shared)
	}
	if !exists(t, aliceDir, "agent:alice:main") {
		t.Fatal("shared session deleted without --all")
	}

	rep, err = Erase(cfg, EraseRequest{Channel: "telegram", ChatID: "555", All: true}, nil)
	if err != nil {
		t.Fatalf("Erase --all: %v", err)
	}
	if !slices.Equal(rep.Erased, []string{"agent:alice:main"}) || rep.Shared != "" {
		t.Fatalf("--all erased %v, shared %q", rep.Erased, rep.Shared)
	}
	if exists(t, aliceDir, "agent:alice:main") {
		t.Fatal("shared session still on disk after --all")
	}
	if !exists(t, bobDir, "agent:bob:main") {
		t.Fatal("--all deleted a main session the sender does not route to")
	}
}

// TestErase_ReleaseRefused: a session the gateway cannot release (turn in
// flight) is reported as skipped and left on disk; the others still go.
func TestErase_ReleaseRefused(t *testing.T) {
	cfg := eraseTestConfig(t, "per-platform")
	dir := seedKeys(t, cfg, "alice", "agent:alice:telegram:direct:555", "agent:alice:telegram:group:555")
	release := func(key string) error {
		if strings.Contains(key, ":group:") {
			return errors.New("turn in flight")
		}
		return nil
	}

	rep, err := Erase(cfg, EraseRequest{Channel: "telegram", ChatID: "555"}, release)
	if err != nil {
		t.Fatalf("Erase: %v", err)
	}
	if !slices.Equal(rep.Erased, []string{"agent:alice:telegram:direct:555"}) {
		t.Fatalf("erased %v", rep.Erased)
	}
	if len(rep.Skipped) != 1 || !strings.HasPrefix(rep.Skipped[0], "agent:alice:telegram:group:555: ") {
		t.Fatalf("skipped %v", rep.Skipped)
	}
	if !exists(t, dir, "agent:alice:telegram:group:555") {
		t.Fatal("refused session was deleted")
	}
}

func TestErase_RequiresChannelAndChat(t *testing.T) {
	cfg := eraseTestConfig(t, "unified")
	if _, err := Erase(cfg, EraseRequest{Channel: "telegram"}, nil); err == nil {
		t.Fatal("missing chat id accepted")
	}
	if _, err := Erase(cfg, EraseRequest{ChatID: "1"}, nil); err == nil {
		t.Fatal("missing channel accepted")
	}
}

func TestMatchesSender(t *testing.T) {
	peers := map[string]bool{"555": true, "webui:abc-def": true}
	tests := []struct {
		key     string
		channel string
		want    bool
	}{
		{"agent:alice:telegram:direct:555", "telegram", true},
		{"agent:alice:telegram:acct:direct:555", "telegram", true},
		{"agent:alice:telegram:group:555", "telegram", true},
		{"agent:alice:slack:channel:555", "slack", true},
		{"agent:alice:direct:555", "telegram", true}, // per-user: no channel in the key
		{"agent:alice:webui:direct:webui:abc-def", "webui", true},
		{"agent:alice:device:555", "device", true},
		{"agent:alice:telegram:direct:5555", "telegram", false},
		{"agent:alice:slack:direct:555", "telegram", false},
		{"agent:alice:device:555", "telegram", false},
		{"agent:alice:main", "telegram", false},
		{"agent:alice:service", "telegram", false},
		{"agent:alice:subagent:555", "telegram", false},
		{"555", "telegram", false},
	}
	for _, tc := range tests {
		if got := matchesSender(tc.key, tc.channel, peers); got != tc.want {
			t.Errorf("matchesSender(%q, %q) = %v, want %v", tc.key, tc.channel, got, tc.want)
		}
	}
}

func TestFormatReport(t *testing.T) {
	req := EraseRequest{Channel: "telegram", ChatID: "555"}
	out := FormatReport(EraseReport{
		Erased:  []string{"agent:alice:telegram:direct:555"},
		Skipped: []string{"agent:alice:telegram:group:555: turn in flight"},
		Shared:  "agent:alice:main",
		Cogmem:  CogmemNote,
	}, req)
	for _, want := range []string{
		"erased agent:alice:telegram:direct:555\n",
		"skipped agent:alice:telegram:group:555: turn in flight\n",
		"kept agent:alice:main: session mode is unified",
		CogmemNote + "\n",
		"Erased 1 session(s).\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q:\n%s", want, out)
		}
	}
	empty := FormatReport(EraseReport{Cogmem: CogmemNote}, req)
	if !strings.Contains(empty, "No sessions found for telegram/555.") {
		t.Errorf("empty report:\n%s", empty)
	}
}
