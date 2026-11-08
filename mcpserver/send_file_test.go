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

// msg_send_file called by a CLI provider over the MCP host sends to the chat
// of the turn holding the session token, exactly as in the loop; an asked
// turn's token refuses, and a service token (no turn, no chat) refuses.
func TestDispatch_SendFileGoesToTheTurnsChat(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "notes.md"), []byte("# notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Agents.Defaults.RestrictToWorkspace = true
	sender := &recordingFileSender{}
	regs := map[string]*tools.ToolRegistry{"alice": newRegistryWith(toolsmsg.NewSendFileTool(files.NewReader(cfg, workspace), 0, sender))}
	args := func(tok string) map[string]any {
		return map[string]any{"session_token": tok, "files": []any{map[string]any{"path": "notes.md"}}}
	}

	st := newSessionTokenStore()
	tok := st.Issue("alice", "agent:alice:main", "/ws/alice/sessions")
	st.SetSource("agent:alice:main", "telegram", "42")
	out, isErr := dispatchToolCall(context.Background(), "msg_send_file", args(tok), st, resolverFor(regs), nil, nil, nil, nil)
	if isErr || !strings.Contains(out, "notes.md: sent") {
		t.Fatalf("dispatch = %q (error %v), want sent", out, isErr)
	}
	if sender.channel != "telegram" || sender.chatID != "42" || len(sender.files) != 1 {
		t.Fatalf("sent %v to %s/%s, want notes.md to telegram/42", sender.files, sender.channel, sender.chatID)
	}

	// An asked turn points the token's source at the ask.
	st.SetSource("agent:alice:main", constants.AgentMessageChannel, "")
	out, isErr = dispatchToolCall(context.Background(), "msg_send_file", args(tok), st, resolverFor(regs), nil, nil, nil, nil)
	if !isErr || !strings.Contains(out, "Files can't be sent to another agent.") {
		t.Fatalf("dispatch = %q (error %v), want the refusal", out, isErr)
	}

	const svc = "SST" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	st.RegisterService(svc, "alice", "/ws/alice/sessions")
	out, isErr = dispatchToolCall(context.Background(), "msg_send_file", args(svc), st, resolverFor(regs), nil, nil, nil, nil)
	if !isErr || !strings.Contains(out, "There is no chat to send files to.") {
		t.Fatalf("dispatch = %q (error %v), want the refusal", out, isErr)
	}
}
