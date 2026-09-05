package routing

import (
	"fmt"
	"strings"
)

// SessionScope controls session isolation granularity.
type SessionScope string

const (
	SessionScopeUnified     SessionScope = "unified"
	SessionScopePerUser     SessionScope = "per-user"
	SessionScopePerPlatform SessionScope = "per-platform"
	SessionScopePerAccount  SessionScope = "per-account"
)

// RoutePeer represents a chat peer with kind and ID.
type RoutePeer struct {
	Kind string // "direct", "group", "channel"
	ID   string
}

// SessionKeyParams holds all inputs for session key construction.
type SessionKeyParams struct {
	AgentID       string
	Channel       string
	AccountID     string
	Peer          *RoutePeer
	SessionScope  SessionScope
	IdentityLinks map[string][]string
}

// ParsedSessionKey is the result of parsing an agent-scoped session key.
type ParsedSessionKey struct {
	AgentID string
	Rest    string
}

// BuildAgentMainSessionKey returns "agent:<agentId>:main".
func BuildAgentMainSessionKey(agentID string) string {
	return fmt.Sprintf("agent:%s:%s", NormalizeAgentID(agentID), DefaultMainKey)
}

// BuildAgentServiceSessionKey returns "agent:<agentId>:service" — the dedicated,
// headless session a long-lived MCP service token resolves to when sessions are
// NOT unified. It is a primary session key (not a subagent key), so it does not
// count against the sub-agent spawn-depth bound. See docs/service-tokens.md.
func BuildAgentServiceSessionKey(agentID string) string {
	return fmt.Sprintf("agent:%s:service", NormalizeAgentID(agentID))
}

// IsUnified reports whether a session mode collapses every surface onto the
// agent's main session. The empty mode is unified — it is the default everywhere
// a mode is read, and an unset config must not silently isolate anything.
func IsUnified(mode SessionScope) bool {
	return mode == "" || mode == SessionScopeUnified
}

// ResolveServiceSessionKey returns the session a long-lived MCP service token
// operates on. Under unified sessions that is the agent's main conversation:
// unified means one agent with one conversation, one tool surface, and one
// memory, whatever is driving it. Under an isolating mode it is the dedicated
// headless service session.
//
// Isolation is a property of the AGENT, not of the door someone came through —
// to keep an integration separate, give it its own agent.
func ResolveServiceSessionKey(mode SessionScope, agentID string) string {
	if IsUnified(mode) {
		return BuildAgentMainSessionKey(agentID)
	}
	return BuildAgentServiceSessionKey(agentID)
}

// ResolveDeviceSessionKey returns the conversation a device-gateway turn runs in.
//
// Under unified sessions every device shares the selected agent's main
// conversation — the R1, the phone app, Slack, and Telegram are the same
// assistant with the same history and the same memory. Under an isolating mode
// each device keeps its own conversation (two devices never share a transcript).
//
// requested is the client-supplied key: operator clients pick their own agent
// (and, when isolating, their own profile), so an agent-scoped request selects
// the agent. Node clients send the "main" sentinel and fall back to fallbackAgent
// (their per-device assignment, else the gateway default).
func ResolveDeviceSessionKey(mode SessionScope, requested, fallbackAgent, deviceID string) string {
	agentID := AgentIDFromSessionKey(requested)
	if agentID == "" {
		agentID = fallbackAgent
	}
	if IsUnified(mode) {
		return BuildAgentMainSessionKey(agentID)
	}
	// Honor an operator client's own key verbatim so its chat.history reads the
	// same profile-scoped conversation it writes.
	if AgentIDFromSessionKey(requested) != "" {
		return requested
	}
	return fmt.Sprintf("agent:%s:device:%s", NormalizeAgentID(agentID), deviceID)
}

// AgentIDFromSessionKey extracts the agent id from an agent-scoped session key
// ("agent:<id>:..."). It returns "" for the bare "main" sentinel a node client
// sends and for any key that is not agent-scoped, so the caller falls back to
// its own default.
func AgentIDFromSessionKey(sessionKey string) string {
	parts := strings.Split(strings.TrimSpace(sessionKey), ":")
	if len(parts) < 2 || parts[0] != "agent" {
		return ""
	}
	id := strings.TrimSpace(parts[1])
	if id == "" || id == DefaultMainKey {
		return ""
	}
	return id
}

