// ClawEh
// License: MIT

package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/agentreg"
	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/constants"
	"github.com/PivotLLM/ClawEh/forum"
	"github.com/PivotLLM/ClawEh/forum/forumfs"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/routing"
	"github.com/PivotLLM/ClawEh/tools"
	toolsagents "github.com/PivotLLM/ClawEh/tools/agents"
)

// ForumHost is ClawEh's side of the forum package's host contract over one
// agent loop: forum.Messenger (the core Ask), forum.Agents (the agent
// registry), forum.Notifier (the completion notice) and OnStuck (an operator
// alert). The gateway creates it before the loop, because the forum service
// must exist when the loop builds the agents' tools, and binds it once the
// loop exists.
type ForumHost struct {
	loop atomic.Pointer[AgentLoop]
}

var (
	_ forum.Messenger = (*ForumHost)(nil)
	_ forum.Agents    = (*ForumHost)(nil)
	_ forum.Notifier  = (*ForumHost)(nil)
)

// forumNoticeSender is the sender id of a forum's completion notice, as the
// launching agent sees it ("[System: forum] …").
const forumNoticeSender = "forum"

// NewForumHost returns a host bound to no loop yet.
func NewForumHost() *ForumHost { return &ForumHost{} }

// Bind attaches the agent loop the host works on.
func (h *ForumHost) Bind(al *AgentLoop) { h.loop.Store(al) }

// bound returns the loop, or ErrShuttingDown before Bind.
func (h *ForumHost) bound() (*AgentLoop, error) {
	al := h.loop.Load()
	if al == nil {
		return nil, fmt.Errorf("the agent loop is not ready: %w", forum.ErrShuttingDown)
	}
	return al, nil
}

// registry returns the loop's agent registry.
func (h *ForumHost) registry() (*AgentRegistry, error) {
	al, err := h.bound()
	if err != nil {
		return nil, err
	}
	r := al.GetRegistry()
	if r == nil {
		return nil, errors.New("agent registry is not available")
	}
	return r, nil
}

// Ceilings returns the install's forum maximums from the loop's current
// configuration (forum.limits, defaults for unset fields), so a reload
// applies to the next validation or launch. Before Bind it returns the
// defaults.
func (h *ForumHost) Ceilings() forum.Ceilings {
	var limits config.ForumLimitsConfig
	if al := h.loop.Load(); al != nil {
		if cfg := al.GetConfig(); cfg != nil {
			limits = cfg.Forum.Limits
		}
	}
	l := limits.Effective()
	return forum.Ceilings{
		MaxCalls:           l.MaxCalls,
		MaxDurationSeconds: l.MaxDurationSeconds,
		CallTimeoutSeconds: l.CallTimeoutSeconds,
		MaxParallelCalls:   l.MaxParallelCalls,
	}
}

// Scopes lists the forum scope of every configured agent: its id and
// <workspace>/forums. Startup recovery runs over these whatever the agent's
// `forum` switch says, so forums of an agent whose switch was turned off
// still resume, keep their participants alive and are cleaned up; the
// switch gates only the tools.
func (h *ForumHost) Scopes() []forum.Scope {
	al, err := h.bound()
	if err != nil {
		return nil
	}
	r, err := h.registry()
	if err != nil {
		return nil
	}
	cfg := al.GetConfig()
	if cfg == nil {
		return nil
	}
	var scopes []forum.Scope
	for _, id := range r.List() {
		a, ok := r.GetConfigured(id)
		if !ok || a.Workspace == "" {
			continue
		}
		base, err := filepath.Abs(filepath.Join(a.Workspace, forumfs.BaseDirName))
		if err != nil {
			continue
		}
		scopes = append(scopes, forum.Scope{AgentID: id, BaseDirectory: base})
	}
	return scopes
}

