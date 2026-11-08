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
)

// countInHistory counts the stored messages of agent's main conversation
// containing text.
func countInHistory(agent *AgentInstance, text string) int {
	n := 0
	for _, m := range agent.Sessions.GetHistory("agent:" + agent.ID + ":main") {
		n += strings.Count(m.Content, text)
	}
	return n
}

// reasoningOnly is a response with reasoning and no reply.
func reasoningOnly() *providers.LLMResponse {
	return &providers.LLMResponse{Reasoning: "thinking", Normal: true, FinishReason: "stop"}
}

// An image sent on a tool-call iteration does not make an empty final
// response a complete reply: a degenerate final response gets the fallback
// and the empty outcome.
func TestGeneratedImages_EarlierImageThenDegenerateFinal(t *testing.T) {
	l := newSendFilesLoop(t)
	responses := make([]*providers.LLMResponse, 0, maxEmptyRetries+2)
	responses = append(responses, &providers.LLMResponse{
		Images:    []providers.GeneratedImage{pngImage()},
		ToolCalls: []providers.ToolCall{toolCallTo("tc-1", "no_such_tool")},
		Normal:    true,
	})
	for range maxEmptyRetries + 1 {
		responses = append(responses, reasoningOnly())
	}
	reply, outcome, _ := l.imageTurn(t, l.agent, "telegram", "42", responses...)
	<-l.delivered
	if outcome != bus.OutcomeEmpty || !strings.Contains(reply, "couldn't compose a reply") {
		t.Fatalf("reply = %q, outcome = %q; want the fallback and the empty outcome", reply, outcome)
	}
	action, text, got := decideTurnReply(turnEnd{response: reply, outcome: outcome})
	if action != replySend || text != reply || got != bus.OutcomeEmpty {
		t.Fatalf("decideTurnReply = %v, %q, %q; want the fallback sent as empty", action, text, got)
	}
}

// A final response of images alone that was cut off is not a complete
// reply: the turn reports the provider's finish reason as an error, and the
// image still reached the chat.
func TestGeneratedImages_TruncatedImagesOnlyFinal(t *testing.T) {
	l := newSendFilesLoop(t)
	reply, outcome, _ := l.imageTurn(t, l.agent, "telegram", "42",
		&providers.LLMResponse{Images: []providers.GeneratedImage{pngImage()}, FinishReason: "length"})
	<-l.delivered
	if outcome != bus.OutcomeError || !strings.Contains(reply, "finish reason: length") {
		t.Fatalf("reply = %q, outcome = %q; want the empty-response error", reply, outcome)
	}
	// The model is still told the image was sent.
	if stored := lastStored(l.agent, "assistant"); !strings.HasPrefix(stored, "[Image sent to the chat and saved as ") {
		t.Fatalf("stored reply = %q, want the image note", stored)
	}
}

// A note about an image not sent on a tool-call iteration is stored once (in
// that iteration's message), while the reply the chat gets still lists it.
func TestGeneratedImages_ToolIterationNoteStoredOnce(t *testing.T) {
	l := newSendFilesLoop(t)
	const url = "https://images.example.com/cat.png"
	reply, outcome, _ := l.imageTurn(t, l.agent, "telegram", "42",
		&providers.LLMResponse{
			Content:   "Drawing.",
			Images:    []providers.GeneratedImage{{URL: url, Size: -1}},
			ToolCalls: []providers.ToolCall{toolCallTo("tc-1", "no_such_tool")},
		},
		&providers.LLMResponse{Content: "All done.", Normal: true})
	if reply != "All done.\n\nImage: "+url || (outcome != "" && outcome != bus.OutcomeOK) {
		t.Fatalf("reply = %q, outcome %q; want the text and the image line", reply, outcome)
	}
	if n := countInHistory(l.agent, "Image: "+url); n != 1 {
		t.Fatalf("image line stored %d times, want once", n)
	}
	if stored := lastStored(l.agent, "assistant"); stored != "All done." {
		t.Fatalf("stored reply = %q, want the text alone", stored)
	}
}

// A clone answering another agent names its source's workspace, where the
// image is.
func TestGeneratedImages_CloneAnsweringAgentNamesSourceWorkspace(t *testing.T) {
	l := newSendFilesLoop(t)
	cloneID, err := l.al.GetRegistry().CreateClone("alice")
	if err != nil {
		t.Fatalf("CreateClone: %v", err)
	}
	clone, _ := l.al.GetRegistry().Get(cloneID)
	if clone.Workspace != l.agent.Workspace {
		t.Fatalf("clone workspace %q, want alice's %q", clone.Workspace, l.agent.Workspace)
	}
	reply, _, _ := l.imageTurn(t, clone, "agent_message", "ask-1",
		&providers.LLMResponse{Content: "Done.", Images: []providers.GeneratedImage{pngImage()}, Normal: true})
	if !strings.Contains(reply, "Image saved in Alice's workspace as files/generated/") {
		t.Fatalf("reply = %q, want alice's workspace named", reply)
	}
	if len(savedImages(t, l.agent.Workspace)) != 1 {
		t.Fatal("image not in alice's workspace")
	}
}

