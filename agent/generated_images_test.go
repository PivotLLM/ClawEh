package agent

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/providers"
)

const testPNG = "\x89PNG\r\n\x1a\n-generated-image-bytes"

func pngImage() providers.GeneratedImage {
	return providers.GeneratedImage{ContentType: "image/png", Data: []byte(testPNG), Size: int64(len(testPNG))}
}

// savedPathRe matches the workspace path of a saved image.
var savedPathRe = regexp.MustCompile(`files/generated/\d{4}-\d{2}-\d{2}/image-\d{9}-[0-9a-f]{8}\.png`)

// imageTurn runs one turn of agent on channel/chatID with the provider
// answering responses in order; it returns the reply and the turn's outcome.
func (l *sendFilesLoop) imageTurn(t *testing.T, agent *AgentInstance, channel, chatID string, responses ...*providers.LLMResponse) (string, string, *sequenceProvider) {
	t.Helper()
	provider := &sequenceProvider{responses: responses, errors: make([]error, len(responses))}
	agent.Provider = provider
	var outcome string
	reply, err := l.al.runAgentLoop(context.Background(), agent, processOptions{
		SessionKey: "agent:" + agent.ID + ":main", Channel: channel, ChatID: chatID,
		UserMessage: "draw a cat", OutcomeOut: &outcome,
	})
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	return reply, outcome, provider
}

// savedImages lists the generated images in workspace.
func savedImages(t *testing.T, workspace string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(workspace, "files", "generated", "*", "*"))
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

// lastStored is the content of the last stored message of the given role.
func lastStored(agent *AgentInstance, role string) string {
	for _, m := range slices.Backward(agent.Sessions.GetHistory("agent:" + agent.ID + ":main")) {
		if m.Role == role {
			return m.Content
		}
	}
	return ""
}

func expectNoDelivery(t *testing.T, l *sendFilesLoop) {
	t.Helper()
	select {
	case d := <-l.delivered:
		t.Fatalf("delivered %+v, want nothing", d.msg)
	case <-time.After(50 * time.Millisecond):
	}
}

// In a chat turn, the image is saved in the workspace and sent to the
// turn's chat; the reply is the model's text alone, and the model's stored
// reply says where the image was saved.
func TestGeneratedImages_ChatTurnSavesAndSends(t *testing.T) {
	l := newSendFilesLoop(t)
	reply, _, _ := l.imageTurn(t, l.agent, "telegram", "42",
		&providers.LLMResponse{Content: "Here is your cat.", Images: []providers.GeneratedImage{pngImage()}})

	saved := savedImages(t, l.agent.Workspace)
	if len(saved) != 1 {
		t.Fatalf("saved %v, want one image", saved)
	}
	if data, err := os.ReadFile(saved[0]); err != nil || string(data) != testPNG {
		t.Fatalf("saved image holds %q (%v)", data, err)
	}
	d := <-l.delivered
	if d.msg.Channel != "telegram" || d.msg.ChatID != "42" || d.msg.Parts[0].Type != "image" || d.content != testPNG {
		t.Fatalf("delivered %+v (content %q), want the image to telegram/42", d.msg, d.content)
	}
	if reply != "Here is your cat." {
		t.Fatalf("reply = %q, want the model's text alone", reply)
	}
	stored := lastStored(l.agent, "assistant")
	if !strings.HasPrefix(stored, "Here is your cat.") || !strings.Contains(stored, "[Image sent to the chat and saved as ") || !savedPathRe.MatchString(stored) {
		t.Fatalf("stored reply = %q, want the note with the saved path", stored)
	}
}

// A turn answering another agent saves the image and gives its path in the
// reply; nothing goes to a chat.
func TestGeneratedImages_AgentReplySavesWithPath(t *testing.T) {
	for _, channel := range []string{"agent_message", "subagent"} {
		t.Run(channel, func(t *testing.T) {
			l := newSendFilesLoop(t)
			reply, _, _ := l.imageTurn(t, l.agent, channel, "ask-1",
				&providers.LLMResponse{Content: "Done.", Images: []providers.GeneratedImage{pngImage()}})
			expectNoDelivery(t, l)
			if len(savedImages(t, l.agent.Workspace)) != 1 {
				t.Fatal("image not saved")
			}
			if !strings.HasPrefix(reply, "Done.\n\nImage saved as files/generated/") || !savedPathRe.MatchString(reply) {
				t.Fatalf("reply = %q, want the saved path", reply)
			}
		})
	}
}

