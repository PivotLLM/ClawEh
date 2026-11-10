package msg

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/constants"
	"github.com/PivotLLM/ClawEh/tools"
	"github.com/PivotLLM/ClawEh/tools/files"
	"github.com/PivotLLM/ClawEh/utils"
)

// fakeSender records what it is asked to send, reading each file as the
// agent loop would, and answers with results, or err.
type fakeSender struct {
	channel, chatID string
	files           []tools.OutgoingFile
	contents        []string
	results         []error
	err             error
}

func (s *fakeSender) SendFiles(_ context.Context, channel, chatID string, files []tools.OutgoingFile) ([]error, error) {
	s.channel, s.chatID, s.files = channel, chatID, files
	s.contents = nil
	for _, f := range files {
		data, err := io.ReadAll(f.File)
		if err != nil {
			return nil, err
		}
		s.contents = append(s.contents, string(data))
	}
	if s.err != nil {
		return nil, s.err
	}
	if s.results != nil {
		return s.results, nil
	}
	return make([]error, len(files)), nil
}

// sendFixture is a workspace with files, a file outside it, and the tool
// reading the workspace with the file tools' confinement on.
type sendFixture struct {
	workspace string
	outside   string
	sender    *fakeSender
	tool      *SendFileTool
}

func newSendFixture(t *testing.T, maxSize int) *sendFixture {
	t.Helper()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	outside := filepath.Join(root, "outside")
	for _, dir := range []string{filepath.Join(workspace, "files"), outside} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, content string) {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(workspace, "files", "notes.md"), "# notes")
	write(filepath.Join(workspace, "files", "chart.png"), "\x89PNG\r\n\x1a\n")
	write(filepath.Join(outside, "secret.txt"), "secret")
	cfg := &config.Config{}
	cfg.Agents.Defaults.RestrictToWorkspace = true
	sender := &fakeSender{}
	return &sendFixture{
		workspace: workspace,
		outside:   outside,
		sender:    sender,
		tool:      NewSendFileTool(files.NewReader(cfg, workspace), true, maxSize, sender),
	}
}

// chatCtx is the context of a turn answering chat 42 on telegram.
func chatCtx() context.Context {
	return tools.WithReplyChat(context.Background(), tools.ReplyChat{Channel: "telegram", ChatID: "42"})
}

func filesArg(entries ...map[string]any) map[string]any {
	list := make([]any, len(entries))
	for i, e := range entries {
		list[i] = e
	}
	return map[string]any{"files": list}
}

func requireRefusal(t *testing.T, r *tools.ToolResult, want string) {
	t.Helper()
	if !r.IsError || !tools.IsExpectedRefusal(r.Err) {
		t.Fatalf("result = %+v, want a refusal", r)
	}
	if !strings.Contains(r.ForLLM, want) {
		t.Fatalf("refusal = %q, want it to contain %q", r.ForLLM, want)
	}
}

// The old single "path" argument and malformed "files" are refused, and
// nothing is sent.
func TestSendFile_RefusesMalformedArguments(t *testing.T) {
	eleven := make([]map[string]any, MaxSendFiles+1)
	for i := range eleven {
		eleven[i] = map[string]any{"path": "files/notes.md"}
	}
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{"old path argument", map[string]any{"path": "files/notes.md"}, `"path" is no longer accepted`},
		{"no files", map[string]any{}, `"files" is required`},
		{"empty files", map[string]any{"files": []any{}}, `"files" is required`},
		{"too many files", filesArg(eleven...), fmt.Sprintf("At most %d files", MaxSendFiles)},
		{"entry not an object", map[string]any{"files": []any{"files/notes.md"}}, `files[0] must be an object`},
		{"entry without path", filesArg(map[string]any{"filename": "x.md"}), `files[0] needs a "path"`},
		{"unknown field", filesArg(map[string]any{"path": "files/notes.md", "caption": "hi"}), `unknown field "caption"`},
		{"filename is a path", filesArg(map[string]any{"path": "files/notes.md", "filename": "../x.md"}), "plain file name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newSendFixture(t, 0)
			requireRefusal(t, f.tool.Execute(chatCtx(), tt.args), tt.want)
			if f.sender.files != nil {
				t.Fatalf("sent %v, want nothing", f.sender.files)
			}
		})
	}
}

