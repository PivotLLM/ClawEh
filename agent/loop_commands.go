// ClawEh - Personal AI Assistant
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/commands"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/memory"
	"github.com/PivotLLM/ClawEh/routing"
)

func (al *AgentLoop) handleCommand(
	ctx context.Context,
	msg bus.InboundMessage,
	agent *AgentInstance,
	opts *processOptions,
) (string, bool) {
	if !commands.HasCommandPrefix(msg.Content) {
		return "", false
	}

	if al.cmdRegistry == nil {
		cmdName, _ := commands.ParseCommandName(msg.Content)
		if cmdName != "" {
			return fmt.Sprintf("Unknown command: /%s", cmdName), true
		}
		return "Unknown command.", true
	}

	rt := al.buildCommandsRuntime(agent, opts, msg)
	executor := commands.NewExecutor(al.cmdRegistry, rt)

	var commandReply string
	result := executor.Execute(ctx, commands.Request{
		Channel:  msg.Channel,
		ChatID:   msg.ChatID,
		SenderID: msg.SenderID,
		Text:     msg.Content,
		Reply: func(text string) error {
			commandReply = text
			return nil
		},
	})

	switch result.Outcome {
	case commands.OutcomeHandled:
		if result.Err != nil {
			return mapCommandError(result), true
		}
		if commandReply != "" {
			return commandReply, true
		}
		return "", true
	default:
		if result.Command != "" {
			return fmt.Sprintf("Unknown command: /%s. Type /help for available commands.", result.Command), true
		}
		return "Unknown command. Type /help for available commands.", true
	}
}