// A fresh temporary agent answering another agent keeps no image: its
// workspace goes with it, so no path is given.
func TestGeneratedImages_FreshAgentAnsweringAgentKeepsNothing(t *testing.T) {
	l := newSendFilesLoop(t)
	cfg := l.al.GetConfig()
	cfg.Providers = []config.Provider{{Name: "p", Protocol: "openai-chat", BaseURL: "http://127.0.0.1:0/v1", APIKey: "k"}}
	cfg.Models = []config.ModelConfig{{ModelName: "alpha", Model: "alpha-wire", Provider: "p", Enabled: true}}
	id, err := l.al.GetRegistry().CreateFresh(config.AgentConfig{Name: "Bob", Models: []string{"alpha"}})
	if err != nil {
		t.Fatalf("CreateFresh: %v", err)
	}
	fresh, _ := l.al.GetRegistry().Get(id)
	for _, channel := range []string{"agent_message", "cli"} {
		t.Run(channel, func(t *testing.T) {
			reply, _, _ := l.imageTurn(t, fresh, channel, "ask-1",
				&providers.LLMResponse{Content: "Done.", Images: []providers.GeneratedImage{pngImage()}, Normal: true})
			if reply != "Done.\n\nAn image was made but not kept: Bob is a temporary agent." {
				t.Fatalf("reply = %q", reply)
			}
			if saved := savedImages(t, fresh.Workspace); len(saved) != 0 {
				t.Fatalf("saved %v, want nothing", saved)
			}
		})
	}
	// Answering a chat that takes files, it sends the image.
	reply, _, _ := l.imageTurn(t, fresh, "telegram", "42",
		&providers.LLMResponse{Content: "Here.", Images: []providers.GeneratedImage{pngImage()}, Normal: true})
	if d := <-l.delivered; d.content != testPNG || reply != "Here." {
		t.Fatalf("delivered %q, reply %q; want the image sent", d.content, reply)
	}
}

// "data:" is recognised in any case, and a long URL is cut in the reply.
func TestGeneratedImages_DataSchemeAndLongURL(t *testing.T) {
	l := newSendFilesLoop(t)
	long := "https://images.example.com/" + strings.Repeat("a", 600)
	reply, _, _ := l.imageTurn(t, l.agent, "telegram", "42",
		&providers.LLMResponse{Content: "Cats.", Normal: true, Images: []providers.GeneratedImage{
			{URL: "DATA:image/png;base64,@@@", Size: -1},
			{URL: long, Size: -1},
		}})
	want := "Cats.\n\nAn image could not be read.\nImage: " + long[:maxImageURLChars] + "…"
	if reply != want {
		t.Fatalf("reply = %q\nwant %q", reply, want)
	}
	if capImageURL("short") != "short" {
		t.Fatal("a short URL must be kept whole")
	}
}

// A channel that refuses the image: the reply gives its reason and the path.
func TestGeneratedImages_DeliveryFailed(t *testing.T) {
	l := newSendFilesLoop(t)
	l.report = func(bus.OutboundMediaMessage) (error, bool) { return errors.New("chat not found"), true }
	reply, _, _ := l.imageTurn(t, l.agent, "telegram", "42",
		&providers.LLMResponse{Content: "A cat.", Images: []providers.GeneratedImage{pngImage()}, Normal: true})
	<-l.delivered
	if !strings.Contains(reply, "Image not sent (chat not found). Image saved as files/generated/") {
		t.Fatalf("reply = %q, want the reason and the path", reply)
	}
}

// A delivery the channel has not confirmed within the wait: the model is
// told it was sent, not confirmed; the reply claims nothing.
func TestGeneratedImages_DeliveryUnconfirmed(t *testing.T) {
	l := newSendFilesLoop(t)
	l.report = func(bus.OutboundMediaMessage) (error, bool) { return nil, false }
	fc := fakeClock(l.al)
	type result struct{ reply, outcome string }
	done := make(chan result, 1)
	go func() {
		reply, outcome, _ := l.imageTurn(t, l.agent, "telegram", "42",
			&providers.LLMResponse{Content: "A cat.", Images: []providers.GeneratedImage{pngImage()}, Normal: true})
		done <- result{reply, outcome}
	}()
	<-l.delivered
	advanceWhenWaiting(t, fc, 1, sendFilesTimeout)
	got := <-done
	if got.reply != "A cat." || (got.outcome != "" && got.outcome != bus.OutcomeOK) {
		t.Fatalf("reply = %q, outcome = %q", got.reply, got.outcome)
	}
	if stored := lastStored(l.agent, "assistant"); !strings.Contains(stored, "[Image sent to the chat (not confirmed yet) and saved as ") {
		t.Fatalf("stored reply = %q, want the unconfirmed note", stored)
	}
}

