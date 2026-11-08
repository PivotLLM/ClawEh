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
	utils.SetMediaStagingDir(t.TempDir(), 0o700, 0o600)
	t.Cleanup(func() { utils.SetMediaStagingDir("", 0o700, 0o600) })

	cfg := newTestConfig(t)
	cfg.Agents.Defaults.RestrictToWorkspace = true
	cfg.Agents.List = []config.AgentConfig{{ID: "alice", Name: "Alice", Default: true, Tools: []string{"msg_send_file"}}}
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
	return l.agent.Tools.ExecuteWithContext(context.Background(), "msg_send_file",
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
	notes := l.write(t, "notes.md", "# notes")
	late := make(chan bus.OutboundMediaMessage, 1)
	l.report = func(msg bus.OutboundMediaMessage) (error, bool) {
		late <- msg
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	abs := filepath.Join(l.agent.Workspace, notes)
	results, err := l.al.SendFiles(ctx, "telegram", "42", []tools.OutgoingFile{{Path: abs, Filename: "notes.md"}})
	if err != nil || len(results) != 1 || !errors.Is(results[0], tools.ErrDeliveryUnconfirmed) {
		t.Fatalf("SendFiles = %v, %v; want one unconfirmed delivery", results, err)
	}
	d := <-l.delivered
	(<-late).OnDelivery(nil)
	waitGone(t, d.path)
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
	r := clone.Tools.ExecuteWithContext(context.Background(), "msg_send_file",
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