func (al *AgentLoop) buildCommandsRuntime(agent *AgentInstance, opts *processOptions, msg bus.InboundMessage) *commands.Runtime {
	registry := al.GetRegistry()
	cfg := al.GetConfig()
	rt := &commands.Runtime{
		Config:          cfg,
		ListAgentIDs:    registry.ListAgentIDs,
		ListDefinitions: al.cmdRegistry.Definitions,
		GetEnabledChannels: func() []string {
			if al.channelManager == nil {
				return nil
			}
			return al.channelManager.GetEnabledChannels()
		},
		GetSessionChannels: func() []string {
			if cfg == nil || agent == nil {
				return nil
			}
			return sessionChannelsForAgent(cfg.Bindings, agent.ID)
		},
		GetArchiveStats: func() (int, time.Time, time.Time) {
			if agent == nil || opts == nil {
				return 0, time.Time{}, time.Time{}
			}
			path := archiveDBPath(agent.Workspace, opts.SessionKey)
			store, err := memory.OpenReadOnly(path)
			if err != nil {
				return 0, time.Time{}, time.Time{}
			}
			defer store.Close()
			count, first, last, _ := store.Stats()
			return count, first, last
		},
		GetMemoryStatus: func() string {
			if agent == nil || opts == nil {
				return ""
			}
			return al.cogmemSessionStatus(agent, opts.SessionKey)
		},
		SwitchChannel: func(value string) error {
			if al.channelManager == nil {
				return fmt.Errorf("channel manager not initialized")
			}
			if _, exists := al.channelManager.GetChannel(value); !exists && value != "cli" {
				return fmt.Errorf("channel '%s' not found or not enabled", value)
			}
			return nil
		},
		Uptime: func() time.Duration { return time.Since(al.startedAt) },
		GetSessionStats: func() (int, int, int) {
			if agent == nil || agent.Sessions == nil || opts == nil {
				return 0, 0, 0
			}
			history := agent.Sessions.GetHistory(opts.SessionKey)
			summary := agent.Sessions.GetSummary(opts.SessionKey)
			return len(history), al.estimateTokens(history), len(summary)
		},
	}
	if agent != nil {
		if agent.Name != "" {
			rt.AgentName = agent.Name
		} else {
			rt.AgentName = agent.ID
		}
		rt.GetContextWindow = func() int { return agent.ContextWindow }
		rt.GetModelInfo = func() (name, provider, protocol, apiBase string) {
			// Resolve the model that is actually active for THIS session (the
			// /model selection), not just the agent's first candidate, so /status
			// and /show model reflect the current choice.
			active := agent.Model
			if len(agent.Candidates) > 0 {
				idx := 0
				if opts != nil {
					idx = al.getActiveModelIndex(agent, opts.SessionKey)
				}
				if idx >= 0 && idx < len(agent.Candidates) {
					if a := agent.Candidates[idx].Alias; a != "" {
						active = a
					} else if m := agent.Candidates[idx].Model; m != "" {
						active = m
					}
				}
			}
			// Resolve the configured model so the provider name, wire protocol,
			// and base URL come from the model's named provider.
			name = active
			if mc, err := cfg.GetModelConfig(active); err == nil && mc != nil {
				if mc.ModelName != "" {
					name = mc.ModelName
				} else if mc.Model != "" {
					name = mc.Model
				}
				if prov, perr := cfg.GetProvider(mc.Provider); perr == nil && prov != nil {
					provider = prov.Name
					protocol = prov.Protocol
					apiBase = prov.BaseURL
				}
			}
			return name, provider, protocol, apiBase
		}
		rt.GetAgentModels = func() ([]commands.ModelEntry, int) {
			entries := make([]commands.ModelEntry, 0, len(agent.Candidates))
			for _, c := range agent.Candidates {
				name := c.Alias
				if name == "" {
					name = c.Model
				}
				entries = append(entries, commands.ModelEntry{Name: name, Provider: c.Provider})
			}
			active := 0
			if opts != nil {
				active = al.getActiveModelIndex(agent, opts.SessionKey)
			}
			return entries, active
		}
		rt.SetActiveModel = func(idx int) (string, error) {
			if opts == nil {
				return "", fmt.Errorf("process options not available")
			}
			if err := al.setActiveModelIndex(agent, opts.SessionKey, idx); err != nil {
				return "", err
			}
			name := agent.Candidates[idx].Alias
			if name == "" {
				name = agent.Candidates[idx].Model
			}
			return name, nil
		}

		rt.GetExposeReasoning = func() bool {
			if opts == nil {
				return false
			}
			return al.getExposeReasoning(agent, opts.SessionKey)
		}
		rt.SetExposeReasoning = func(on bool) {
			if opts == nil {
				return
			}
			al.setExposeReasoning(agent, opts.SessionKey, on)
		}

		rt.GetShowToolActivity = func() bool {
			if opts == nil {
				return false
			}
			return al.getShowToolActivity(agent, opts.SessionKey)
		}
		rt.SetShowToolActivity = func(on bool) {
			if opts == nil {
				return
			}
			al.setShowToolActivity(agent, opts.SessionKey, on)
		}

		rt.ClearHistory = func() error {
			if opts == nil {
				return fmt.Errorf("process options not available")
			}
			if agent.Sessions == nil {
				return fmt.Errorf("sessions not initialized for agent")
			}
			cm, releaseCM := al.getContextManager(agent, opts.SessionKey)
			defer releaseCM()
			if err := cm.Reset(context.Background()); err != nil {
				logger.WarnCF("agent", "clear: Reset failed", map[string]any{
					"session_key": opts.SessionKey,
					"error":       err.Error(),
				})
				return err
			}
			// Revoke the old session token and issue a fresh one. The LLM will
			// receive the new token in the next Build() call.
			al.mu.RLock()
			sti := al.sessionTokenIssuer
			al.mu.RUnlock()
			if sti != nil {
				archiveDir := filepath.Join(agent.Workspace, "sessions")
				tok := sti.Issue(agent.ID, opts.SessionKey, archiveDir)
				if tok != "" {
					cm.SetSessionToken(tok)
				}
			}
			// Notify the agent that its context was cleared, so it can re-orient
			// (the same notice an agent-initiated clear delivers, minus a handoff).
			// The reset already happened above, so no reset metadata is set.
			notice := bus.InboundMessage{
				Channel:    msg.Channel,
				ChatID:     msg.ChatID,
				SenderID:   "system",
				SessionKey: opts.SessionKey,
				Content:    wrapClearNotice(""),
				Peer:       msg.Peer,
			}
			go func() {
				pubCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := al.bus.PublishInbound(pubCtx, notice); err != nil {
					logger.WarnCF("agent", "clear: failed to publish clear notice", map[string]any{
						"session_key": opts.SessionKey, "error": err.Error(),
					})
				}
			}()
			return nil
		}
		rt.CompactHistory = func(ctx context.Context) (string, error) {
			if opts == nil {
				return "", fmt.Errorf("process options not available")
			}
			cm, releaseCM := al.getContextManager(agent, opts.SessionKey)
			defer releaseCM()
			cm.SetCallContext(opts.Channel, opts.ChatID)
			err := cm.Compact(ctx)
			report := ""
			if r := cm.LastCompactionReport(); r != nil {
				report = r.String()
			}
			return report, err
		}
		rt.ResetCooldown = func() {
			if al.fallback != nil {
				al.fallback.Reset()
			}
		}
		rt.ClearCooldown = func(provider, model string) bool {
			if al.fallback == nil {
				return false
			}
			return al.fallback.Clear(provider, model)
		}
		rt.ListCooldowns = func() []commands.CooldownEntry {
			if al.fallback == nil {
				return nil
			}
			snap := al.fallback.CooldownSnapshot()
			out := make([]commands.CooldownEntry, 0, len(snap))
			for _, s := range snap {
				out = append(out, commands.CooldownEntry{
					Provider: s.Provider,
					Model:    s.Model,
					Reason:   string(s.Reason),
					Since:    s.Since,
					Until:    s.Until,
				})
			}
			return out
		}
		// Token-quota hooks scope to THIS agent by closing over its normalized id
		// (the same id the named-token store and message API key on).
		quotaAgentID := routing.NormalizeAgentID(agent.ID)
		rt.ListTokenQuota = func() []commands.TokenQuotaEntry {
			snap := al.MessageTokenQuota(quotaAgentID)
			out := make([]commands.TokenQuotaEntry, 0, len(snap))
			for _, q := range snap {
				out = append(out, commands.TokenQuotaEntry{
					Name:           q.Name,
					RatePerMin:     q.RatePerMin,
					BlockMinutes:   q.BlockMinutes,
					HitsInWindow:   q.HitsInWindow,
					Blocked:        q.Blocked,
					BlockRemaining: q.BlockRemaining,
				})
			}
			return out
		}
		rt.ResetTokenQuota = func(name string) int {
			return al.ResetMessageTokenBlocks(quotaAgentID, name)
		}
		if opts != nil {
			sessionKey := opts.SessionKey
			rt.CancelPending = func() int {
				cs := al.getOrCreateCancelState(sessionKey)
				cs.pending.Store(false)
				return int(cs.skipCount.Swap(0))
			}
		}
		rt.RetriggerLastMessage = func(ctx context.Context) error {
			if agent == nil || agent.Sessions == nil || opts == nil {
				return fmt.Errorf("session not available")
			}
			history := agent.Sessions.GetHistory(opts.SessionKey)
			lastUserMsg := ""
			for i := len(history) - 1; i >= 0; i-- {
				if history[i].Role == "user" && history[i].Content != "" {
					lastUserMsg = history[i].Content
					break
				}
			}
			if lastUserMsg == "" {
				return fmt.Errorf("no previous message to retry")
			}
			retrigger := bus.InboundMessage{
				Channel:  msg.Channel,
				ChatID:   msg.ChatID,
				SenderID: msg.SenderID,
				Content:  lastUserMsg,
				Peer:     msg.Peer,
				IsRetry:  true,
			}
			go func() {
				pubCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := al.bus.PublishInbound(pubCtx, retrigger); err != nil {
					logger.WarnCF("agent", "Failed to retrigger message after /retry",
						map[string]any{"error": err.Error()})
				}
			}()
			return nil
		}
	}
	return rt
}