// An image larger than the media limit is saved, not sent; the reply says
// so and gives the path.
func TestGeneratedImages_TooLargeToSend(t *testing.T) {
	l := newSendFilesLoop(t)
	l.al.GetConfig().Agents.Defaults.MaxMediaSize = 4
	reply, _, _ := l.imageTurn(t, l.agent, "telegram", "42",
		&providers.LLMResponse{Content: "Big cat.", Images: []providers.GeneratedImage{pngImage()}})
	expectNoDelivery(t, l)
	if len(savedImages(t, l.agent.Workspace)) != 1 {
		t.Fatal("image not saved")
	}
	if !strings.Contains(reply, "Image too large to send. Image saved as files/generated/") {
		t.Fatalf("reply = %q, want the size refusal and the path", reply)
	}
}

// A chat whose channel can't receive files gets the path; so does claw
// agent's cli channel.
func TestGeneratedImages_ChannelWithoutFiles(t *testing.T) {
	for _, channel := range []string{"webui", "cli"} {
		t.Run(channel, func(t *testing.T) {
			l := newSendFilesLoop(t)
			reply, _, _ := l.imageTurn(t, l.agent, channel, "42",
				&providers.LLMResponse{Content: "A cat.", Images: []providers.GeneratedImage{pngImage()}})
			expectNoDelivery(t, l)
			if len(savedImages(t, l.agent.Workspace)) != 1 {
				t.Fatal("image not saved")
			}
			if !strings.Contains(reply, "This chat can't receive files. Image saved as files/generated/") {
				t.Fatalf("reply = %q, want the path", reply)
			}
		})
	}
}

// An image given as a URL is named in the reply and never fetched or saved;
// one whose data could not be decoded is reported without its raw text.
func TestGeneratedImages_URL(t *testing.T) {
	l := newSendFilesLoop(t)
	reply, _, _ := l.imageTurn(t, l.agent, "telegram", "42",
		&providers.LLMResponse{Content: "A cat.", Images: []providers.GeneratedImage{
			{URL: "https://images.example.com/cat.png", Size: -1},
			{URL: "data:;base64,@@@", Size: -1},
		}})
	expectNoDelivery(t, l)
	if saved := savedImages(t, l.agent.Workspace); len(saved) != 0 {
		t.Fatalf("saved %v, want nothing", saved)
	}
	if reply != "A cat.\n\nImage: https://images.example.com/cat.png\nAn image could not be read." {
		t.Fatalf("reply = %q", reply)
	}
}

// Images that come with tool calls are handled at once, and the model is
// told about them in the stored tool-call message.
func TestGeneratedImages_OnToolCallIteration(t *testing.T) {
	l := newSendFilesLoop(t)
	reply, _, _ := l.imageTurn(t, l.agent, "telegram", "42",
		&providers.LLMResponse{
			Content:   "Drawing, then checking.",
			Images:    []providers.GeneratedImage{pngImage()},
			ToolCalls: []providers.ToolCall{toolCallTo("tc-1", "no_such_tool")},
		},
		&providers.LLMResponse{Content: "All done."})
	d := <-l.delivered
	if d.msg.Channel != "telegram" || d.content != testPNG {
		t.Fatalf("delivered %+v", d.msg)
	}
	if reply != "All done." {
		t.Fatalf("reply = %q", reply)
	}
	var callMsg string
	for _, m := range l.agent.Sessions.GetHistory("agent:alice:main") {
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			callMsg = m.Content
		}
	}
	if !strings.HasPrefix(callMsg, "Drawing, then checking.\n\n[Image sent to the chat and saved as files/generated/") {
		t.Fatalf("stored tool-call message = %q, want the image note", callMsg)
	}
	if stored := lastStored(l.agent, "assistant"); stored != "All done." {
		t.Fatalf("stored reply = %q, want no repeated note", stored)
	}
}

