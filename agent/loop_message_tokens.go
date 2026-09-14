// ClawEh - Personal AI Assistant
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package agent

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/msgtoken"
	"github.com/PivotLLM/ClawEh/routing"
)

// ValidateMessageToken resolves an external-message token to its owning agent. It
// checks BOTH token mechanisms: the long-lived named tokens (minted in the WebUI)
// and the short-lived rotating per-agent tokens. Named tokens are checked first
// since they are the primary, operator-facing path. Returns ("", false) if no
// match.
func (al *AgentLoop) ValidateMessageToken(token string) (string, bool) {
	agentID, _, _, ok := al.resolveMessageToken(token)
	return agentID, ok
}

// resolveMessageToken resolves a token to its owning agent and reports whether
// it is a named token (which is rate-limited) or a rotating one. named is only
// meaningful when ok is true. Shared by ValidateMessageToken and
// CheckMessageToken so both paths agree on which store owns a token.
func (al *AgentLoop) resolveMessageToken(token string) (agentID, tokenID string, named, ok bool) {
	al.mu.RLock()
	managers := al.messageManagers
	namedStore := al.namedTokens
	al.mu.RUnlock()
	if namedStore != nil {
		if aid, tid, found := namedStore.ValidateWithID(token); found {
			return aid, tid, true, true
		}
	}
	for aid, mgr := range managers {
		if mgr.Validate(token) {
			return aid, "", false, true
		}
	}
	return "", "", false, false
}

// MsgTokenDecision is the closed set of outcomes for an external-message token
// check: a valid token, an unknown/expired one, or a named token that is
// currently rate-limited (blocked).
type MsgTokenDecision int

const (
	MsgTokenInvalid MsgTokenDecision = iota
	MsgTokenValid
	MsgTokenRateLimited
)

// CheckMessageToken resolves a token AND applies the per-token rate limit for
// named tokens. Rotating tokens are never rate-limited (they are short-lived by
// construction). retryAfter is only meaningful for MsgTokenRateLimited.
func (al *AgentLoop) CheckMessageToken(token string) (agentID string, retryAfter time.Duration, decision MsgTokenDecision) {
	aid, tokenID, named, ok := al.resolveMessageToken(token)
	if !ok {
		return "", 0, MsgTokenInvalid
	}
	if !named {
		return aid, 0, MsgTokenValid
	}
	al.mu.RLock()
	namedStore := al.namedTokens
	al.mu.RUnlock()
	if namedStore == nil {
		return aid, 0, MsgTokenValid
	}
	allowed, ra := namedStore.Allow(aid, tokenID)
	if !allowed {
		return aid, ra, MsgTokenRateLimited
	}
	return aid, 0, MsgTokenValid
}

// ListMessageTokens returns the named message-API tokens for an agent.
func (al *AgentLoop) ListMessageTokens(agentID string) []msgtoken.NamedToken {
	al.mu.RLock()
	named := al.namedTokens
	al.mu.RUnlock()
	if named == nil {
		return nil
	}
	return named.List(agentID)
}

// CreateMessageToken mints a new named message-API token for an agent and
// persists it. The returned token includes its plaintext secret.
func (al *AgentLoop) CreateMessageToken(agentID, name string) (msgtoken.NamedToken, error) {
	al.mu.RLock()
	named := al.namedTokens
	al.mu.RUnlock()
	if named == nil {
		return msgtoken.NamedToken{}, fmt.Errorf("named message-token store unavailable")
	}
	return named.Create(agentID, name)
}

// DeleteMessageToken revokes a named message-API token by id. Returns true if a
// token was removed.
func (al *AgentLoop) DeleteMessageToken(agentID, id string) bool {
	al.mu.RLock()
	named := al.namedTokens
	al.mu.RUnlock()
	if named == nil {
		return false
	}
	return named.Delete(agentID, id)
}

// MessageTokenQuota returns the per-token rate-limit status for an agent's named
// message-API tokens.
func (al *AgentLoop) MessageTokenQuota(agentID string) []msgtoken.TokenQuota {
	al.mu.RLock()
	named := al.namedTokens
	al.mu.RUnlock()
	if named == nil {
		return nil
	}
	return named.Quota(agentID)
}

// ResetMessageTokenBlocks clears active rate-limit blocks for an agent's tokens.
// An empty name clears all; a name clears just that token. Returns the count
// cleared.
func (al *AgentLoop) ResetMessageTokenBlocks(agentID, name string) int {
	al.mu.RLock()
	named := al.namedTokens
	al.mu.RUnlock()
	if named == nil {
		return 0
	}
	return named.ResetBlocks(agentID, name)
}

// UpdateMessageToken sets a named token's rate/block config and persists it.
// Returns true when the token exists.
func (al *AgentLoop) UpdateMessageToken(agentID, id string, ratePerMin, blockMinutes int) bool {
	al.mu.RLock()
	named := al.namedTokens
	al.mu.RUnlock()
	if named == nil {
		return false
	}
	return named.Update(agentID, id, ratePerMin, blockMinutes)
}

// ErrNoDefaultChannel is returned (wrapped) by HandleExternalMessage when the
// target agent has no default channel binding to deliver an external event to.
// Callers can errors.Is against it to report a precondition failure (4xx).
var ErrNoDefaultChannel = errors.New("agent has no default channel")

// buildMessageManagers constructs per-agent message-token managers from cfg and
// wires each onto its agent's ContextBuilder. An agent whose message-token window is
// not > 0 gets no manager — no token is ever issued, injected, or validated for
// it. Call this on initial construction AND on every config reload so the
// managers (and the ContextBuilders' wiring) track the CURRENT config; otherwise
// a reloaded agent gets no token injected, and a now-disabled agent's old token
// would keep validating against a stale manager.
func buildMessageManagers(registry *AgentRegistry, cfg *config.Config) map[string]*msgtoken.Manager {
	managers := make(map[string]*msgtoken.Manager)
	for _, agentID := range registry.ListAgentIDs() {
		agentInstance, ok := registry.GetAgent(agentID)
		if !ok {
			continue
		}
		// Find the matching AgentConfig for callback settings.
		var agentCfg *config.AgentConfig
		for i := range cfg.Agents.List {
			if routing.NormalizeAgentID(cfg.Agents.List[i].ID) == agentID {
				agentCfg = &cfg.Agents.List[i]
				break
			}
		}
		storePath := filepath.Join(agentInstance.Workspace, "state", "message-tokens.json")
		windowMinutes, windowCount := 0, 0
		if agentCfg != nil && agentCfg.Message != nil && agentCfg.Message.WindowMinutes > 0 {
			windowMinutes = agentCfg.Message.WindowMinutes
			windowCount = agentCfg.Message.WindowCount
		}
		mgr, err := msgtoken.NewManager(agentID, storePath, windowMinutes, windowCount)
		if err != nil {
			logger.WarnCF("callback", "Failed to initialize callback manager",
				map[string]any{"agent": agentID, "error": err.Error()})
			continue
		}
		if mgr != nil {
			managers[agentID] = mgr
			logger.InfoCF("callback", "callbacks ENABLED for agent",
				map[string]any{"agent": agentID, "window_minutes": windowMinutes, "window_count": windowCount})
		} else {
			logger.InfoCF("callback", "callbacks disabled for agent (no token will be issued or injected)",
				map[string]any{"agent": agentID})
		}
	}
	return managers
}