// sessionChannelsForAgent returns the distinct channel names that route to
// agentID via the supplied bindings. The result is the per-agent channel
// surface area — used by /status to avoid leaking the full daemon-wide
// channel list to callers on unrelated channels. Channel names are returned
// in the order they first appear in bindings; comparisons are case-insensitive
// for dedup but original casing is preserved in the output.
func sessionChannelsForAgent(bindings []config.AgentBinding, agentID string) []string {
	target := routing.NormalizeAgentID(agentID)
	seen := make(map[string]struct{})
	var out []string
	for _, b := range bindings {
		if routing.NormalizeAgentID(b.AgentID) != target {
			continue
		}
		ch := strings.TrimSpace(b.Match.Channel)
		if ch == "" {
			continue
		}
		key := strings.ToLower(ch)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, ch)
	}
	return out
}

// archiveDBPath returns the on-disk path of the SQLite archive for a session.
func archiveDBPath(workspace, sessionKey string) string {
	return memory.ArchivePath(filepath.Join(workspace, "sessions"), sessionKey)
}

func mapCommandError(result commands.ExecuteResult) string {
	if result.Command == "" {
		return fmt.Sprintf("Failed to execute command: %v", result.Err)
	}
	return fmt.Sprintf("Failed to execute /%s: %v", result.Command, result.Err)
}

// metaSessionReset, when set to "true" on an inbound message's Metadata, tells
// processMessage to clear the session (preserving the archive) before handling
// the message. Set by session_clear so the reset happens at a clean turn
// boundary instead of mid-turn.
const metaSessionReset = "session_reset"

// selfClearCooldown rate-limits agent-initiated session_clear per session to
// prevent a clear→continue→clear runaway loop.
const selfClearCooldown = 10 * time.Second

// wrapClearNotice builds the system notice delivered to the agent on the fresh
// turn after a clear, optionally appending a self-authored handoff note.
func wrapClearNotice(handoff string) string {
	const base = "[System notice] Your active conversation was just cleared. Your long-term memory is " +
		"preserved — retrieve past messages with session_messages / session_search and past summaries " +
		"with session_summary_get."
	if h := strings.TrimSpace(handoff); h != "" {
		return base + "\n\nHandoff note you left yourself before clearing:\n" + h
	}
	return base
}

// allowSelfClear reports whether an agent-initiated session_clear may proceed for
// the session now, enforcing selfClearCooldown to bound runaway loops.
func (al *AgentLoop) allowSelfClear(sessionKey string) bool {
	now := time.Now()
	if v, ok := al.lastSelfClear.Load(sessionKey); ok {
		if last, ok := v.(time.Time); ok && now.Sub(last) < selfClearCooldown {
			return false
		}
	}
	al.lastSelfClear.Store(sessionKey, now)
	return true
}
