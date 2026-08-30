// ClawEh - Cognitive Memory
// License: MIT

package cogmem

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/tools"
	toolfiles "github.com/PivotLLM/ClawEh/tools/files"
)

// buildHandlersWithFiles builds the cogmem handlers against a workspace whose
// reads are confined to files/, matching the default agent posture. Attachment
// validation needs a real config, which the plain buildHandlers helper omits.
func buildHandlersWithFiles(t *testing.T) (map[string]global.ToolHandler, string) {
	t.Helper()
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "files"), 0o755); err != nil {
		t.Fatal(err)
	}
	toolfiles.SetReadScopeSubdirs([]string{"files"})
	t.Cleanup(func() { toolfiles.SetReadScopeSubdirs(nil) })

	cfg := &config.Config{}
	cfg.Agents.Defaults.RestrictToWorkspace = true
	cfg.Agents.Defaults.AllowReadOutsideWorkspace = false

	defs := GlobalProvider.RegisterTools(global.Deps{
		Cfg:  cfg,
		Host: tools.ToolDeps{Workspace: ws, AgentID: "alice"},
	})
	m := make(map[string]global.ToolHandler, len(defs))
	for _, d := range defs {
		m[d.Name] = d.Handler
	}
	return m, ws
}

func TestMemoryCreateWithAttachment(t *testing.T) {
	h, ws := buildHandlersWithFiles(t)
	if err := os.WriteFile(filepath.Join(ws, "files", "voice.md"), []byte("my voice"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := run(t, h["memory_create"], newCall(testSession, map[string]any{
		"type": "rule", "text": "Write in my voice.", "file": "files/voice.md",
	}))
	if res.IsError {
		t.Fatalf("memory_create with a readable attachment failed: %s", res.ForLLM)
	}
	if !strings.Contains(res.ForLLM, "Attached files/voice.md (8 bytes)") {
		t.Fatalf("expected the attachment to be confirmed with its size: %s", res.ForLLM)
	}

	// The reference is visible when the memory is read back.
	got := run(t, h["memory_search"], newCall(testSession, map[string]any{"query": "voice"}))
	if !strings.Contains(got.ForLLM, "[file: files/voice.md]") {
		t.Fatalf("search should show the attached file: %s", got.ForLLM)
	}
}

// A pointer the agent cannot read is rejected at create time rather than stored
// and silently failing in every later prompt.
func TestMemoryCreateRejectsUnreadableAttachment(t *testing.T) {
	h, ws := buildHandlersWithFiles(t)
	if err := os.MkdirAll(filepath.Join(ws, "secrets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "secrets", "keys.md"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := run(t, h["memory_create"], newCall(testSession, map[string]any{
		"type": "rule", "text": "secret", "file": "secrets/keys.md",
	}))
	if !res.IsError {
		t.Fatalf("expected an out-of-scope attachment to be rejected: %s", res.ForLLM)
	}
	if !strings.Contains(res.ForLLM, "attachment rejected") {
		t.Fatalf("unexpected error text: %s", res.ForLLM)
	}

	// Nothing was stored.
	list := run(t, h["memory_search"], newCall(testSession, map[string]any{"query": "secret"}))
	if strings.Contains(list.ForLLM, "secrets/keys.md") {
		t.Fatalf("rejected memory should not have been stored: %s", list.ForLLM)
	}
}

func TestMemoryCreateRejectsNonMarkdownAttachment(t *testing.T) {
	h, ws := buildHandlersWithFiles(t)
	if err := os.WriteFile(filepath.Join(ws, "files", "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := run(t, h["memory_create"], newCall(testSession, map[string]any{
		"type": "fact", "text": "notes", "file": "files/notes.txt",
	}))
	if !res.IsError || !strings.Contains(res.ForLLM, "not a markdown file") {
		t.Fatalf("expected a non-markdown attachment to be rejected: %s", res.ForLLM)
	}
}

