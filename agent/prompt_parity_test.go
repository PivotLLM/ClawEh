// ClawEh
// License: MIT

package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/ctxengine"
	"github.com/PivotLLM/ctxengine/session"

	"github.com/PivotLLM/ClawEh/app"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/providers"
)

// promptParityGolden is the parity oracle for the prompt-layer refactor: the
// bytes the engine sends to the model for a realistic agent must not change.
// The golden was captured under the previous ContextBuilder.BuildMessages path
// and the host-supplied Layers path must reproduce it exactly. Regenerate with
// PROMPT_PARITY_UPDATE=1 only when a change to the prompt is intended.
const promptParityGolden = "testdata/prompt_parity.golden.json"

// parityCase is one assembled request captured in the golden file.
type parityCase struct {
	Name     string              `json:"name"`
	Messages []providers.Message `json:"messages"`
}

// parityContextBuilder builds a ContextBuilder over a workspace with every
// prompt source populated: bootstrap files, long-term memory, a workspace
// skill, a channel prompt, mounts, memory guidance and tool discovery.
func parityContextBuilder(t *testing.T) *ContextBuilder {
	t.Helper()
	// Keep the global and builtin skill roots empty so the prompt depends only
	// on the workspace built here.
	t.Setenv("CLAW_HOME", t.TempDir())
	t.Setenv("CLAW_BUILTIN_SKILLS", t.TempDir())

	ws := t.TempDir()
	files := map[string]string{
		"AGENTS.md":            "# Agents\n\nAlice coordinates; Bob executes.\n",
		"SOUL.md":              "# Soul\n\nCalm and precise.\n",
		"USER.md":              "# User\n\nPrefers short answers.\n",
		"IDENTITY.md":          "# Identity\n\nYou are Alice.\n",
		"MEMORY.md":            "- The build is gated by make check.\n",
		"channel-webui.md":     "## WebUI\n\nMarkdown is rendered.\n",
		"skills/demo/SKILL.md": "---\nname: demo\ndescription: A demo skill\n---\n# Demo\n",
	}
	for name, content := range files {
		path := filepath.Join(ws, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cb := NewContextBuilder(ws).
		WithToolDiscovery(true).
		WithMemoryGuidance("**Memory** - Retrieve before you assume.").
		WithMounts([]config.MountConfig{{Name: "notes", Path: "/srv/notes", Writable: true}})
	cb.now = func() time.Time { return time.Date(2026, 9, 14, 8, 30, 0, 0, time.UTC) }
	return cb
}

// parityHistory is a stored history that exercises the provider sanitiser: a
// stale system message, a complete tool group, an orphaned tool result and a
// plain exchange.
func parityHistory() []providers.Message {
	return []providers.Message{
		{Role: "system", Content: "stale system prompt from a previous build"},
		{Role: "user", Content: "Read the outline and tell me the first heading."},
		{Role: "assistant", ToolCalls: []providers.ToolCall{{
			ID: "call_1", Type: "function", Name: "file_read_lines",
			Function: &providers.FunctionCall{Name: "file_read_lines", Arguments: `{"path":"files/outline.md"}`},
		}}},
		{Role: "tool", ToolCallID: "call_1", Content: "# Outline\n\n## Chapter one"},
		{Role: "assistant", Content: "The first heading is \"Outline\"."},
		{Role: "tool", ToolCallID: "call_orphan", Content: "orphaned result"},
		{Role: "user", Content: "Thanks. What is next?"},
	}
}

// paritySummary is a structured summary with cited material so it renders
// through Summary.Render rather than passing through as prose.
const paritySummary = `{"version":2,"state":{"goals":[{"text":"Finish the outline","refs":[{"seq_start":2,"seq_end":3}]}],"pending":[{"text":"Draft chapter two","refs":[{"seq_start":5}]}]},"key_moments":[{"refs":[{"seq_start":2}],"role":"user","summary":"Asked for the first heading"}],"carry_forward":[{"text":"Persist the heading convention to AGENTS.md","refs":[{"seq_start":5}]}],"notes":["Headings use ATX style"],"covered_seq_start":1,"covered_seq_end":5,"covered_seq_start_at":"2026-09-13T10:00:00Z","covered_seq_end_at":"2026-09-13T11:00:00Z","generated_at":"2026-09-13T11:05:00Z","model":"test-summarizer","profile":"abcd1234"}`

// parityInjections are the memory blocks a cognitive agent places: one in the
// stable system prefix, one on the current turn.
func parityInjections() []ctxengine.Injection {
	return []ctxengine.Injection{
		{Placement: ctxengine.PlaceSystemStable, Text: "# Memory Domains\n\n- writing (active)"},
		{Placement: ctxengine.PlaceCurrentUser, Text: "Recalled: the outline lives in files/outline.md"},
	}
}

// parityStore seeds a session store with parityHistory and the given summary.
func parityStore(t *testing.T, key, summary string) session.SessionStore {
	t.Helper()
	store := session.NewSessionManager("")
	for _, m := range parityHistory() {
		store.AddFullMessage(key, m)
	}
	if summary != "" {
		store.SetSummary(key, summary)
	}
	return store
}

// parityAssemble runs one Assemble through the engine the way the agent loop
// does for a dispatch on channel "webui", chat "chat-1", with a session token.
func parityAssemble(t *testing.T, cb *ContextBuilder, store session.SessionStore, key, archiveDir string) []providers.Message {
	t.Helper()
	const token = "SST-parity-token"
	cm := ctxengine.New(key, store,
		ctxengine.WithContextWindow(200_000),
		ctxengine.WithArchiveDir(archiveDir),
	)
	defer cm.Close(context.Background())
	asm, err := cm.Assemble(context.Background(), ctxengine.AssembleRequest{
		ToolDefinitionTokens: 10,
		Layers:               append(cb.PromptLayers("webui", "chat-1"), sessionTokenLayer(token)),
		Injections:           parityInjections(),
		Channel:              "webui",
		ChatID:               "chat-1",
	})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	return asm.Messages
}

// parityCases builds the two assemblies the golden file records: a session
// with a structured summary and no archive, and a session with no summary
// whose archive holds rows (the archive-bounds note path).
func parityCases(t *testing.T) ([]parityCase, *ContextBuilder) {
	t.Helper()
	cb := parityContextBuilder(t)

	withSummary := parityAssemble(t, cb, parityStore(t, "parity-summary", paritySummary), "parity-summary", "")

	archiveDir := t.TempDir()
	const archiveKey = "parity-archive"
	store := parityStore(t, archiveKey, "")
	// Route one message through a manager with an archive so the archive holds
	// rows and the assembled prompt carries the bounds note instead of a summary.
	seed := ctxengine.New(archiveKey, store, ctxengine.WithArchiveDir(archiveDir))
	if _, err := seed.AddUserMessage(context.Background(), providers.Message{Role: "user", Content: "archived turn"}); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	withArchive := parityAssemble(t, cb, store, archiveKey, archiveDir)

	return []parityCase{
		{Name: "summary_no_archive", Messages: withSummary},
		{Name: "archive_no_summary", Messages: withArchive},
	}, cb
}

// parityNormalize replaces the machine-specific substrings of the prompt with
// stable tokens so the golden file compares on every host: the workspace path,
// the build version and the Go runtime line.
func parityNormalize(t *testing.T, data []byte, cb *ContextBuilder) string {
	t.Helper()
	ws, err := filepath.Abs(cb.workspace)
	if err != nil {
		t.Fatal(err)
	}
	r := strings.NewReplacer(
		ws, "{{WORKSPACE}}",
		app.Version(), "{{VERSION}}",
		"OS: "+runtime.GOOS+" "+runtime.GOARCH, "OS: {{OS}}",
		"Go: "+runtime.Version(), "Go: {{GO}}",
	)
	return r.Replace(string(data))
}

// TestPromptParity_Golden is the byte-level oracle: the assembled request for a
// realistic agent, summary, archive, session token and memory injections must
// match the recorded golden exactly.
func TestPromptParity_Golden(t *testing.T) {
	cases, cb := parityCases(t)
	raw, err := json.MarshalIndent(cases, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got := parityNormalize(t, raw, cb)

	if os.Getenv("PROMPT_PARITY_UPDATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(promptParityGolden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(promptParityGolden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("updated %s", promptParityGolden)
		return
	}

	want, err := os.ReadFile(promptParityGolden)
	if err != nil {
		t.Fatalf("read golden (regenerate with PROMPT_PARITY_UPDATE=1): %v", err)
	}
	if string(want) != got {
		t.Fatalf("assembled request differs from %s\n--- got ---\n%s", promptParityGolden, firstDiff(string(want), got))
	}
}

// firstDiff renders the region around the first differing byte.
func firstDiff(want, got string) string {
	n := len(want)
	if len(got) < n {
		n = len(got)
	}
	i := 0
	for i < n && want[i] == got[i] {
		i++
	}
	lo := i - 200
	if lo < 0 {
		lo = 0
	}
	hiW, hiG := i+200, i+200
	if hiW > len(want) {
		hiW = len(want)
	}
	if hiG > len(got) {
		hiG = len(got)
	}
	return "first difference at byte " + itoa(i) + "\nwant: ..." + want[lo:hiW] + "...\ngot:  ..." + got[lo:hiG] + "..."
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}