// Ask implements forum.Messenger over the core Ask. The participant's turn
// runs at the maximum sub-agent depth, so it cannot spawn or ask further, and
// its sender is the forum's launching agent. A host shutdown is reported as
// forum.ErrShuttingDown, never as a cancelled reply.
func (h *ForumHost) Ask(ctx context.Context, agentID, message string, wait time.Duration) (forum.Reply, error) {
	al, err := h.bound()
	if err != nil {
		return forum.Reply{}, err
	}
	cfg := al.GetConfig()
	if cfg == nil || !al.running.Load() {
		return forum.Reply{}, fmt.Errorf("ask %s: %w", agentID, forum.ErrShuttingDown)
	}
	info, ok := forum.AskInfoFromContext(ctx)
	if !ok || info.Origin.AgentID == "" {
		return forum.Reply{}, fmt.Errorf("ask %s: the forum's launcher is unknown", agentID)
	}
	if refusal := h.mayAsk(al, info.Origin.AgentID, agentID); refusal != nil {
		return forum.Reply{}, refusal
	}
	askCtx := toolsagents.WithSpawnDepth(ctx, cfg.Agents.Defaults.GetMaxSubagentDepth()-1)
	// The participant's turn ends when the forum stops waiting for it (the
	// call timeout, the run deadline, a cancel), so its model call is not
	// left running for a reply nobody reads.
	reply, err := al.askStoppingTurn(askCtx, h.sender(ctx), agentID, message, wait)
	stopping := !al.running.Load()
	switch {
	case err != nil && ctx.Err() != nil:
		return forum.Reply{}, ctx.Err()
	case err != nil && stopping:
		return forum.Reply{}, fmt.Errorf("ask %s: %w", agentID, forum.ErrShuttingDown)
	case err != nil:
		return forum.Reply{}, err
	}
	switch reply.Outcome {
	case bus.OutcomeOK:
		return forum.Reply{Text: reply.Text, Outcome: forum.OutcomeOK}, nil
	case bus.OutcomeEmpty:
		return forum.Reply{Outcome: forum.OutcomeEmpty}, nil
	case tools.OutcomeTimeout:
		return forum.Reply{Outcome: forum.OutcomeTimeout}, nil
	case bus.OutcomeCancelled, tools.OutcomePersonCancelled:
		if stopping {
			return forum.Reply{}, fmt.Errorf("ask %s: turn cancelled: %w", agentID, forum.ErrShuttingDown)
		}
		return forum.Reply{Outcome: forum.OutcomeCancelled}, nil
	default:
		return forum.Reply{Text: reply.Text, Outcome: forum.OutcomeError}, nil
	}
}

// mayAsk checks, now, that the launcher may have the forum ask agentID: an
// agent in its subagents.allow_agents, or a forum participant it owns. The
// forum's records live in the launcher's workspace and are not trusted for
// this.
func (h *ForumHost) mayAsk(al *AgentLoop, launcherID, agentID string) error {
	if newAgentServices(al, launcherID).CanTarget(agentID) {
		return nil
	}
	if r, err := h.registry(); err == nil && ownedParticipant(r, launcherID, agentID) {
		return nil
	}
	return fmt.Errorf("agent %s may not take part in a forum of %s: it is not in the launcher's subagents.allow_agents", agentID, launcherID)
}

// ownedParticipant reports whether agentID is a temporary agent a forum of
// launcherID created.
func ownedParticipant(r *AgentRegistry, launcherID, agentID string) bool {
	info, ok := r.Info(agentID)
	return ok && info.Spec.Origin == agentreg.OriginTemp && info.Spec.Purpose == tools.TempPurposeForum &&
		info.Spec.Owner != "" && info.Spec.Owner == routing.NormalizeAgentID(launcherID)
}

// sender names the forum's launching agent (from the ask's context) for the
// participant: its name, or its id when it is gone.
func (h *ForumHost) sender(ctx context.Context) string {
	info, ok := forum.AskInfoFromContext(ctx)
	if !ok || info.Origin.AgentID == "" {
		return "Forum"
	}
	if r, err := h.registry(); err == nil {
		if a, found := r.Get(info.Origin.AgentID); found && a != nil {
			return a.DisplayName()
		}
	}
	return info.Origin.AgentID
}

// Exists implements forum.Agents: any registered agent, config or temporary.
func (h *ForumHost) Exists(_ context.Context, agentID string) (bool, error) {
	r, err := h.registry()
	if err != nil {
		return false, err
	}
	_, ok := r.Get(agentID)
	return ok, nil
}

// MayTarget implements forum.Agents: the launcher's subagents.allow_agents,
// for configured agents only.
func (h *ForumHost) MayTarget(_ context.Context, launcherID, targetID string) (bool, error) {
	al, err := h.bound()
	if err != nil {
		return false, err
	}
	return newAgentServices(al, launcherID).CanTarget(targetID), nil
}

// Models implements forum.Agents: the agent's models in its fallback order,
// described from their configuration.
func (h *ForumHost) Models(_ context.Context, agentID string) ([]forum.ModelInfo, error) {
	al, err := h.bound()
	if err != nil {
		return nil, err
	}
	r, err := h.registry()
	if err != nil {
		return nil, err
	}
	a, ok := r.Get(agentID)
	if !ok || a == nil {
		return nil, fmt.Errorf("%w: %s", agentreg.ErrNotFound, agentID)
	}
	cfg := al.GetConfig()
	out := make([]forum.ModelInfo, 0, len(a.Candidates))
	for _, c := range a.Candidates {
		out = append(out, modelInfo(cfg, candidateName(c)))
	}
	return out, nil
}

