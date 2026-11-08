// ClawEh
// License: MIT

package agent

import (
	"context"
	"testing"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/providers"
)

// A turn with no chat takes no chat hold: there is no typing indicator,
// reaction or placeholder of its to keep.
func TestRunQueuedTurn_NoChatNoHold(t *testing.T) {
	answer := chatFunc(func(context.Context, []providers.Message) (*providers.LLMResponse, error) {
		return &providers.LLMResponse{Content: "done"}, nil
	})
	al := mustNewAgentLoop(t, newTestConfig(t), bus.NewMessageBus(), answer, nil)
	holder := newFakeChatHolder()
	al.chatHolder = holder

	ctx := context.Background()
	al.runQueuedTurn(ctx, ctx, bus.InboundMessage{Channel: "cli", SenderID: "u", Content: "hi"})

	holder.mu.Lock()
	defer holder.mu.Unlock()
	if len(holder.holds) != 0 {
		t.Fatalf("holds = %v, want none for a turn without a chat", holder.holds)
	}
}

// The channel manager is the loop's chat holder and file channels; without
// one the loop holds nothing and sends no files.
func TestSetChannelManager_WiresHoldAndFiles(t *testing.T) {
	al := mustNewAgentLoop(t, newTestConfig(t), bus.NewMessageBus(), &countingProvider{}, nil)
	cm := &channels.Manager{}
	al.SetChannelManager(cm)
	if al.chatHolder != chatHolder(cm) || al.fileChannels != fileChannels(cm) {
		t.Fatal("channel manager not wired as chat holder and file channels")
	}
	al.SetChannelManager(nil)
	if al.chatHolder != nil || al.fileChannels != nil {
		t.Fatal("chat holder or file channels kept after the manager was removed")
	}
}
