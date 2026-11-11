// ClawEh
// License: MIT

package agent

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
)

// The context engine defers a compaction that only the age trigger asks for
// (the oldest message passed the trigger age): the add marks it due instead of
// making the turn wait on the summary model, and the host runs it with
// CompactIfDue once the turn's reply is out. CompactIfDue holds the session's
// manager for the whole model call, so it runs in the background, never on the
// reply path.

// afterReplyQueue holds the work a turn leaves until its reply has been
// published. runTurn puts one on the turn's context and runs it on the way
// out, after the reply (or nothing) was sent; the turn's own runAgentLoop
// claims it. A runAgentLoop nested in the turn (a sub-agent run from a tool
// call) finds it claimed and does its own follow-up work at its end.
type afterReplyQueue struct {
	claimed atomic.Bool
	mu      sync.Mutex
	fns     []func()
}

type afterReplyKey struct{}

// withAfterReply returns ctx carrying a new afterReplyQueue, and the queue.
func withAfterReply(ctx context.Context) (context.Context, *afterReplyQueue) {
	q := &afterReplyQueue{}
	return context.WithValue(ctx, afterReplyKey{}, q), q
}

// claimAfterReply returns the after-reply queue of the turn ctx belongs to,
// the first time it is asked for; nil when ctx has none or it was claimed.
func claimAfterReply(ctx context.Context) *afterReplyQueue {
	q, ok := ctx.Value(afterReplyKey{}).(*afterReplyQueue)
	if !ok || !q.claimed.CompareAndSwap(false, true) {
		return nil
	}
	return q
}

// add queues fn to run once the reply is out.
func (q *afterReplyQueue) add(fn func()) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.fns = append(q.fns, fn)
}

// run runs the queued work, in order.
func (q *afterReplyQueue) run() {
	q.mu.Lock()
	fns := q.fns
	q.fns = nil
	q.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

// compactAfterReply arranges the deferred age compaction of agent's session
// once the turn's reply is out: queued on q when the turn has one, started now
// otherwise (a caller that delivers the reply itself, such as the CLI, gets it
// as the turn returns; the pass runs in the background and does not hold it
// up). Agents that never keep a conversation for long are
// skipped: an ephemeral agent (a sub-agent, deleted once its result is
// delivered) and a single-shot one (its conversation is discarded after the
// turn).
func (al *AgentLoop) compactAfterReply(q *afterReplyQueue, agent *AgentInstance, sessionKey string) {
	if agent.Spec.Ephemeral || agent.Spec.SingleShot() {
		return
	}
	start := func() { al.startDeferredCompaction(agent, sessionKey) }
	if q != nil {
		q.add(start)
		return
	}
	start()
}

// startDeferredCompaction runs the session's deferred age compaction, if one is
// due, in the background. It runs under the loop's run context, not the turn's
// (it outlives the turn), so shutdown cancels it; it is not waited for, and the
// next turn of a session still past the trigger age marks an interrupted pass
// due again. At most one runs per session:
// a turn ending while one is still running starts none. It reports whether
// it started one.
func (al *AgentLoop) startDeferredCompaction(agent *AgentInstance, sessionKey string) bool {
	parent := al.runContext()
	if parent == nil {
		// Not served by Run (a one-off CLI process): the process's own end
		// stops it.
		parent = context.Background()
	}
	if parent.Err() != nil {
		return false
	}
	key := agent.ID + ":" + sessionKey
	if _, running := al.deferredCompactions.LoadOrStore(key, struct{}{}); running {
		return false
	}
	go func() {
		defer al.deferredCompactions.Delete(key)
		al.runDeferredCompaction(parent, agent, key, sessionKey)
	}()
	return true
}

// runDeferredCompaction calls CompactIfDue on the session's cached context
// manager. A session with no live manager has nothing deferred (the due mark
// lives in the manager), so none is built for it. The reference held for the
// call keeps the manager from being evicted under it.
func (al *AgentLoop) runDeferredCompaction(parent context.Context, agent *AgentInstance, key, sessionKey string) {
	v, _ := al.contextManagers.Load(key)
	entry, ok := v.(*cmEntry)
	if !ok || entry.stale.Load() {
		return
	}
	// Reference first, then the mark, as on getSessionContext's fast path.
	entry.refcount.Add(1)
	defer entry.refcount.Add(-1)
	if entry.stale.Load() {
		return
	}

	// The summarization chain's request timeouts bound the pass; the turn
	// budget is only a backstop against a hung call.
	ctx, cancel := al.clk().WithTimeout(providers.WithAgentID(parent, agent.ID), al.GetConfig().Agents.Defaults.GetTurnTimeout())
	defer cancel()
	ran, err := entry.cm.CompactIfDue(ctx)
	if !ran {
		return
	}
	fields := map[string]any{"agent_id": agent.ID, "agent": agent.Label(), "session_key": sessionKey}
	if err != nil {
		fields["error"] = err.Error()
		logger.InfoCF("agent", "Deferred age compaction failed; it is retried after a later turn", fields)
		return
	}
	logger.InfoCF("agent", "Deferred age compaction finished", fields)
}