// Files go only where the turn's reply goes: never to another agent, not to
// an internal channel, and not at all without a turn or when the call cannot
// be tied to one turn.
func TestSendFile_Destination(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
		want string
	}{
		{"asked turn", tools.WithReplyChat(context.Background(), tools.ReplyChat{Channel: constants.AgentMessageChannel}), "Files can't be sent to another agent."},
		{"sub-agent turn", tools.WithReplyChat(context.Background(), tools.ReplyChat{Channel: constants.SubagentChannel, ChatID: "agent:alice:task"}), "Files can't be sent to another agent."},
		{"claw agent (cli) turn", tools.WithReplyChat(context.Background(), tools.ReplyChat{Channel: "cli", ChatID: "direct"}), "This chat can't receive files."},
		{"recovery turn", tools.WithReplyChat(context.Background(), tools.ReplyChat{Channel: "recovery", ChatID: "x"}), "This chat can't receive files."},
		{"more than one turn", tools.WithReplyChat(context.Background(), tools.ReplyChat{Ambiguous: true}), "answering more than one conversation"},
		{"no turn (service token)", context.Background(), "There is no chat to send files to."},
		{"tool context only", tools.WithToolContext(context.Background(), "telegram", "42"), "There is no chat to send files to."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newSendFixture(t, 0)
			requireRefusal(t, f.tool.Execute(tt.ctx, filesArg(map[string]any{"path": "files/notes.md"})), tt.want)
			if f.sender.files != nil {
				t.Fatalf("sent %v, want nothing", f.sender.files)
			}
		})
	}
}

// An agent without a file-reading tool can send no file.
func TestSendFile_RequiresReadGrant(t *testing.T) {
	f := newSendFixture(t, 0)
	f.tool.canRead = false
	requireRefusal(t, f.tool.Execute(chatCtx(), filesArg(map[string]any{"path": "files/notes.md"})), "You can only send files you're allowed to read.")
	if f.sender.files != nil {
		t.Fatalf("sent %v, want nothing", f.sender.files)
	}

	for _, tt := range []struct {
		tools []string
		want  bool
	}{
		{[]string{"file_read_lines"}, true},
		{[]string{"file_read_bytes"}, true},
		{[]string{"file_*"}, true},
		{[]string{"msg_send_file"}, false},
		{nil, false},
	} {
		if got := CanReadFiles(&config.AgentConfig{Tools: tt.tools}); got != tt.want {
			t.Errorf("CanReadFiles(%v) = %v, want %v", tt.tools, got, tt.want)
		}
	}
	if CanReadFiles(&config.AgentConfig{Tools: []string{"file_read_lines"}, DenyTools: []string{"file_read_lines"}}) {
		t.Error("CanReadFiles ignores a denied read tool")
	}
}