// Sending fails for a reason of the loop's own (no media store): reported
// with the path.
func TestGeneratedImages_SendError(t *testing.T) {
	l := newSendFilesLoop(t)
	l.al.mediaStore = nil
	reply, _, _ := l.imageTurn(t, l.agent, "telegram", "42",
		&providers.LLMResponse{Content: "A cat.", Images: []providers.GeneratedImage{pngImage()}, Normal: true})
	if !strings.Contains(reply, "Image not sent (no media store). Image saved as files/generated/") {
		t.Fatalf("reply = %q", reply)
	}
}

// An image that cannot be written is reported, and nothing is sent.
func TestGeneratedImages_NotSaved(t *testing.T) {
	l := newSendFilesLoop(t)
	blocker := filepath.Join(l.agent.Workspace, "files", "generated")
	if err := os.MkdirAll(filepath.Dir(blocker), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blocker, []byte("not a folder"), 0o600); err != nil {
		t.Fatal(err)
	}
	reply, _, _ := l.imageTurn(t, l.agent, "telegram", "42",
		&providers.LLMResponse{Content: "A cat.", Images: []providers.GeneratedImage{pngImage()}, Normal: true})
	if reply != "A cat.\n\nAn image could not be saved." {
		t.Fatalf("reply = %q", reply)
	}
	expectNoDelivery(t, l)
}

// Every turn kind: chat turns send; asks and sub-agent tasks give the path
// in the answering agent's workspace; internal turns say the chat can't
// take files.
func TestGeneratedImages_TurnKinds(t *testing.T) {
	for _, tt := range []struct {
		channel, chatID, want string
		sent                  bool
	}{
		{"telegram", "42", "", true},
		{"agent_message", "ask-1", "Image saved in Alice's workspace as files/generated/", false},
		{"subagent", "task-1", "Image saved in Alice's workspace as files/generated/", false},
		{"system", "42", "This chat can't receive files. Image saved as files/generated/", false},
		{"recovery", "42", "This chat can't receive files. Image saved as files/generated/", false},
		{"telegram", "", "This chat can't receive files. Image saved as files/generated/", false},
		{"webui", "42", "This chat can't receive files. Image saved as files/generated/", false},
	} {
		t.Run(tt.channel+"/"+tt.chatID, func(t *testing.T) {
			l := newSendFilesLoop(t)
			reply, _, _ := l.imageTurn(t, l.agent, tt.channel, tt.chatID,
				&providers.LLMResponse{Content: "Done.", Images: []providers.GeneratedImage{pngImage()}, Normal: true})
			if tt.sent {
				if d := <-l.delivered; d.msg.Channel != tt.channel || d.msg.ChatID != tt.chatID {
					t.Fatalf("delivered to %s/%s", d.msg.Channel, d.msg.ChatID)
				}
				if reply != "Done." {
					t.Fatalf("reply = %q", reply)
				}
				return
			}
			if !strings.Contains(reply, tt.want) {
				t.Fatalf("reply = %q, want %q", reply, tt.want)
			}
		})
	}
}

// mediaTool returns a media ref, as an image-making tool does.
type mediaTool struct{ refs []string }

func (mediaTool) Name() string               { return "media_tool" }
func (mediaTool) Description() string        { return "returns media" }
func (mediaTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (m mediaTool) Execute(context.Context, map[string]any) *tools.ToolResult {
	r := tools.NewToolResult("made it")
	r.Media = m.refs
	return r
}

// A tool's media goes to the turn's chat as one message, each part named
// and typed from the media store.
func TestPublishToolMedia_ToTheTurnsChat(t *testing.T) {
	l := newSendFilesLoopWithTools(t, "media_tool")
	l.report = func(bus.OutboundMediaMessage) (error, bool) { return nil, false }
	src := filepath.Join(t.TempDir(), "cat.png")
	if err := os.WriteFile(src, []byte(testPNG), 0o600); err != nil {
		t.Fatal(err)
	}
	ref, err := l.store.Store(src, media.MediaMeta{Filename: "cat.png", ContentType: "image/png", Source: "test"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	l.agent.Tools.Register(mediaTool{refs: []string{ref, "media://unknown"}})
	l.imageTurn(t, l.agent, "telegram", "42",
		&providers.LLMResponse{ToolCalls: []providers.ToolCall{toolCallTo("tc-1", "media_tool")}},
		&providers.LLMResponse{Content: "Here.", Normal: true})
	var d deliveredFile
	select {
	case d = <-l.delivered:
	case <-time.After(5 * time.Second):
		t.Fatal("tool media not published")
	}
	if d.msg.Channel != "telegram" || d.msg.ChatID != "42" || len(d.msg.Parts) != 2 {
		t.Fatalf("published %+v", d.msg)
	}
	if p := d.msg.Parts[0]; p.Ref != ref || p.Filename != "cat.png" || p.ContentType != "image/png" || p.Type != "image" {
		t.Fatalf("part 0 = %+v, want the stored image", p)
	}
	if p := d.msg.Parts[1]; p.Ref != "media://unknown" || p.Filename != "" {
		t.Fatalf("part 1 = %+v, want the bare ref", p)
	}
}
