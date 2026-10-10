package routing

import (
	"strings"

	"github.com/PivotLLM/ClawEh/config"
)

// RouteInput contains the routing context from an inbound message.
type RouteInput struct {
	Channel        string
	AccountID      string
	Peer           *RoutePeer
	ParentPeer     *RoutePeer
	GuildID        string
	TeamID         string
	MentionedAgent string // normalized agent ID extracted from content, or ""
}

// ResolvedRoute is the result of agent routing.
type ResolvedRoute struct {
	AgentID   string
	Channel   string
	AccountID string
	// SessionKey is the agent's one conversation, agent:<id>:main.
	SessionKey string
	MatchedBy  string // "binding.peer", "binding.peer.parent", "binding.guild", "binding.team", "binding.account", "binding.channel", "default"
}

// RouteResolver determines which agent handles a message based on config bindings.
type RouteResolver struct {
	cfg *config.Config
}

// NewRouteResolver creates a new route resolver.
func NewRouteResolver(cfg *config.Config) *RouteResolver {
	return &RouteResolver{cfg: cfg}
}

// ResolveRoute determines which agent handles the message; the session is
// always that agent's main conversation.
// Implements the 7-level priority cascade:
// peer > parent_peer > guild > team > account > channel_wildcard > default
func (r *RouteResolver) ResolveRoute(input RouteInput) ResolvedRoute {
	channel := strings.ToLower(strings.TrimSpace(input.Channel))
	accountID := NormalizeAccountID(input.AccountID)
	peer := input.Peer

	bindings := r.filterBindings(channel, accountID)

	choose := func(agentID string, matchedBy string) ResolvedRoute {
		resolvedAgentID := r.pickAgentID(agentID)
		return ResolvedRoute{
			AgentID:    resolvedAgentID,
			Channel:    channel,
			AccountID:  accountID,
			SessionKey: strings.ToLower(BuildAgentMainSessionKey(resolvedAgentID)),
			MatchedBy:  matchedBy,
		}
	}

	// chooseWithMention applies agent_mentions override if applicable.
	chooseWithMention := func(binding *config.AgentBinding, matchedBy string) ResolvedRoute {
		agentID := binding.AgentID
		if input.MentionedAgent != "" && len(binding.AgentMentions) > 0 {
			for _, m := range binding.AgentMentions {
				if m == "*" {
					agentID = input.MentionedAgent
					break
				}
				if m == input.MentionedAgent {
					agentID = m
					break
				}
			}
		}
		return choose(agentID, matchedBy)
	}

	// Priority 1: Peer binding
	if peer != nil && strings.TrimSpace(peer.ID) != "" {
		if match := r.findPeerMatch(bindings, peer); match != nil {
			return chooseWithMention(match, "binding.peer")
		}
	}

	// Priority 2: Parent peer binding
	parentPeer := input.ParentPeer
	if parentPeer != nil && strings.TrimSpace(parentPeer.ID) != "" {
		if match := r.findPeerMatch(bindings, parentPeer); match != nil {
			return chooseWithMention(match, "binding.peer.parent")
		}
	}

	// Priority 3: Guild binding
	guildID := strings.TrimSpace(input.GuildID)
	if guildID != "" {
		if match := r.findGuildMatch(bindings, guildID); match != nil {
			return chooseWithMention(match, "binding.guild")
		}
	}

	// Priority 4: Team binding
	teamID := strings.TrimSpace(input.TeamID)
	if teamID != "" {
		if match := r.findTeamMatch(bindings, teamID); match != nil {
			return chooseWithMention(match, "binding.team")
		}
	}

	// Priority 5: Account binding
	if match := r.findAccountMatch(bindings); match != nil {
		return chooseWithMention(match, "binding.account")
	}

	// Priority 6: Channel wildcard binding
	if match := r.findChannelWildcardMatch(bindings); match != nil {
		return chooseWithMention(match, "binding.channel")
	}

	// Priority 7: Default agent
	return choose(r.resolveDefaultAgentID(), "default")
}

func (r *RouteResolver) filterBindings(channel, accountID string) []config.AgentBinding {
	var filtered []config.AgentBinding
	for _, b := range r.cfg.Bindings {
		matchChannel := strings.ToLower(strings.TrimSpace(b.Match.Channel))
		if matchChannel == "" || matchChannel != channel {
			continue
		}
		if !matchesAccountID(b.Match.AccountID, accountID) {
			continue
		}
		filtered = append(filtered, b)
	}
	return filtered
}

func matchesAccountID(matchAccountID, actual string) bool {
	trimmed := strings.TrimSpace(matchAccountID)
	if trimmed == "" {
		return actual == DefaultAccountID
	}
	if trimmed == "*" {
		return true
	}
	return strings.EqualFold(trimmed, actual)
}

func (r *RouteResolver) findPeerMatch(bindings []config.AgentBinding, peer *RoutePeer) *config.AgentBinding {
	for i := range bindings {
		b := &bindings[i]
		if b.Match.Peer == nil {
			continue
		}
		peerKind := strings.ToLower(strings.TrimSpace(b.Match.Peer.Kind))
		peerID := strings.TrimSpace(b.Match.Peer.ID)
		if peerKind == "" || peerID == "" {
			continue
		}
		if peerKind == strings.ToLower(peer.Kind) && peerID == peer.ID {
			return b
		}
	}
	return nil
}