// Cooldown is forum.Host.Cooldown: when every model the agent can run on
// is in cooldown, the one available first and how long until it is; ""
// and 0 when one can be used now (or the agent is unknown, so the ask
// reports that itself).
func (h *ForumHost) Cooldown(agentID string) (string, time.Duration) {
	al, err := h.bound()
	if err != nil {
		return "", 0
	}
	r, err := h.registry()
	if err != nil {
		return "", 0
	}
	tracker := al.cooldownTracker()
	a, ok := r.Get(agentID)
	if !ok || a == nil || tracker == nil || len(a.Candidates) == 0 {
		return "", 0
	}
	model, soonest := "", time.Duration(0)
	for _, c := range a.Candidates {
		left := tracker.CooldownRemaining(c.Provider, c.Model)
		if left <= 0 {
			return "", 0
		}
		if model == "" || left < soonest {
			model, soonest = candidateName(c), left
		}
	}
	return model, soonest
}

// modelInfo describes the model name from its configuration.
func modelInfo(cfg *config.Config, name string) forum.ModelInfo {
	info := forum.ModelInfo{Name: name}
	if cfg == nil {
		return info
	}
	mc, err := cfg.GetModelConfig(name)
	if err != nil || mc == nil {
		return info
	}
	info.Provider, info.NoTools = mc.Provider, mc.NoTools
	info.Vision = mc.Vision != "" && !strings.EqualFold(mc.Vision, "off")
	if p, perr := cfg.GetProvider(mc.Provider); perr == nil && p != nil {
		info.Protocol = p.Protocol
	}
	return info
}

// CreateClone implements forum.Agents: a temporary clone of the source,
// owned by the launcher and marked as a forum participant, optionally on one
// of the source's models. Its memory is a snapshot kept across restarts.
func (h *ForumHost) CreateClone(_ context.Context, spec forum.CloneSpec) (string, error) {
	al, err := h.bound()
	if err != nil {
		return "", err
	}
	if !newAgentServices(al, spec.Owner).CanTarget(spec.Source) {
		return "", fmt.Errorf("agent %q may not target agent %q (subagents.allow_agents)", spec.Owner, spec.Source)
	}
	r, err := h.registry()
	if err != nil {
		return "", err
	}
	opts := []agentreg.CloneOption{agentreg.OwnedBy(spec.Owner), agentreg.WithPurpose(tools.TempPurposeForum)}
	if spec.Model != "" {
		src, ok := r.GetConfigured(spec.Source)
		if !ok || src == nil {
			return "", fmt.Errorf("%w: %s", agentreg.ErrNotFound, spec.Source)
		}
		m, ok := toolsagents.MatchCandidate(src.Candidates, spec.Model)
		if !ok {
			return "", fmt.Errorf("model %q is not one of agent %q's models", spec.Model, spec.Source)
		}
		opts = append(opts, agentreg.CloneModel(candidateName(m)))
	}
	return r.CreateClone(spec.Source, opts...)
}

// CreateFresh implements forum.Agents: a fresh temporary agent on one of the
// launcher's models, owned by it and marked as a forum participant.
func (h *ForumHost) CreateFresh(_ context.Context, spec forum.FreshSpec) (string, error) {
	al, err := h.bound()
	if err != nil {
		return "", err
	}
	var o tools.FreshOptions
	if spec.SystemPrompt != "" {
		o.SystemPrompt, o.SystemPromptSet = spec.SystemPrompt, true
	}
	switch spec.Mode {
	case "", forum.FreshModeMemory:
	case forum.FreshModeNoMemory:
		o.NoMemory = true
	case forum.FreshModeSingleShot:
		o.SingleShot = true
	default:
		return "", fmt.Errorf("unknown fresh participant mode %q", spec.Mode)
	}
	return newAgentServices(al, spec.Owner).createFresh(spec.Model, o, agentreg.WithPurpose(tools.TempPurposeForum))
}

// Delete implements forum.Agents: only a forum participant the launcher
// owns is deleted; an agent that is already gone counts as deleted; one in
// a turn (a turn the forum stopped waiting for, which is being cancelled)
// is deleted as soon as that turn ends (forum.ErrDeletePending).
func (h *ForumHost) Delete(_ context.Context, launcherID, agentID string) error {
	r, err := h.registry()
	if err != nil {
		return err
	}
	if _, ok := r.Info(agentID); !ok {
		return nil
	}
	if !ownedParticipant(r, launcherID, agentID) {
		return fmt.Errorf("agent %s is not a forum participant of %s; not deleted", agentID, launcherID)
	}
	err = r.DeleteWhenIdle(agentID)
	if errors.Is(err, agentreg.ErrDeletePending) {
		return fmt.Errorf("%w: %w", forum.ErrDeletePending, err)
	}
	if err != nil && !errors.Is(err, agentreg.ErrNotFound) {
		return err
	}
	return nil
}

