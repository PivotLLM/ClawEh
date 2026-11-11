// ClawEh
// License: MIT

package agent

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/PivotLLM/ClawEh/classifier"
	"github.com/PivotLLM/ClawEh/config"
)

// instructionsHost is a host over Alice, granted file_read_bytes, with
// files/classifiers/inbox in her workspace; it returns the host and the
// workspace.
func instructionsHost(t *testing.T, tools []string, change ...func(*config.Config)) (*ClassifierHost, string) {
	t.Helper()
	cfg := classifierHostConfig(t, "http://127.0.0.1:0")
	cfg.Agents.List[0].Tools = tools
	for _, c := range change {
		c(cfg)
	}
	al, host, _ := newClassifierHostLoop(t, cfg)
	alice, ok := al.GetRegistry().GetConfigured("alice")
	if !ok {
		t.Fatal("no alice")
	}
	if err := os.MkdirAll(filepath.Join(alice.Workspace, "files", "classifiers", "inbox", "rules"), 0o700); err != nil {
		t.Fatal(err)
	}
	return host, alice.Workspace
}

func writeInstr(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func symlinkInstr(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// The folder gives every regular, non-hidden .md file directly in it (a
// link inside files/ followed); sub-folders, other names, FIFOs and links
// to folders are skipped.
func TestClassifierHost_ReadInstructionsDir(t *testing.T) {
	host, ws := instructionsHost(t, []string{"file_read_bytes"})
	inbox := filepath.Join(ws, "files", "classifiers", "inbox")
	rules := filepath.Join(inbox, "rules")
	writeInstr(t, filepath.Join(inbox, "guide.md"), "GUIDE")
	writeInstr(t, filepath.Join(rules, "example.com.md"), "EXAMPLE")
	writeInstr(t, filepath.Join(rules, "Billing.MD"), "BILLING")
	writeInstr(t, filepath.Join(rules, ".hidden.md"), "HIDDEN")
	writeInstr(t, filepath.Join(rules, "notes.txt"), "NOTES")
	writeInstr(t, filepath.Join(rules, "sub", "deep.md"), "DEEP")
	if err := os.Mkdir(filepath.Join(rules, "folder.md"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(rules, "pipe.md"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlinkInstr(t, "../guide.md", filepath.Join(rules, "linked.md"))
	symlinkInstr(t, "sub", filepath.Join(rules, "dirlink.md"))

	got, err := host.ReadInstructionsDir("alice", "files/classifiers/inbox/rules")
	if err != nil {
		t.Fatalf("ReadInstructionsDir: %v", err)
	}
	texts := map[string]string{}
	for _, f := range got {
		texts[f.Name] = f.Text
	}
	want := map[string]string{"example.com.md": "EXAMPLE", "Billing.MD": "BILLING", "linked.md": "GUIDE"}
	if len(texts) != len(want) {
		t.Fatalf("files = %v, want %v", texts, want)
	}
	for name, text := range want {
		if texts[name] != text {
			t.Errorf("%s = %q, want %q", name, texts[name], text)
		}
	}

	if _, err := host.ReadInstructionsDir("alice", "files/classifiers/missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing folder = %v, want ErrNotExist", err)
	}
	if _, err := host.ReadInstructionsDir("alice", "files/classifiers/inbox/guide.md"); err == nil || err.Error() != "it is not a folder" {
		t.Errorf("a file as the folder = %v", err)
	}
}

// A link out of files/ is refused, as a file, as a folder and inside a
// folder (not skipped); so are a folder, a FIFO and a file over the cap
// named as the file.
func TestClassifierHost_ReadInstructionsRefusals(t *testing.T) {
	host, ws := instructionsHost(t, []string{"file_read_bytes"})
	inbox := filepath.Join(ws, "files", "classifiers", "inbox")
	writeInstr(t, filepath.Join(ws, "memory", "secret.md"), "SECRET")
	outside := filepath.Join(t.TempDir(), "outside.md")
	writeInstr(t, outside, "OUTSIDE")
	symlinkInstr(t, "../../../memory/secret.md", filepath.Join(inbox, "workspace-link.md"))
	symlinkInstr(t, outside, filepath.Join(inbox, "host-link.md"))
	symlinkInstr(t, filepath.Dir(outside), filepath.Join(inbox, "host-dir"))
	writeInstr(t, filepath.Join(inbox, "escape", "ok.md"), "OK")
	symlinkInstr(t, outside, filepath.Join(inbox, "escape", "out.md"))
	if err := syscall.Mkfifo(filepath.Join(inbox, "pipe.md"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeInstr(t, filepath.Join(inbox, "big.md"), strings.Repeat("x", classifier.MaxInstructionsFileBytes+1))

	for _, rel := range []string{"workspace-link.md", "host-link.md"} {
		text, err := host.ReadInstructionsFile("alice", "files/classifiers/inbox/"+rel)
		if err == nil || errors.Is(err, fs.ErrNotExist) || strings.Contains(text, "SECRET") || strings.Contains(text, "OUTSIDE") {
			t.Errorf("%s = %q, %v; want a refusal", rel, text, err)
		}
	}
	if _, err := host.ReadInstructionsDir("alice", "files/classifiers/inbox/host-dir"); err == nil || errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a folder linked out of files/ = %v, want a refusal", err)
	}
	if got, err := host.ReadInstructionsDir("alice", "files/classifiers/inbox/escape"); err == nil || !strings.HasPrefix(err.Error(), "out.md: ") {
		t.Errorf("a folder holding a link out of files/ = %v, %v; want out.md refused", got, err)
	}
	for rel, want := range map[string]string{
		"rules":   "it is a folder",
		"pipe.md": "it is not a regular file",
		"big.md":  "it has more than 262144 bytes",
	} {
		if _, err := host.ReadInstructionsFile("alice", "files/classifiers/inbox/"+rel); err == nil || err.Error() != want {
			t.Errorf("%s = %v, want %q", rel, err, want)
		}
	}
	if _, err := host.ReadInstructionsFile("alice", "files/classifiers/inbox/none.md"); !errors.Is(err, fs.ErrNotExist) || err.Error() != "it does not exist" {
		t.Errorf("missing file = %v, want ErrNotExist", err)
	}
}

// The folder's .md files are capped together.
func TestClassifierHost_ReadInstructionsDirTotalCap(t *testing.T) {
	host, ws := instructionsHost(t, []string{"file_read_bytes"})
	rules := filepath.Join(ws, "files", "classifiers", "inbox", "rules")
	chunk := strings.Repeat("x", classifier.MaxInstructionsFileBytes)
	for _, n := range []string{"a.md", "b.md", "c.md", "d.md", "e.md"} {
		writeInstr(t, filepath.Join(rules, n), chunk)
	}
	if _, err := host.ReadInstructionsDir("alice", "files/classifiers/inbox/rules"); err == nil || !strings.Contains(err.Error(), "more than 1048576 bytes together") {
		t.Errorf("over the total cap = %v", err)
	}
}

// An agent without a file-reading tool reads no Markdown.
func TestClassifierHost_ReadInstructionsNeedsFileTool(t *testing.T) {
	host, ws := instructionsHost(t, []string{"web_fetch"})
	writeInstr(t, filepath.Join(ws, "files", "guide.md"), "GUIDE")
	_, err := host.ReadInstructionsFile("alice", "files/guide.md")
	if err == nil || !strings.Contains(err.Error(), "agent Alice has no file-reading tool") {
		t.Errorf("without a file tool = %v", err)
	}
	if _, err := NewClassifierHost().ReadInstructionsDir("alice", "files"); !errors.Is(err, errLoopNotReady) {
		t.Errorf("before Bind = %v", err)
	}
}

// An agent whose reads are not confined to its workspace still reads only
// inside files/: a link out of it is refused.
func TestClassifierHost_ReadInstructionsUnconfined(t *testing.T) {
	host, ws := instructionsHost(t, []string{"file_read_bytes"}, func(c *config.Config) {
		c.Agents.Defaults.RestrictToWorkspace = false
	})
	inbox := filepath.Join(ws, "files", "classifiers", "inbox")
	writeInstr(t, filepath.Join(inbox, "guide.md"), "GUIDE")
	outside := filepath.Join(t.TempDir(), "outside.md")
	writeInstr(t, outside, "OUTSIDE")
	symlinkInstr(t, outside, filepath.Join(inbox, "host-link.md"))
	if text, err := host.ReadInstructionsFile("alice", "files/classifiers/inbox/guide.md"); err != nil || text != "GUIDE" {
		t.Errorf("guide.md = %q, %v", text, err)
	}
	if _, err := host.ReadInstructionsFile("alice", "files/classifiers/inbox/host-link.md"); err == nil || err.Error() != "it leads outside files/" {
		t.Errorf("host-link.md = %v, want it refused", err)
	}
}
