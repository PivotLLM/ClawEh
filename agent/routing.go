// ClawEh
// License: MIT

package agent

import (
	"fmt"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/routing"
)

func (al *AgentLoop) resolveMessageRoute(msg bus.InboundMessage) (routing.ResolvedRoute, *AgentInstance, error) {
	registry := al.GetRegistry()

	// Honor an explicit preresolved agent ID (set by trusted internal callers
	// such as the callback HTTP handler). This bypasses binding-based routing
	// so the message is delivered to the named agent regardless of which
	// bindings would otherwise match the channel/peer/account cascade.
	//
	// A message addressed to an agent that does not exist (a temporary agent
	// deleted since, or an agent removed from the config) is dropped: handing
	// it to binding routing or the default agent would deliver one agent's
	// traffic — a session_clear, say — to another.
	if preresolved := inboundMetadata(msg, metadataKeyPreresolvedAgentID); preresolved != "" {
		normalized := routing.NormalizeAgentID(preresolved)
		agent, ok := registry.Get(normalized)
		if !ok {
			logger.WarnCF("agent", "Message dropped: addressed agent does not exist",
				map[string]any{"preresolved_agent_id": preresolved, "channel": msg.Channel, "sender_id": msg.SenderID})
			return routing.ResolvedRoute{}, nil, fmt.Errorf("%w: %s", errAgentGone, preresolved)
		}
		route := routing.ResolvedRoute{
			AgentID:    normalized,
			Channel:    msg.Channel,
			AccountID:  routing.NormalizeAccountID(inboundMetadata(msg, metadataKeyAccountID)),
			SessionKey: routing.BuildAgentMainSessionKey(normalized),
			MatchedBy:  "preresolved",
		}
		return route, agent, nil
	}

	route := registry.ResolveRoute(routing.RouteInput{
		Channel:        msg.Channel,
		AccountID:      inboundMetadata(msg, metadataKeyAccountID),
		Peer:           extractPeer(msg),
		ParentPeer:     extractParentPeer(msg),
		GuildID:        inboundMetadata(msg, metadataKeyGuildID),
		TeamID:         inboundMetadata(msg, metadataKeyTeamID),
		MentionedAgent: inboundMetadata(msg, "mentioned_agent"),
	})

	// Bindings and mentions only ever name config agents.
	agent, ok := registry.GetConfigured(route.AgentID)
	if !ok {
		agent = registry.Default()
	}
	if agent == nil {
		return routing.ResolvedRoute{}, nil, fmt.Errorf("no agent available for route (agent_id=%s)", route.AgentID)
	}

	return route, agent, nil
}

// resolveScopeKey returns the session an inbound message runs in: the routed
// agent's main conversation. Any explicit key collapses to it, so no inbound
// path can open a second session for an agent (its cognitive memory is fed
// from exactly one).
func resolveScopeKey(route routing.ResolvedRoute, msgSessionKey string) string {
	return routing.ResolveAgentSessionKey(route.AgentID, msgSessionKey)
}

// extractMention checks for and strips an agent mention trigger from msg.Content,
// recording the target agent in msg.Metadata["mentioned_agent"]. Idempotent: a
// message that already carries a mention is left alone, so the route chosen
// for the dispatch mutex in processSessionMessage is the route processMessage
// dispatches on, even when the stripped content starts with another mention.
func (al *AgentLoop) extractMention(msg *bus.InboundMessage) {
	if msg == nil || msg.Metadata["mentioned_agent"] != "" {
		return
	}
	cfg := al.GetConfig()
	var triggers []string
	if cfg != nil {
		triggers = cfg.AgentMentions.Triggers
	}
	if len(triggers) == 0 {
		triggers = []string{"@", "/", "."}
	}
	registry := al.GetRegistry()
	if registry == nil {
		return
	}
	agentIDs := registry.List()
	if mentionedAgent, stripped := channels.ExtractAgentMention(msg.Content, triggers, agentIDs); mentionedAgent != "" {
		msg.Content = stripped
		if msg.Metadata == nil {
			msg.Metadata = make(map[string]string)
		}
		msg.Metadata["mentioned_agent"] = mentionedAgent
	}
}

// extractPeer extracts the routing peer from the inbound message's structured Peer field.
func extractPeer(msg bus.InboundMessage) *routing.RoutePeer {
	if msg.Peer.Kind == "" {
		return nil
	}
	peerID := msg.Peer.ID
	if peerID == "" {
		if msg.Peer.Kind == "direct" {
			peerID = msg.SenderID
		} else {
			peerID = msg.ChatID
		}
	}
	return &routing.RoutePeer{Kind: msg.Peer.Kind, ID: peerID}
}

func inboundMetadata(msg bus.InboundMessage, key string) string {
	if msg.Metadata == nil {
		return ""
	}
	return msg.Metadata[key]
}

// senderLabel returns a short human-readable identifier for a sender, used to
// prefix group/channel messages so the LLM knows who sent each turn.
// Falls back gracefully: DisplayName → "@Username" → empty (caller decides).
func senderLabel(s bus.SenderInfo) string {
	if s.DisplayName != "" && s.Username != "" {
		return s.DisplayName + " (@" + s.Username + ")"
	}
	if s.DisplayName != "" {
		return s.DisplayName
	}
	if s.Username != "" {
		return "@" + s.Username
	}
	return ""
}

// prependSenderLabel attributes a message with its sender ("[From: <label>]").
// Applied to every message, direct chats included: direct and group messages
// share the agent's one session, so a "private"
// message is just one of several senders in the shared context — the label keeps
// attribution unambiguous. Cheap, and the clarity is worth the few tokens. No-op
// when the sender yields no label.
func prependSenderLabel(content string, s bus.SenderInfo) string {
	label := senderLabel(s)
	if label == "" {
		return content
	}
	return "[From: " + label + "]\n" + content
}

// senderSource returns a human-readable source string for message archiving.
// Combines the display label with the canonical ID when both are available.
func senderSource(canonicalID string, s bus.SenderInfo) string {
	label := senderLabel(s)
	if label == "" {
		return canonicalID
	}
	if canonicalID == "" {
		return label
	}
	return label + " [" + canonicalID + "]"
}

// extractParentPeer extracts the parent peer (reply-to) from inbound message metadata.
func extractParentPeer(msg bus.InboundMessage) *routing.RoutePeer {
	parentKind := inboundMetadata(msg, metadataKeyParentPeerKind)
	parentID := inboundMetadata(msg, metadataKeyParentPeerID)
	if parentKind == "" || parentID == "" {
		return nil
	}
	return &routing.RoutePeer{Kind: parentKind, ID: parentID}
}