// Touch implements forum.Agents: only a forum participant the launcher
// owns is touched.
func (h *ForumHost) Touch(_ context.Context, launcherID, agentID string) error {
	r, err := h.registry()
	if err != nil {
		return err
	}
	if _, ok := r.Info(agentID); ok && !ownedParticipant(r, launcherID, agentID) {
		return fmt.Errorf("agent %s is not a forum participant of %s; not touched", agentID, launcherID)
	}
	return r.Touch(agentID)
}

// ForumFinished implements forum.Notifier: the notice is queued as a
// background result for the launching agent, in its one conversation, and
// the call returns without waiting for the agent's turn. The agent's answer
// goes to the chat the forum was launched from when this process saw the
// launch (chat, kept in the forum service's memory). The chat recorded at
// launch in the launcher's workspace is not trusted: when the launching
// chat is unknown (the run ended after a restart), or the answer finds it
// offline or not found, a forum launched from a chat (a channel that is not
// internal) has the answer posted to the launcher's own default chat (its
// default binding) when it has one; otherwise, and for a forum launched
// locally, the answer stays in the conversation.
func (h *ForumHost) ForumFinished(ctx context.Context, origin forum.Origin, chat forum.Chat, result *forum.Result) error {
	al, err := h.bound()
	if err != nil {
		return err
	}
	if result == nil {
		return errors.New("no result")
	}
	// processSystemMessage keeps a result of the ask channel in the agent's
	// main conversation and sends it to no chat.
	defChannel, defChatID := constants.AgentMessageChannel, ""
	launchChat := chat.Channel != "" && chat.ChatID != "" && !constants.IsInternalChannel(chat.Channel)
	if cfg := al.GetConfig(); cfg != nil && (launchChat || !constants.IsInternalChannel(origin.Channel)) {
		if c, id, _, ok := cfg.CronTarget(origin.AgentID); ok {
			defChannel, defChatID = c, id
		}
	}
	channel, chatID := defChannel, defChatID
	meta := map[string]string{metadataKeyPreresolvedAgentID: origin.AgentID}
	if launchChat {
		channel, chatID = chat.Channel, chat.ChatID
		if defChannel != constants.AgentMessageChannel && (defChannel != channel || defChatID != chatID) {
			meta[metadataKeyFallbackChannel], meta[metadataKeyFallbackChatID] = defChannel, defChatID
		}
	}
	meta[bus.MetaOriginChannel] = channel
	pubCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := al.bus.PublishInbound(pubCtx, bus.InboundMessage{
		Channel:    "system",
		SenderID:   forumNoticeSender,
		ChatID:     chatID,
		Content:    forumNoticeText(result),
		SessionKey: routing.BuildAgentMainSessionKey(origin.AgentID),
		Metadata:   meta,
		Internal:   true,
	}); err != nil {
		return fmt.Errorf("queue the notice for agent %s: %w", origin.AgentID, err)
	}
	return nil
}

// forumNoticeText is the completion notice: the run's status and, when it
// says more than the status, its reason, in the words forum_status uses
// ("finished: incomplete (deadline)."), and how many outputs came from turns
// resent after a restart (the count forum_status gives; forum_results notes
// which).
func forumNoticeText(result *forum.Result) string {
	status := string(result.Status)
	if result.Reason != "" && string(result.Reason) != status {
		status += " (" + string(result.Reason) + ")"
	}
	text := fmt.Sprintf("Forum %s run %d finished: %s.", forum.Ref(result.Name, result.ForumID), result.Run, status)
	switch n := result.ResentAfterRestart; {
	case n == 1:
		text += " 1 output came from a turn resent after a restart."
	case n > 1:
		text += fmt.Sprintf(" %d outputs came from turns resent after a restart.", n)
	}
	return text
}

// OnStuck is forum.Host.OnStuck: an operator alert naming the forum, its
// run and its launching agent. Alerts are queued, so it does not block.
func (h *ForumHost) OnStuck(forumID string, run int, origin forum.Origin, err error) {
	al := h.loop.Load()
	if al == nil {
		return
	}
	who := origin.AgentID
	if r, rerr := h.registry(); rerr == nil {
		if a, ok := r.Get(origin.AgentID); ok && a != nil {
			who = a.DisplayName()
		}
	}
	details := ""
	if err != nil {
		details = err.Error()
	}
	al.Alerter().Send(alerter.Alert{
		Title:       fmt.Sprintf("Forum %s run %d of %s stopped", forumID, run, who),
		Description: "the forum's run stopped on an error; it continues with forum_resume or at the next start",
		Details:     details,
		EventID:     fmt.Sprintf("forum:%s:%d", forumID, run),
	})
	logger.WarnCF("forum", "Forum run stopped on an error; operator alerted",
		map[string]any{"forum_id": forumID, "run": run, "agent_id": origin.AgentID})
}