// memory_attach repoints and detaches an existing memory in place, keeping its
// id — the whole reason the tool exists rather than retire + re-create.
func TestMemoryAttachRepointsAndDetaches(t *testing.T) {
	h, ws := buildHandlersWithFiles(t)
	for _, name := range []string{"voice.md", "voice-v2.md"} {
		if err := os.WriteFile(filepath.Join(ws, "files", name), []byte("doc "+name), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	created := run(t, h["memory_create"], newCall(testSession, map[string]any{
		"type": "rule", "text": "Write in my voice.", "file": "files/voice.md",
	}))
	if created.IsError {
		t.Fatalf("create: %s", created.ForLLM)
	}
	id := memoryIDFrom(t, created.ForLLM)

	res := run(t, h["memory_attach"], newCall(testSession, map[string]any{
		"id": id, "file": "files/voice-v2.md",
	}))
	if res.IsError || !strings.Contains(res.ForLLM, "Attached files/voice-v2.md") {
		t.Fatalf("repoint failed: %s", res.ForLLM)
	}
	got := run(t, h["explain"], newCall(testSession, map[string]any{"id": id}))
	if !strings.Contains(got.ForLLM, "files/voice-v2.md") {
		t.Fatalf("explain should show the new file: %s", got.ForLLM)
	}

	res = run(t, h["memory_attach"], newCall(testSession, map[string]any{"id": id, "file": ""}))
	if res.IsError || !strings.Contains(res.ForLLM, "Detached") {
		t.Fatalf("detach failed: %s", res.ForLLM)
	}
	got = run(t, h["explain"], newCall(testSession, map[string]any{"id": id}))
	if strings.Contains(got.ForLLM, "attached file") {
		t.Fatalf("memory should have no attachment after detach: %s", got.ForLLM)
	}
	if !strings.Contains(got.ForLLM, "Write in my voice.") {
		t.Fatalf("detach must not disturb the memory itself: %s", got.ForLLM)
	}
}

// Omitting "file" entirely is a detach, not an error.
func TestMemoryAttachWithoutFileDetaches(t *testing.T) {
	h, ws := buildHandlersWithFiles(t)
	if err := os.WriteFile(filepath.Join(ws, "files", "voice.md"), []byte("doc"), 0o644); err != nil {
		t.Fatal(err)
	}
	created := run(t, h["memory_create"], newCall(testSession, map[string]any{
		"type": "rule", "text": "voice", "file": "files/voice.md",
	}))
	id := memoryIDFrom(t, created.ForLLM)

	res := run(t, h["memory_attach"], newCall(testSession, map[string]any{"id": id}))
	if res.IsError || !strings.Contains(res.ForLLM, "Detached") {
		t.Fatalf("expected a detach: %s", res.ForLLM)
	}
}

// The same permission and format rules apply as at create time.
func TestMemoryAttachRejectsBadFile(t *testing.T) {
	h, ws := buildHandlersWithFiles(t)
	if err := os.WriteFile(filepath.Join(ws, "files", "voice.md"), []byte("doc"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(ws, "secrets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "secrets", "keys.md"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	created := run(t, h["memory_create"], newCall(testSession, map[string]any{
		"type": "rule", "text": "voice", "file": "files/voice.md",
	}))
	id := memoryIDFrom(t, created.ForLLM)

	for _, bad := range []string{"secrets/keys.md", "files/missing.md", "files/voice.txt"} {
		res := run(t, h["memory_attach"], newCall(testSession, map[string]any{"id": id, "file": bad}))
		if !res.IsError || !strings.Contains(res.ForLLM, "attachment rejected") {
			t.Fatalf("expected %q to be rejected: %s", bad, res.ForLLM)
		}
	}
	// The original attachment survived every rejected change.
	got := run(t, h["explain"], newCall(testSession, map[string]any{"id": id}))
	if !strings.Contains(got.ForLLM, "files/voice.md") {
		t.Fatalf("a rejected attach must not clear the existing file: %s", got.ForLLM)
	}
}

func TestMemoryAttachUnknownMemory(t *testing.T) {
	h, ws := buildHandlersWithFiles(t)
	if err := os.WriteFile(filepath.Join(ws, "files", "voice.md"), []byte("doc"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := run(t, h["memory_attach"], newCall(testSession, map[string]any{
		"id": "hNOPE1", "file": "files/voice.md",
	}))
	if !res.IsError {
		t.Fatalf("expected an error for an unknown memory id: %s", res.ForLLM)
	}
}

// memoryIDFrom pulls the assigned id out of a memory_create confirmation
// ("Stored memory hAB12C in domain ...").
func memoryIDFrom(t *testing.T, msg string) string {
	t.Helper()
	fields := strings.Fields(msg)
	for i, f := range fields {
		if f == "memory" && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	t.Fatalf("no memory id in %q", msg)
	return ""
}

// The common case — no attachment — is unchanged.
func TestMemoryCreateWithoutAttachment(t *testing.T) {
	h, _ := buildHandlersWithFiles(t)

	res := run(t, h["memory_create"], newCall(testSession, map[string]any{
		"type": "preference", "text": "Be concise.",
	}))
	if res.IsError {
		t.Fatalf("plain memory_create failed: %s", res.ForLLM)
	}
	if strings.Contains(res.ForLLM, "Attached") {
		t.Fatalf("unexpected attachment mention: %s", res.ForLLM)
	}
}
