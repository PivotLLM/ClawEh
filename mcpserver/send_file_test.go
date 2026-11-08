// ClawEh
// License: MIT

package mcpserver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/constants"
	"github.com/PivotLLM/ClawEh/tools"
	"github.com/PivotLLM/ClawEh/tools/files"
	toolsmsg "github.com/PivotLLM/ClawEh/tools/msg"
)

// recordingFileSender records the chat it was asked to send files to.
type recordingFileSender struct {
	channel, chatID string
	files           []tools.OutgoingFile
}

func (s *recordingFileSender) SendFiles(_ context.Context, channel, chatID string, files []tools.OutgoingFile) ([]error, error) {
	s.channel, s.chatID, s.files = channel, chatID, files
	return make([]error, len(files)), nil
}

// sendFileHost is alice's registry with msg_send_file over a workspace
// holding notes.md, and a token store with alice's conversation token.
type sendFileHost struct {
	st     *SessionTokenStore
	tok    string
	sender *recordingFileSender
	regs   map[string]*tools.ToolRegistry
}

func newSendFileHost(t *testing.T) *sendFileHost {
	t.Helper()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "notes.md"), []byte("# notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Agents.Defaults.RestrictToWorkspace = true
	sender := &recordingFileSender{}
	st := newSessionTokenStore()
	return &sendFileHost{
		st:     st,
		tok:    st.Issue("alice", "agent:alice:main", "/ws/alice/sessions"),
		sender: sender,
		regs: map[string]*tools.ToolRegistry{"alice": newRegistryWith(
			toolsmsg.NewSendFileTool(files.NewReader(cfg, workspace), true, 0, sender))},
	}
}

func (h *sendFileHost) call(tok string) (string, bool) {
	args := map[string]any{"session_token": tok, "files": []any{map[string]any{"path": "notes.md"}}}
	return dispatchToolCall(context.Background(), "msg_send_file", args, h.st, resolverFor(h.regs), nil, nil, nil, nil)
}

// msg_send_file called by a CLI provider over the MCP host sends to the
// chat of the turn running on the token's session, exactly as in the loop,
// and never to the chat the session last heard from.
func TestDispatch_SendFileGoesToTheTurnsChat(t *testing.T) {
	h := newSendFileHost(t)
	// An earlier turn answered a chat; no turn runs.
	h.st.BeginTurn("agent:alice:main", "telegram", "42")()
	if out, isErr := h.call(h.tok); !isErr || !strings.Contains(out, "There is no chat to send files to.") {
		t.Fatalf("no turn: dispatch = %q (error %v), want the refusal", out, isErr)
	}

	end := h.st.BeginTurn("agent:alice:main", "slack", "C1")
	out, isErr := h.call(h.tok)
	if isErr || !strings.Contains(out, "notes.md: sent") {
		t.Fatalf("dispatch = %q (error %v), want sent", out, isErr)
	}
	if h.sender.channel != "slack" || h.sender.chatID != "C1" {
		t.Fatalf("sent to %s/%s, want the turn's slack/C1", h.sender.channel, h.sender.chatID)
	}
	end()
}

// A turn on an internal channel (claw agent's cli, a recovery or system
// replay) keeps its files from the chat the session last heard from.
func TestDispatch_SendFileInternalTurn(t *testing.T) {
	for _, channel := range []string{"cli", "recovery", "system"} {
		t.Run(channel, func(t *testing.T) {
			h := newSendFileHost(t)
			h.st.BeginTurn("agent:alice:main", "telegram", "42")() // an earlier chat turn
			end := h.st.BeginTurn("agent:alice:main", channel, "direct")
			defer end()
			if out, isErr := h.call(h.tok); !isErr || !strings.Contains(out, "This chat can't receive files.") {
				t.Fatalf("dispatch = %q (error %v), want the refusal", out, isErr)
			}
			if h.sender.files != nil {
				t.Fatalf("sent to %s/%s", h.sender.channel, h.sender.chatID)
			}
		})
	}
}

// An ask running beside a chat turn on the same session: the call cannot be
// tied to one of them, so nothing is sent; once the ask ends, the chat turn's
// files go to its chat, whatever order the turns ended or began in.
func TestDispatch_SendFileConcurrentAskAndChatTurn(t *testing.T) {
	h := newSendFileHost(t)
	endChat := h.st.BeginTurn("agent:alice:main", "telegram", "42")
	endAsk := h.st.BeginTurn("agent:alice:main", constants.AgentMessageChannel, "")
	if out, isErr := h.call(h.tok); !isErr || !strings.Contains(out, "answering more than one conversation") {
		t.Fatalf("both turns: dispatch = %q (error %v), want the refusal", out, isErr)
	}
	endAsk()
	if out, isErr := h.call(h.tok); isErr || h.sender.channel != "telegram" || h.sender.chatID != "42" {
		t.Fatalf("chat turn alone: dispatch = %q (error %v), sent to %s/%s", out, isErr, h.sender.channel, h.sender.chatID)
	}
	endChat()
	endChat() // a second end does nothing

	endAsk = h.st.BeginTurn("agent:alice:main", constants.AgentMessageChannel, "")
	defer endAsk()
	if out, isErr := h.call(h.tok); !isErr || !strings.Contains(out, "Files can't be sent to another agent.") {
		t.Fatalf("ask alone: dispatch = %q (error %v), want the refusal", out, isErr)
	}
}

// A service token belongs to no turn: it has no chat even while a turn of
// its session runs.
func TestDispatch_SendFileServiceToken(t *testing.T) {
	h := newSendFileHost(t)
	const svc = "SST" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	h.st.RegisterService(svc, "alice", "/ws/alice/sessions")
	end := h.st.BeginTurn("agent:alice:main", "telegram", "42")
	defer end()
	if out, isErr := h.call(svc); !isErr || !strings.Contains(out, "There is no chat to send files to.") {
		t.Fatalf("dispatch = %q (error %v), want the refusal", out, isErr)
	}
	h.st.SyncServiceTokens(map[string]string{"alice": svc}, func(string) string { return "/ws/alice/sessions" })
	if out, isErr := h.call(svc); !isErr || !strings.Contains(out, "There is no chat to send files to.") {
		t.Fatalf("synced service token: dispatch = %q (error %v), want the refusal", out, isErr)
	}
}
