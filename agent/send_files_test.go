package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/media"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/tools"
	toolsmsg "github.com/PivotLLM/ClawEh/tools/msg"
	"github.com/PivotLLM/ClawEh/utils"
)

// fakeFileChannels says which channels can send files.
type fakeFileChannels map[string]bool

func (f fakeFileChannels) SupportsMedia(channel string) bool { return f[channel] }

// deliveredFile is one file the fake channel took: the message and the
// staged copy's content when it was sent.
type deliveredFile struct {
	msg     bus.OutboundMediaMessage
	path    string
	content string
}

// sendFilesLoop is a loop whose agent alice may use msg_send_file, with a
// media store, a telegram channel that can send files and a fake channel
// worker answering every file with report (nil: delivered; set it before
// the first send).
type sendFilesLoop struct {
	al        *AgentLoop
	agent     *AgentInstance
	store     *media.FileMediaStore
	delivered chan deliveredFile
	report    func(bus.OutboundMediaMessage) (error, bool)
}

func newSendFilesLoop(t *testing.T) *sendFilesLoop {
	t.Helper()
	return newSendFilesLoopWithTools(t, "msg_send_file", "file_read_lines")
}

// newSendFilesLoopWithTools is newSendFilesLoop with alice granted grants.
func newSendFilesLoopWithTools(t *testing.T, grants ...string) *sendFilesLoop {
	t.Helper()
	utils.SetMediaStagingDir(t.TempDir(), 0o700, 0o600)
	t.Cleanup(func() { utils.SetMediaStagingDir("", 0o700, 0o600) })

	cfg := newTestConfig(t)
	cfg.Agents.Defaults.RestrictToWorkspace = true
	cfg.Agents.List = []config.AgentConfig{{ID: "alice", Name: "Alice", Default: true, Tools: grants}}
	msgBus := bus.NewMessageBus()
	t.Cleanup(msgBus.Close)
	tools.RegisterProvider(tools.NamespacedProvider("msg", toolsmsg.GlobalProvider)) // idempotent
	al := mustNewAgentLoop(t, cfg, msgBus, &mockProvider{}, nil)
	store := media.NewFileMediaStore()
	al.SetMediaStore(store)
	al.fileChannels = fakeFileChannels{"telegram": true, "webui": false}
	agent, ok := al.GetRegistry().Get("alice")
	if !ok {
		t.Fatal("no agent alice")
	}

	l := &sendFilesLoop{al: al, agent: agent, store: store, delivered: make(chan deliveredFile, 16)}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		for {
			msg, ok := msgBus.SubscribeOutboundMedia(ctx)
			if !ok {
				return
			}
			d := deliveredFile{msg: msg}
			if path, err := store.Resolve(msg.Parts[0].Ref); err == nil {
				d.path = path
				if data, err := os.ReadFile(path); err == nil {
					d.content = string(data)
				}
			}
			l.delivered <- d
			err, answer := error(nil), true
			if l.report != nil {
				err, answer = l.report(msg)
			}
			if answer {
				msg.OnDelivery(err)
			}
		}
	}()
	return l
}

