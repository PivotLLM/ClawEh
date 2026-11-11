// ClawEh
// License: MIT

package agent

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/PivotLLM/ctxengine"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/tools"
)

// compactionNoticeTimeout bounds enqueueing the notice. The engine calls the
// start hook under the session's lock, so it waits only for room on the bus,
// never for delivery.
const compactionNoticeTimeout = time.Second

// compactionNoticeText tells a person their reply waits on a compaction.
func compactionNoticeText(agentName string) string {
	return agentName + " is summarising the conversation so far; the reply may take a moment."
}

// compactionNotice is where a turn tells its person that a compaction makes
// them wait: the chat and the text, sent at most once per turn.
type compactionNotice struct {
	channel, chatID, text string
	sent                  atomic.Bool
}

type compactionNoticeKey struct{}

// withCompactionNotice returns ctx carrying the turn's compaction notice: set
// for a turn answering a person in a chat they read (tools.TurnChat), cleared
// for every other turn, so a turn nested in another (a sub-agent run from a
// tool call) never borrows the outer turn's chat. A fresh temporary agent
// gets none.
func withCompactionNotice(ctx context.Context, agent *AgentInstance, opts processOptions) context.Context {
	var notice *compactionNotice
	if opts.FromPerson && !agent.Spec.Fresh {
		rc, problem := tools.TurnChat(tools.WithReplyChat(ctx, tools.ReplyChat{Channel: opts.Channel, ChatID: opts.ChatID}))
		if problem == tools.ChatOK {
			notice = &compactionNotice{channel: rc.Channel, chatID: rc.ChatID, text: compactionNoticeText(agent.DisplayName())}
		}
	}
	return context.WithValue(ctx, compactionNoticeKey{}, notice)
}

// compactionStarted is the context engine's compaction-start hook: a pass
// the turn waits on (Blocking) is about to call the summary model. The turn's
// person is told once; a background pass (session_compact, the deferred age
// compaction) says nothing. It runs under the session's lock, so it only
// enqueues the message and never calls back into the engine.
func (al *AgentLoop) compactionStarted(ctx context.Context, info ctxengine.CompactionStart) {
	if !info.Blocking || al.bus == nil {
		return
	}
	notice, ok := ctx.Value(compactionNoticeKey{}).(*compactionNotice)
	if !ok || notice == nil || !notice.sent.CompareAndSwap(false, true) {
		return
	}
	pubCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), compactionNoticeTimeout)
	defer cancel()
	fields := map[string]any{"session_key": info.SessionKey, "reason": string(info.Reason), "channel": notice.channel}
	if err := al.bus.PublishOutbound(pubCtx, bus.OutboundMessage{
		Channel: notice.channel,
		ChatID:  notice.chatID,
		Content: notice.text,
	}); err != nil {
		fields["error"] = err.Error()
		logger.WarnCF("agent", "Failed to send the compaction notice", turnFields(ctx, fields))
		return
	}
	logger.InfoCF("agent", "Compaction notice sent", turnFields(ctx, fields))
}