// BuildAgentPeerSessionKey constructs a session key based on agent, channel, peer, and DM scope.
func BuildAgentPeerSessionKey(params SessionKeyParams) string {
	agentID := NormalizeAgentID(params.AgentID)

	peer := params.Peer
	if peer == nil {
		peer = &RoutePeer{Kind: "direct"}
	}
	peerKind := strings.TrimSpace(peer.Kind)
	if peerKind == "" {
		peerKind = "direct"
	}

	if peerKind == "direct" {
		sessionScope := params.SessionScope
		if sessionScope == "" {
			sessionScope = SessionScopeUnified
		}
		peerID := strings.TrimSpace(peer.ID)

		// Resolve identity links (cross-platform collapse)
		if sessionScope != SessionScopeUnified && peerID != "" {
			if linked := resolveLinkedPeerID(params.IdentityLinks, params.Channel, peerID); linked != "" {
				peerID = linked
			}
		}
		peerID = strings.ToLower(peerID)

		switch sessionScope {
		case SessionScopeUnified:
			// Unified sessions do not split by peer; fall through to the
			// agent-wide key built below.
		case SessionScopePerAccount:
			if peerID != "" {
				channel := normalizeChannel(params.Channel)
				accountID := NormalizeAccountID(params.AccountID)
				return fmt.Sprintf("agent:%s:%s:%s:direct:%s", agentID, channel, accountID, peerID)
			}
		case SessionScopePerPlatform:
			if peerID != "" {
				channel := normalizeChannel(params.Channel)
				return fmt.Sprintf("agent:%s:%s:direct:%s", agentID, channel, peerID)
			}
		case SessionScopePerUser:
			if peerID != "" {
				return fmt.Sprintf("agent:%s:direct:%s", agentID, peerID)
			}
		}
		return BuildAgentMainSessionKey(agentID)
	}

	// Group/channel peers use main session if SessionScope is unified, otherwise per-channel sessions
	if params.SessionScope == SessionScopeUnified {
		return BuildAgentMainSessionKey(agentID)
	}
	channel := normalizeChannel(params.Channel)
	peerID := strings.ToLower(strings.TrimSpace(peer.ID))
	if peerID == "" {
		peerID = "unknown"
	}
	return fmt.Sprintf("agent:%s:%s:%s:%s", agentID, channel, peerKind, peerID)
}

// ParseAgentSessionKey extracts agentId and rest from "agent:<agentId>:<rest>".
func ParseAgentSessionKey(sessionKey string) *ParsedSessionKey {
	raw := strings.TrimSpace(sessionKey)
	if raw == "" {
		return nil
	}
	parts := strings.SplitN(raw, ":", 3)
	if len(parts) < 3 {
		return nil
	}
	if parts[0] != "agent" {
		return nil
	}
	agentID := strings.TrimSpace(parts[1])
	rest := parts[2]
	if agentID == "" || rest == "" {
		return nil
	}
	return &ParsedSessionKey{AgentID: agentID, Rest: rest}
}

// IsSubagentSessionKey returns true if the session key represents a subagent.
func IsSubagentSessionKey(sessionKey string) bool {
	raw := strings.TrimSpace(sessionKey)
	if raw == "" {
		return false
	}
	if strings.HasPrefix(strings.ToLower(raw), "subagent:") {
		return true
	}
	parsed := ParseAgentSessionKey(raw)
	if parsed == nil {
		return false
	}
	return strings.HasPrefix(strings.ToLower(parsed.Rest), "subagent:")
}

func normalizeChannel(channel string) string {
	c := strings.TrimSpace(strings.ToLower(channel))
	if c == "" {
		return "unknown"
	}
	return c
}

func resolveLinkedPeerID(identityLinks map[string][]string, channel, peerID string) string {
	if len(identityLinks) == 0 {
		return ""
	}
	peerID = strings.TrimSpace(peerID)
	if peerID == "" {
		return ""
	}

	candidates := make(map[string]bool)
	rawCandidate := strings.ToLower(peerID)
	if rawCandidate != "" {
		candidates[rawCandidate] = true
	}
	channel = strings.ToLower(strings.TrimSpace(channel))
	if channel != "" {
		scopedCandidate := fmt.Sprintf("%s:%s", channel, strings.ToLower(peerID))
		candidates[scopedCandidate] = true
	}

	// If peerID is already in canonical "platform:id" format, also add the
	// bare ID part as a candidate for backward compatibility with identity_links
	// that use raw IDs (e.g. "123" instead of "telegram:123").
	if idx := strings.Index(rawCandidate, ":"); idx > 0 && idx < len(rawCandidate)-1 {
		bareID := rawCandidate[idx+1:]
		candidates[bareID] = true
	}

	if len(candidates) == 0 {
		return ""
	}

	for canonical, ids := range identityLinks {
		canonicalName := strings.TrimSpace(canonical)
		if canonicalName == "" {
			continue
		}
		for _, id := range ids {
			normalized := strings.ToLower(strings.TrimSpace(id))
			if normalized != "" && candidates[normalized] {
				return canonicalName
			}
		}
	}
	return ""
}