// A reply of images alone, with reasoning and no text, is complete: the
// model is not poked, no fallback text is sent, and the outcome is ok.
func TestGeneratedImages_ImagesOnlyReplyIsNotEmpty(t *testing.T) {
	l := newSendFilesLoop(t)
	reply, outcome, provider := l.imageTurn(t, l.agent, "telegram", "42",
		&providers.LLMResponse{Reasoning: "drawing", Images: []providers.GeneratedImage{pngImage()}, Normal: true})
	<-l.delivered
	if provider.idx != 1 {
		t.Fatalf("model called %d times, want once (no poke)", provider.idx)
	}
	if reply != "" || outcome != bus.OutcomeOK {
		t.Fatalf("reply = %q, outcome = %q; want no text, ok", reply, outcome)
	}
	if stored := lastStored(l.agent, "assistant"); !strings.HasPrefix(stored, "[Image sent to the chat and saved as ") {
		t.Fatalf("stored reply = %q, want the note", stored)
	}
	action, text, got := decideTurnReply(turnEnd{response: reply, outcome: outcome})
	if action != replySend || text != "" || got != bus.OutcomeOK {
		t.Fatalf("decideTurnReply = %v, %q, %q; want send, nothing, ok", action, text, got)
	}
}

// A clone saves in its source's workspace, as its file tools do.
func TestGeneratedImages_CloneSavesInSourceWorkspace(t *testing.T) {
	l := newSendFilesLoop(t)
	cloneID, err := l.al.GetRegistry().CreateClone("alice")
	if err != nil {
		t.Fatalf("CreateClone: %v", err)
	}
	clone, ok := l.al.GetRegistry().Get(cloneID)
	if !ok {
		t.Fatal("clone not found")
	}
	l.imageTurn(t, clone, "telegram", "42",
		&providers.LLMResponse{Content: "A cat.", Images: []providers.GeneratedImage{pngImage()}})
	<-l.delivered
	if saved := savedImages(t, l.agent.Workspace); len(saved) != 1 {
		t.Fatalf("saved in alice's workspace: %v, want one image", saved)
	}
}

// A saved image and its folders take the agents area's modes exactly.
func TestGeneratedImages_Modes(t *testing.T) {
	l := newSendFilesLoop(t)
	l.al.GetConfig().Security.Permissions.Agents = config.AreaPermissions{DirMode: "0770", FileMode: "0660"}
	l.imageTurn(t, l.agent, "telegram", "42",
		&providers.LLMResponse{Content: "A cat.", Images: []providers.GeneratedImage{pngImage()}})
	<-l.delivered
	saved := savedImages(t, l.agent.Workspace)
	if len(saved) != 1 {
		t.Fatalf("saved %v", saved)
	}
	for path, want := range map[string]os.FileMode{
		saved[0]:                             0o660,
		filepath.Dir(saved[0]):               0o770 | os.ModeDir,
		filepath.Dir(filepath.Dir(saved[0])): 0o770 | os.ModeDir,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode() != want {
			t.Errorf("%s mode = %v, want %v", path, info.Mode(), want)
		}
	}
}

func TestImageExtension(t *testing.T) {
	for _, tt := range []struct {
		img  providers.GeneratedImage
		want string
	}{
		{providers.GeneratedImage{ContentType: "image/jpeg"}, ".jpg"},
		{providers.GeneratedImage{ContentType: "image/webp; q=1"}, ".webp"},
		{providers.GeneratedImage{Data: []byte(testPNG)}, ".png"},
		{providers.GeneratedImage{Data: []byte("??")}, ".bin"},
	} {
		if got := imageExtension(tt.img); got != tt.want {
			t.Errorf("imageExtension(%q) = %q, want %q", tt.img.ContentType, got, tt.want)
		}
	}
}
