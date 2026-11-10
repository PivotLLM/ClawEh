package msg

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/tools"
)

// swappingReader is the fixture's reader with a hook run when the target is
// checked (Allowed): the moment between the path being resolved and the
// file being opened, where a swap would happen; and one run just before the
// open itself, after every check.
type swappingReader struct {
	FileReader
	onAllowed func(abs string)
	onOpen    func(abs string)
}

func (r swappingReader) Open(path string) (*os.File, error) {
	if r.onOpen != nil {
		r.onOpen(path)
	}
	return r.FileReader.Open(path)
}

func (r swappingReader) Allowed(abs string) error {
	err := r.FileReader.Allowed(abs)
	if r.onAllowed != nil {
		r.onAllowed(abs)
	}
	return err
}

// withSwap gives f's tool a reader that runs swap after the permission check.
func (f *sendFixture) withSwap(swap func(abs string)) {
	f.tool.reader = swappingReader{FileReader: f.tool.reader, onAllowed: swap}
}

// A file replaced between the permission check and the open is never sent:
// a symbolic link to a file outside the workspace, a FIFO, a folder, or the
// file removed. Nothing of the batch is sent.
func TestSendFile_SwappedAfterCheck(t *testing.T) {
	for _, tt := range []struct {
		name string
		swap func(t *testing.T, f *sendFixture, abs string)
		want string
	}{
		{"link to a secret", func(t *testing.T, f *sendFixture, abs string) {
			t.Helper()
			mustDo(t, os.Remove(abs))
			mustDo(t, os.Symlink(filepath.Join(f.outside, "secret.txt"), abs))
		}, "it is not a regular file"},
		{"fifo", func(t *testing.T, _ *sendFixture, abs string) {
			t.Helper()
			mustDo(t, os.Remove(abs))
			mustDo(t, syscall.Mkfifo(abs, 0o600))
		}, "it is not a regular file"},
		{"folder", func(t *testing.T, _ *sendFixture, abs string) {
			t.Helper()
			mustDo(t, os.Remove(abs))
			mustDo(t, os.Mkdir(abs, 0o700))
		}, "it is a folder"},
		{"removed", func(t *testing.T, _ *sendFixture, abs string) {
			t.Helper()
			mustDo(t, os.Remove(abs))
		}, "you can't read it, or it does not exist"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newSendFixture(t, 0)
			f.withSwap(func(abs string) {
				if filepath.Base(abs) == "notes.md" {
					tt.swap(t, f, abs)
				}
			})
			done := make(chan *tools.ToolResult, 1)
			go func() {
				done <- f.tool.Execute(chatCtx(), filesArg(
					map[string]any{"path": "files/chart.png"},
					map[string]any{"path": "files/notes.md"},
				))
			}()
			var r *tools.ToolResult
			select {
			case r = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("msg_send_file blocked on the swapped file")
			}
			requireRefusal(t, r, `Can't send "files/notes.md": `+tt.want)
			if !strings.Contains(r.ForLLM, "Nothing was sent.") || f.sender.files != nil {
				t.Fatalf("result %q, sent %v: want nothing sent", r.ForLLM, f.sender.files)
			}
		})
	}
}

// A file replaced by a link after every check, just before the open, is
// never sent: a link leading outside the workspace is refused by the
// sandbox the open goes through, and a link to another file inside it is
// not the file checked.
func TestSendFile_LinkSwappedBeforeOpen(t *testing.T) {
	for _, tt := range []struct {
		name   string
		target func(f *sendFixture) string
		want   string
	}{
		{
			name:   "outside",
			target: func(f *sendFixture) string { return filepath.Join(f.outside, "secret.txt") },
			want:   "you can't read it, or it does not exist",
		},
		{
			name:   "inside",
			target: func(*sendFixture) string { return "chart.png" },
			want:   "it changed while it was being checked",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newSendFixture(t, 0)
			f.tool.reader = swappingReader{FileReader: f.tool.reader, onOpen: func(abs string) {
				if filepath.Base(abs) == "notes.md" {
					mustDo(t, os.Remove(abs))
					mustDo(t, os.Symlink(tt.target(f), abs))
				}
			}}
			r := f.tool.Execute(chatCtx(), filesArg(map[string]any{"path": "files/notes.md"}))
			requireRefusal(t, r, `Can't send "files/notes.md": `+tt.want)
			if f.sender.files != nil {
				t.Fatalf("sent %v: want nothing sent", f.sender.files)
			}
		})
	}
}