// A FIFO is refused without blocking.
func TestSendFile_SpecialFiles(t *testing.T) {
	f := newSendFixture(t, 0)
	fifo := filepath.Join(f.workspace, "files", "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	requireRefusal(t, f.tool.Execute(chatCtx(), filesArg(map[string]any{"path": "files/pipe"})), "it is not a regular file")

	// The reader's open itself: a FIFO opens without waiting for a writer.
	pf, err := f.tool.reader.Open(fifo)
	if err != nil {
		t.Fatalf("Open(fifo) = %v, want it open without blocking", err)
	}
	utils.CloseQuietly(pf)
}

// Every path is checked with the agent's file-reading permission before
// anything is sent; a path it may not read is refused by name.
func TestSendFile_ReadPermission(t *testing.T) {
	f := newSendFixture(t, 0)
	link := filepath.Join(f.workspace, "files", "link.txt")
	if err := os.Symlink(filepath.Join(f.outside, "secret.txt"), link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(f.outside, "secret.txt"), // outside the workspace
		"../outside/secret.txt",                // escapes by a relative path
		"files/link.txt",                       // a link inside to a file outside
		"files/missing.md",                     // does not exist
	} {
		t.Run(path, func(t *testing.T) {
			r := f.tool.Execute(chatCtx(), filesArg(
				map[string]any{"path": "files/notes.md"},
				map[string]any{"path": path},
			))
			requireRefusal(t, r, fmt.Sprintf("Can't send %q", path))
			if !strings.Contains(r.ForLLM, "Nothing was sent.") || f.sender.files != nil {
				t.Fatalf("result %q, sent %v: want nothing sent", r.ForLLM, f.sender.files)
			}
		})
	}
}

// A folder and a file over the media size limit are refused.
func TestSendFile_FolderAndSize(t *testing.T) {
	f := newSendFixture(t, 4)
	requireRefusal(t, f.tool.Execute(chatCtx(), filesArg(map[string]any{"path": "files"})), "it is a folder")
	requireRefusal(t, f.tool.Execute(chatCtx(), filesArg(map[string]any{"path": "files/notes.md"})), "more than the limit of 4 bytes")
}

// Readable files are handed to the sender in order, for the turn's chat,
// with their names and types; the result says each was sent.
func TestSendFile_SendsInOrder(t *testing.T) {
	f := newSendFixture(t, 0)
	r := f.tool.Execute(chatCtx(), filesArg(
		map[string]any{"path": "files/notes.md"},
		map[string]any{"path": "files/chart.png", "filename": "q3.png"},
	))
	if r.IsError {
		t.Fatalf("result = %q, want success", r.ForLLM)
	}
	if f.sender.channel != "telegram" || f.sender.chatID != "42" {
		t.Fatalf("sent to %s/%s, want telegram/42", f.sender.channel, f.sender.chatID)
	}
	wantNames := []string{"notes.md", "q3.png"}
	wantContents := []string{"# notes", "\x89PNG\r\n\x1a\n"}
	if len(f.sender.files) != 2 {
		t.Fatalf("sent %d files, want 2", len(f.sender.files))
	}
	for i, got := range f.sender.files {
		if got.Filename != wantNames[i] || f.sender.contents[i] != wantContents[i] || got.MaxSize != config.DefaultMaxMediaSize {
			t.Errorf("file %d = %+v (content %q), want %s", i, got, f.sender.contents[i], wantNames[i])
		}
	}
	for _, line := range []string{"notes.md: sent", "q3.png: sent"} {
		if !strings.Contains(r.ForLLM, line) {
			t.Errorf("result %q lacks %q", r.ForLLM, line)
		}
	}
}

// The result reports each file's outcome: a channel's reason is an error,
// a delivery not yet confirmed is said so, never "sent".
func TestSendFile_ReportsEachOutcome(t *testing.T) {
	f := newSendFixture(t, 0)
	f.sender.results = []error{nil, errors.New("telegram send media: send failed: Bad Request: file is too big")}
	r := f.tool.Execute(chatCtx(), filesArg(
		map[string]any{"path": "files/notes.md"},
		map[string]any{"path": "files/chart.png"},
	))
	if !r.IsError {
		t.Fatalf("result = %q, want an error", r.ForLLM)
	}
	for _, line := range []string{"notes.md: sent", "chart.png: not sent: telegram send media: send failed: Bad Request: file is too big"} {
		if !strings.Contains(r.ForLLM, line) {
			t.Errorf("result %q lacks %q", r.ForLLM, line)
		}
	}

	f.sender.results = []error{tools.ErrDeliveryUnconfirmed}
	r = f.tool.Execute(chatCtx(), filesArg(map[string]any{"path": "files/notes.md"}))
	if r.IsError || !strings.Contains(r.ForLLM, "notes.md: not confirmed yet") || strings.Contains(r.ForLLM, ": sent") {
		t.Fatalf("result = %+v, want it unconfirmed, not sent", r)
	}
}

// A chat whose channel can't send files is refused.
func TestSendFile_ChatWithoutFileSupport(t *testing.T) {
	f := newSendFixture(t, 0)
	f.sender.err = tools.ErrNoFileSupport
	requireRefusal(t, f.tool.Execute(chatCtx(), filesArg(map[string]any{"path": "files/notes.md"})), "This chat can't receive files.")
}