func (r *RouteResolver) findGuildMatch(bindings []config.AgentBinding, guildID string) *config.AgentBinding {
	for i := range bindings {
		b := &bindings[i]
		matchGuild := strings.TrimSpace(b.Match.GuildID)
		if matchGuild != "" && matchGuild == guildID {
			return &bindings[i]
		}
	}
	return nil
}

func (r *RouteResolver) findTeamMatch(bindings []config.AgentBinding, teamID string) *config.AgentBinding {
	for i := range bindings {
		b := &bindings[i]
		matchTeam := strings.TrimSpace(b.Match.TeamID)
		if matchTeam != "" && matchTeam == teamID {
			return &bindings[i]
		}
	}
	return nil
}

func (r *RouteResolver) findAccountMatch(bindings []config.AgentBinding) *config.AgentBinding {
	for i := range bindings {
		b := &bindings[i]
		accountID := strings.TrimSpace(b.Match.AccountID)
		if accountID == "*" {
			continue
		}
		if b.Match.Peer != nil || b.Match.GuildID != "" || b.Match.TeamID != "" {
			continue
		}
		return &bindings[i]
	}
	return nil
}

func (r *RouteResolver) findChannelWildcardMatch(bindings []config.AgentBinding) *config.AgentBinding {
	for i := range bindings {
		b := &bindings[i]
		accountID := strings.TrimSpace(b.Match.AccountID)
		if accountID != "*" {
			continue
		}
		if b.Match.Peer != nil || b.Match.GuildID != "" || b.Match.TeamID != "" {
			continue
		}
		return &bindings[i]
	}
	return nil
}

func (r *RouteResolver) pickAgentID(agentID string) string {
	trimmed := strings.TrimSpace(agentID)
	if trimmed == "" {
		return NormalizeAgentID(r.resolveDefaultAgentID())
	}
	normalized := NormalizeAgentID(trimmed)
	agents := r.cfg.Agents.List
	if len(agents) == 0 {
		return normalized
	}
	for _, a := range agents {
		if a.ID == normalized {
			return normalized
		}
	}
	return NormalizeAgentID(r.resolveDefaultAgentID())
}

func (r *RouteResolver) resolveDefaultAgentID() string {
	agents := r.cfg.Agents.List
	if len(agents) == 0 {
		return DefaultAgentID
	}
	// A human agent (one representing a person) is never the default: an
	// unaddressed message must not be posted to a person.
	for _, a := range agents {
		if a.Default && !r.cfg.IsHumanAgent(a.ID) && a.ID != "" {
			return a.ID
		}
	}
	for _, a := range agents {
		if r.cfg.IsHumanAgent(a.ID) {
			continue
		}
		if a.ID != "" {
			return a.ID
		}
		break
	}
	return DefaultAgentID
}

// Reaches reports whether a message with input could be handled by agentID:
// it routes there (by a binding or as the default agent), or a binding that
// matches it (channel and account, plus its peer, guild or team when it names
// one) names agentID as its agent or among its agent_mentions ("*" names
// every agent). It is the "may this sender talk to that agent" test behind
// /ask and /whisper. input.MentionedAgent is ignored.
func (r *RouteResolver) Reaches(input RouteInput, agentID string) bool {
	target := NormalizeAgentID(agentID)
	input.MentionedAgent = ""
	if r.ResolveRoute(input).AgentID == target {
		return true
	}
	channel := strings.ToLower(strings.TrimSpace(input.Channel))
	for _, b := range r.filterBindings(channel, NormalizeAccountID(input.AccountID)) {
		if !bindingMatches(b, input) {
			continue
		}
		if r.pickAgentID(b.AgentID) == target {
			return true
		}
		for _, m := range b.AgentMentions {
			if m == "*" || m == target {
				return true
			}
		}
	}
	return false
}

// bindingMatches reports whether binding b, already matched on channel and
// account, also matches input's peer (or parent peer), guild or team — the
// criterion ResolveRoute would match it on. A binding with none of them
// matches every message on its channel and account.
func bindingMatches(b config.AgentBinding, input RouteInput) bool {
	switch {
	case b.Match.Peer != nil:
		kind := strings.ToLower(strings.TrimSpace(b.Match.Peer.Kind))
		id := strings.TrimSpace(b.Match.Peer.ID)
		if kind == "" || id == "" {
			return false
		}
		for _, p := range []*RoutePeer{input.Peer, input.ParentPeer} {
			if p != nil && strings.ToLower(p.Kind) == kind && p.ID == id {
				return true
			}
		}
		return false
	case strings.TrimSpace(b.Match.GuildID) != "":
		return strings.TrimSpace(b.Match.GuildID) == strings.TrimSpace(input.GuildID)
	case strings.TrimSpace(b.Match.TeamID) != "":
		return strings.TrimSpace(b.Match.TeamID) == strings.TrimSpace(input.TeamID)
	default:
		return true
	}
}