// write puts a file in alice's workspace and returns its workspace path.
func (l *sendFilesLoop) write(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(l.agent.Workspace, "files", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return "files/" + name
}

// run calls msg_send_file as alice's registered tool in a turn answering
// channel/chatID.
func (l *sendFilesLoop) run(channel, chatID string, paths ...string) *tools.ToolResult {
	list := make([]any, len(paths))
	for i, p := range paths {
		list[i] = map[string]any{"path": p}
	}
	ctx := tools.WithReplyChat(context.Background(), tools.ReplyChat{Channel: channel, ChatID: chatID})
	return l.agent.Tools.ExecuteWithContext(ctx, "msg_send_file",
		map[string]any{"files": list}, channel, chatID, nil)
}

// The tool alice is given (through the global provider, as every agent gets
// it) sends readable files, in order, to the turn's chat: a staged copy of
// each, never the agent's own file, which the media store would delete.
// Images go as images, documents as files.
func TestSendFiles_RegisteredToolDeliversToTheTurnsChat(t *testing.T) {
	l := newSendFilesLoop(t)
	notes := l.write(t, "notes.md", "# notes")
	chart := l.write(t, "chart.png", "\x89PNG\r\n\x1a\n")

	r := l.run("telegram", "42", notes, chart)
	if r.IsError {
		t.Fatalf("result = %q, want success", r.ForLLM)
	}
	for _, want := range []struct{ name, kind, content string }{
		{"notes.md", "file", "# notes"},
		{"chart.png", "image", "\x89PNG\r\n\x1a\n"},
	} {
		d := <-l.delivered
		part := d.msg.Parts[0]
		if d.msg.Channel != "telegram" || d.msg.ChatID != "42" || part.Filename != want.name || part.Type != want.kind || d.content != want.content {
			t.Fatalf("delivered %+v (content %q), want %s as %s to telegram/42", d.msg, d.content, want.name, want.kind)
		}
		if strings.HasPrefix(d.path, l.agent.Workspace) {
			t.Fatalf("the channel was handed the agent's own file %s", d.path)
		}
		// The staged copy is removed once delivered; the agent's file stays.
		waitGone(t, d.path)
	}
	for _, line := range []string{"notes.md: sent", "chart.png: sent"} {
		if !strings.Contains(r.ForLLM, line) {
			t.Errorf("result %q lacks %q", r.ForLLM, line)
		}
	}
	if _, err := os.Stat(filepath.Join(l.agent.Workspace, notes)); err != nil {
		t.Fatalf("the agent's file is gone: %v", err)
	}
}

// A channel that refuses a file is reported per file with its reason.
func TestSendFiles_ReportsTheChannelsReason(t *testing.T) {
	l := newSendFilesLoop(t)
	notes := l.write(t, "notes.md", "# notes")
	l.report = func(bus.OutboundMediaMessage) (error, bool) {
		return errors.New("the Slack bot is missing the files:write permission: send failed"), true
	}
	r := l.run("telegram", "42", notes)
	if !r.IsError || !strings.Contains(r.ForLLM, "notes.md: not sent: the Slack bot is missing the files:write permission") {
		t.Fatalf("result = %+v, want the channel's reason", r)
	}
}

// A chat whose channel can't send files is refused before anything is
// published.
func TestSendFiles_ChatWithoutFileSupport(t *testing.T) {
	l := newSendFilesLoop(t)
	notes := l.write(t, "notes.md", "# notes")
	r := l.run("webui", "42", notes)
	if !r.IsError || !strings.Contains(r.ForLLM, "This chat can't receive files.") {
		t.Fatalf("result = %+v, want the refusal", r)
	}
	select {
	case d := <-l.delivered:
		t.Fatalf("published %+v, want nothing", d.msg)
	case <-time.After(50 * time.Millisecond):
	}
}

// A file the channel has not reported by the deadline is not claimed sent;
// its staged copy is removed when the report comes.
func TestSendFiles_UnconfirmedDelivery(t *testing.T) {
	l := newSendFilesLoop(t)
	late := make(chan bus.OutboundMediaMessage, 1)
	l.report = func(msg bus.OutboundMediaMessage) (error, bool) {
		late <- msg
		return nil, false
	}
	fc := fakeClock(l.al)
	type sent struct {
		results []error
		err     error
	}
	done := make(chan sent, 1)
	go func() {
		results, err := l.al.SendFiles(context.Background(), "telegram", "42",
			[]tools.OutgoingFile{{File: strings.NewReader("# notes"), Filename: "notes.md", MaxSize: 100}})
		done <- sent{results, err}
	}()
	d := <-l.delivered
	// Not over before the minute is.
	advanceWhenWaiting(t, fc, 1, sendFilesTimeout-time.Second)
	select {
	case got := <-done:
		t.Fatalf("SendFiles returned %v before its minute was up", got.results)
	case <-time.After(20 * time.Millisecond):
	}
	fc.Advance(time.Second)
	got := <-done
	if got.err != nil || len(got.results) != 1 || !errors.Is(got.results[0], tools.ErrDeliveryUnconfirmed) {
		t.Fatalf("SendFiles = %v, %v; want one unconfirmed delivery", got.results, got.err)
	}
	if _, err := os.Stat(d.path); err != nil {
		t.Fatalf("staged copy gone before its report: %v", err)
	}
	// The late report, delivered twice, neither blocks nor leaves the copy.
	msg := <-late
	msg.OnDelivery(nil)
	msg.OnDelivery(nil)
	waitGone(t, d.path)
}

// A file that grew past its limit after the tool checked it is not sent,
// and nothing is staged for it.
func TestSendFiles_GrownPastTheLimit(t *testing.T) {
	l := newSendFilesLoop(t)
	results, err := l.al.SendFiles(context.Background(), "telegram", "42",
		[]tools.OutgoingFile{{File: strings.NewReader("12345"), Filename: "notes.md", MaxSize: 4}})
	if err != nil || len(results) != 1 || results[0] == nil || !strings.Contains(results[0].Error(), "more than the limit of 4 bytes") {
		t.Fatalf("SendFiles = %v, %v; want the file refused as too large", results, err)
	}
	entries, err := os.ReadDir(utils.MediaTempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("staging folder holds %d files, want none", len(entries))
	}
}

// The staged copies a stop left behind are removed at the next start; other
// media in the staging folder is left alone.
func TestRemoveStagedFileCopies(t *testing.T) {
	newSendFilesLoop(t)
	dir := utils.MediaTempDir()
	for _, name := range []string{stagedPrefix + "1-notes.md", "photo.jpg"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	RemoveStagedFileCopies()
	if _, err := os.Stat(filepath.Join(dir, stagedPrefix+"1-notes.md")); !os.IsNotExist(err) {
		t.Fatalf("staged copy still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "photo.jpg")); err != nil {
		t.Fatalf("other media removed: %v", err)
	}
}

// An agent without a file-reading tool cannot send files.
func TestSendFiles_RequiresReadGrant(t *testing.T) {
	l := newSendFilesLoopWithTools(t, "msg_send_file")
	notes := l.write(t, "notes.md", "# notes")
	r := l.run("telegram", "42", notes)
	if !r.IsError || !strings.Contains(r.ForLLM, "You can only send files you're allowed to read.") {
		t.Fatalf("result = %+v, want the refusal", r)
	}
}

// In a turn the loop runs, the model's msg_send_file sends to the turn's
// own chat; on claw agent's cli channel it is refused.
func TestSendFiles_InLoopTurnChat(t *testing.T) {
	for _, tt := range []struct {
		channel, chatID string
		sent            bool
	}{
		{"telegram", "42", true},
		{"cli", "direct", false},
	} {
		t.Run(tt.channel, func(t *testing.T) {
			l := newSendFilesLoop(t)
			notes := l.write(t, "notes.md", "# notes")
			call := toolCallTo("tc-1", "msg_send_file")
			call.Function.Arguments = `{"files":[{"path":"` + notes + `"}]}`
			call.Arguments = map[string]any{"files": []any{map[string]any{"path": notes}}}
			provider := &sequenceProvider{
				responses: []*providers.LLMResponse{{ToolCalls: []providers.ToolCall{call}}, {Content: "done"}},
				errors:    []error{nil, nil},
			}
			l.agent.Provider = provider
			if _, err := l.al.runAgentLoop(context.Background(), l.agent, processOptions{
				SessionKey: "agent:alice:main", Channel: tt.channel, ChatID: tt.chatID, UserMessage: "send it",
			}); err != nil {
				t.Fatalf("runAgentLoop: %v", err)
			}
			select {
			case d := <-l.delivered:
				if !tt.sent || d.msg.Channel != tt.channel || d.msg.ChatID != tt.chatID {
					t.Fatalf("delivered to %s/%s", d.msg.Channel, d.msg.ChatID)
				}
			case <-time.After(100 * time.Millisecond):
				if tt.sent {
					t.Fatal("nothing delivered")
				}
			}
		})
	}
}

// A turn records its reply chat on the session token while it runs, for a
// CLI provider's MCP calls: its own chat, an internal channel included.
func TestSendFiles_TurnRecordsReplyChat(t *testing.T) {
	for _, tt := range []struct{ channel, chatID string }{
		{"telegram", "42"},
		{"cli", "direct"},
		{"system", "x"},
		{"subagent", "agent:main:task"},
		{"agent_message", ""},
	} {
		t.Run(tt.channel, func(t *testing.T) {
			al := newTestAgentLoop(t).al
			agent := al.GetRegistry().Default()
			rec := &recordingSTI{}
			al.SetSessionTokenIssuer(rec)
			var during []sourceCall
			agent.Provider = &probeProvider{onChat: func() { during = rec.activeTurns() }}
			if _, err := al.runAgentLoop(context.Background(), agent, processOptions{
				SessionKey: "agent:main:main", Channel: tt.channel, ChatID: tt.chatID, UserMessage: "hi",
			}); err != nil {
				t.Fatalf("runAgentLoop: %v", err)
			}
			if len(during) != 1 || during[0].channel != tt.channel || during[0].chatID != tt.chatID {
				t.Fatalf("turns during the call = %+v, want %s/%s", during, tt.channel, tt.chatID)
			}
			if left := rec.activeTurns(); len(left) != 0 {
				t.Fatalf("turns after = %+v, want none", left)
			}
		})
	}
}

// probeProvider calls onChat on each request and answers "done".
type probeProvider struct{ onChat func() }

func (p *probeProvider) Chat(context.Context, []providers.Message, []providers.ToolDefinition, string, map[string]any) (*providers.LLMResponse, error) {
	p.onChat()
	return &providers.LLMResponse{Content: "done"}, nil
}

func (p *probeProvider) GetDefaultModel() string { return "test" }

func TestDetectMediaType(t *testing.T) {
	dir := t.TempDir()
	for _, tt := range []struct {
		name    string
		content []byte
		want    string
	}{
		{"image.dat", []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, "image/png"}, // magic bytes win
		{"mystery", []byte{0x00, 0x01, 0x02}, "application/octet-stream"},
	} {
		path := filepath.Join(dir, tt.name)
		if err := os.WriteFile(path, tt.content, 0o600); err != nil {
			t.Fatal(err)
		}
		if got := detectMediaType(path); got != tt.want {
			t.Errorf("detectMediaType(%s) = %q, want %q", tt.name, got, tt.want)
		}
	}
	txt := filepath.Join(dir, "readme.txt")
	if err := os.WriteFile(txt, []byte("hello world"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := detectMediaType(txt); got == "application/octet-stream" {
		t.Errorf("detectMediaType(readme.txt) = %q, want the extension's type", got)
	}
}

// A clone's msg_send_file reads with its source's permissions: its
// source's workspace files can be sent.
func TestSendFiles_CloneActsAsSource(t *testing.T) {
	l := newSendFilesLoop(t)
	notes := l.write(t, "notes.md", "# notes")
	cloneID, err := l.al.GetRegistry().CreateClone("alice")
	if err != nil {
		t.Fatal(err)
	}
	clone, ok := l.al.GetRegistry().Get(cloneID)
	if !ok {
		t.Fatal("clone not registered")
	}
	ctx := tools.WithReplyChat(context.Background(), tools.ReplyChat{Channel: "telegram", ChatID: "42"})
	r := clone.Tools.ExecuteWithContext(ctx, "msg_send_file",
		map[string]any{"files": []any{map[string]any{"path": notes}}}, "telegram", "42", nil)
	if r.IsError || !strings.Contains(r.ForLLM, "notes.md: sent") {
		t.Fatalf("result = %+v, want sent", r)
	}
}

// waitGone waits for path to be removed.
func waitGone(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s was not removed", path)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