func mustDo(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// A link inside the workspace to a readable file is sent, read from its
// target, under the link's own name unless a name is given.
func TestSendFile_LinkToReadableFile(t *testing.T) {
	f := newSendFixture(t, 0)
	// A relative link, as the file tools follow one (os.Root refuses an
	// absolute target).
	mustDo(t, os.Symlink("notes.md", filepath.Join(f.workspace, "files", "link.md")))
	r := f.tool.Execute(chatCtx(), filesArg(
		map[string]any{"path": "files/link.md"},
		map[string]any{"path": "files/link.md", "filename": "renamed.md"},
	))
	if r.IsError {
		t.Fatalf("result = %+v, want sent", r)
	}
	if len(f.sender.files) != 2 || f.sender.files[0].Filename != "link.md" || f.sender.files[1].Filename != "renamed.md" {
		t.Fatalf("sent %+v", f.sender.files)
	}
	if f.sender.contents[0] != "# notes" || f.sender.contents[1] != "# notes" {
		t.Fatalf("contents %q, want the target's", f.sender.contents)
	}
	if r.ForLLM != "Sent to the chat:\nlink.md: sent\nrenamed.md: sent" {
		t.Fatalf("report = %q", r.ForLLM)
	}
}

// The size limit is inclusive: a file of exactly the limit is sent, one byte
// more is refused, and the limit given is what the sender enforces while
// copying (a file that grows after the check).
func TestSendFile_SizeBoundary(t *testing.T) {
	f := newSendFixture(t, len("# notes"))
	if r := f.tool.Execute(chatCtx(), filesArg(map[string]any{"path": "files/notes.md"})); r.IsError {
		t.Fatalf("file at the limit: %+v, want sent", r)
	}
	if got := f.sender.files[0].MaxSize; got != int64(len("# notes")) {
		t.Fatalf("MaxSize handed to the sender = %d", got)
	}
	f = newSendFixture(t, len("# notes")-1)
	requireRefusal(t, f.tool.Execute(chatCtx(), filesArg(map[string]any{"path": "files/notes.md"})),
		`Can't send "files/notes.md": it is 7 bytes, more than the limit of 6 bytes.`)
}

// Names a chat would show as a path, or that are not strings, are refused
// before anything is checked or sent.
func TestSendFile_FilenameValidation(t *testing.T) {
	f := newSendFixture(t, 0)
	for _, tt := range []struct {
		filename any
		want     string
	}{
		{"a/b.md", "files[0].filename must be a plain file name, not a path."},
		{`a\b.md`, "files[0].filename must be a plain file name, not a path."},
		{"..", "files[0].filename must be a plain file name, not a path."},
		{".", "files[0].filename must be a plain file name, not a path."},
		{42, "files[0].filename must be a string."},
	} {
		requireRefusal(t, f.tool.Execute(chatCtx(), filesArg(map[string]any{"path": "files/notes.md", "filename": tt.filename})), tt.want)
	}
	if f.sender.files != nil {
		t.Fatal("something was sent")
	}
	// A blank name is the file's own.
	if r := f.tool.Execute(chatCtx(), filesArg(map[string]any{"path": "files/notes.md", "filename": "  "})); r.IsError || f.sender.files[0].Filename != "notes.md" {
		t.Fatalf("blank filename: %+v, sent %+v", r, f.sender.files)
	}
}

// Partial failures: the report gives each file's outcome and is an error
// when any was not sent; only unconfirmed ones leave it a success.
func TestSendFile_PartialOutcomes(t *testing.T) {
	f := newSendFixture(t, 0)
	f.sender.results = []error{nil, tools.ErrDeliveryUnconfirmed}
	r := f.tool.Execute(chatCtx(), filesArg(map[string]any{"path": "files/notes.md"}, map[string]any{"path": "files/chart.png"}))
	if r.IsError || !strings.Contains(r.ForLLM, "notes.md: sent\nchart.png: ") || strings.Contains(r.ForLLM, "Sent to the chat") {
		t.Fatalf("result = %+v, want each outcome, not an error", r)
	}
	f.sender.results = []error{errors.New("chat not found"), tools.ErrDeliveryUnconfirmed}
	r = f.tool.Execute(chatCtx(), filesArg(map[string]any{"path": "files/notes.md"}, map[string]any{"path": "files/chart.png"}))
	if !r.IsError || !strings.Contains(r.ForLLM, "notes.md: not sent: chat not found") {
		t.Fatalf("result = %+v, want the failure reported", r)
	}
	// A sender reporting fewer results than files: the rest count as sent
	// only when nothing says otherwise.
	f.sender.results = []error{nil}
	r = f.tool.Execute(chatCtx(), filesArg(map[string]any{"path": "files/notes.md"}, map[string]any{"path": "files/chart.png"}))
	if r.IsError || r.ForLLM != "Sent to the chat:\nnotes.md: sent\nchart.png: sent" {
		t.Fatalf("result = %+v", r)
	}
}

// A sender that fails outright is an error naming why; no sender at all is
// "not available".
func TestSendFile_SenderFailure(t *testing.T) {
	f := newSendFixture(t, 0)
	f.sender.err = errors.New("no media store")
	r := f.tool.Execute(chatCtx(), filesArg(map[string]any{"path": "files/notes.md"}))
	if !r.IsError || r.ForLLM != "The files could not be sent: no media store." || tools.IsExpectedRefusal(r.Err) {
		t.Fatalf("result = %+v", r)
	}
	none := NewSendFileTool(f.tool.reader, true, 0, nil)
	r = none.Execute(chatCtx(), filesArg(map[string]any{"path": "files/notes.md"}))
	if !r.IsError || r.ForLLM != "Sending files is not available." {
		t.Fatalf("result = %+v", r)
	}
}

// The sentences each kind of turn gets, in the order the rules apply: the
// chat is decided before the read grant.
func TestSendFile_RefusalSentences(t *testing.T) {
	f := newSendFixture(t, 0)
	noRead := NewSendFileTool(f.tool.reader, false, 0, f.sender)
	for _, tt := range []struct {
		name string
		ctx  context.Context
		tool *SendFileTool
		want string
	}{
		{"no turn", context.Background(), f.tool, "There is no chat to send files to."},
		{"several turns", tools.WithReplyChat(context.Background(), tools.ReplyChat{Channel: "telegram", ChatID: "42", Ambiguous: true}), f.tool, "Files can't be sent now: this agent is answering more than one conversation."},
		{"ask", tools.WithReplyChat(context.Background(), tools.ReplyChat{Channel: "agent_message"}), f.tool, "Files can't be sent to another agent."},
		{"sub-agent", tools.WithReplyChat(context.Background(), tools.ReplyChat{Channel: "subagent", ChatID: "t"}), f.tool, "Files can't be sent to another agent."},
		{"cli", tools.WithReplyChat(context.Background(), tools.ReplyChat{Channel: "cli", ChatID: "direct"}), f.tool, "This chat can't receive files."},
		{"no chat id", tools.WithReplyChat(context.Background(), tools.ReplyChat{Channel: "telegram"}), f.tool, "This chat can't receive files."},
		{"no read grant", chatCtx(), noRead, "You can only send files you're allowed to read."},
		{"no read grant, ask", tools.WithReplyChat(context.Background(), tools.ReplyChat{Channel: "agent_message"}), noRead, "Files can't be sent to another agent."},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := tt.tool.Execute(tt.ctx, filesArg(map[string]any{"path": "files/notes.md"}))
			requireRefusal(t, r, tt.want)
			if r.ForLLM != tt.want {
				t.Fatalf("refusal = %q, want exactly %q", r.ForLLM, tt.want)
			}
		})
	}
	if f.sender.files != nil {
		t.Fatal("something was sent")
	}
}
