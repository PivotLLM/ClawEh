// ClawEh - Session store CLI
// License: MIT

package sessions

import (
	"errors"
	"fmt"
	"strings"

	"github.com/PivotLLM/ctxengine/memory"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/routing"
)

// CogmemNote is reported with every erasure: cognitive memories are left in
// place because cogmem records no per-sender (or per-session) provenance on a
// memory, so there is nothing to attribute to the erased sender.
const CogmemNote = "cognitive memories untouched: cogmem records no per-sender provenance"

// EraseRequest identifies a sender by the channel it used and the chat (or
// sender) id that channel reports, the peer id that ends its session keys.
type EraseRequest struct {
	Channel string
	ChatID  string
	// All also deletes the shared main session the sender's channel routes to
	// under unified scope, which holds every sender's messages: the archive
	// has no per-sender column, so a sender cannot be cut out of it.
	All bool
}

// EraseReport is exactly what Erase did and did not remove.
type EraseReport struct {
	Erased  []string `json:"erased"`            // session keys whose archive was deleted
	Skipped []string `json:"skipped,omitempty"` // "<key>: <reason>" for matches left in place
	// Shared is the unified-scope main session the sender's messages also
	// live in, when it exists and All was not set. Empty otherwise.
	Shared string `json:"shared_session,omitempty"`
	Cogmem string `json:"cogmem"`
}

// Erase deletes, across every agent, each session archive whose key belongs
// to the sender in req: direct sessions under the per-user, per-platform and
// per-account scopes (identity links resolved), group and channel sessions
// whose peer is the chat id, and device sessions keyed by the device id. The
// shared main session is deleted only with req.All. release, when non-nil,
// is called with each key before its files go so a running gateway can close
// its handles; an error from it leaves that session in place and reports it.
func Erase(cfg *config.Config, req EraseRequest, release func(key string) error) (EraseReport, error) {
	rep := EraseReport{Erased: []string{}, Cogmem: CogmemNote}
	channel := strings.ToLower(strings.TrimSpace(req.Channel))
	chatID := strings.ToLower(strings.TrimSpace(req.ChatID))
	if channel == "" || chatID == "" {
		return rep, errors.New("channel and chat id are required")
	}
	peers := senderPeers(cfg, channel, chatID)

	var errs []error
	erase := func(dir, key string) {
		if release != nil {
			if err := release(key); err != nil {
				rep.Skipped = append(rep.Skipped, key+": "+err.Error())
				return
			}
		}
		if err := memory.DeleteSession(dir, key); err != nil {
			errs = append(errs, err)
			return
		}
		rep.Erased = append(rep.Erased, key)
	}

	route := routing.NewRouteResolver(cfg).ResolveRoute(routing.RouteInput{
		Channel: channel,
		Peer:    &routing.RoutePeer{Kind: "direct", ID: chatID},
	})
	for _, dir := range cfg.AgentSessionDirs() {
		keys, err := memory.ListSessions(dir)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, key := range keys {
			switch {
			case matchesSender(key, channel, peers):
				erase(dir, key)
			case key == route.MainSessionKey && routing.IsUnified(routing.SessionScope(cfg.Session.Mode)):
				if req.All {
					erase(dir, key)
				} else {
					rep.Shared = key
				}
			}
		}
	}
	return rep, errors.Join(errs...)
}

// senderPeers returns the lowercased peer ids that can end the sender's
// session keys: the chat id itself and, when identity_links map it to a
// canonical person, that name (the per-user scope keys by it).
func senderPeers(cfg *config.Config, channel, chatID string) map[string]bool {
	peers := map[string]bool{chatID: true}
	linked := routing.BuildAgentPeerSessionKey(routing.SessionKeyParams{
		AgentID:       "x",
		Channel:       channel,
		Peer:          &routing.RoutePeer{Kind: "direct", ID: chatID},
		SessionScope:  routing.SessionScopePerUser,
		IdentityLinks: cfg.Session.IdentityLinks,
	})
	if canonical, ok := strings.CutPrefix(linked, "agent:x:direct:"); ok && canonical != "" {
		peers[strings.ToLower(canonical)] = true
	}
	return peers
}

// matchesSender reports whether key is one of the sender's own sessions. The
// key shapes routing builds, after the "agent:<id>:" prefix, are:
//
//	<channel>:direct:<peer>             per-platform
//	<channel>:<account>:direct:<peer>   per-account
//	direct:<peer>                       per-user (no channel; peer is the linked identity)
//	<channel>:group|channel:<peer>      groups and channels under any isolating scope
//	device:<device-id>                  device gateway under an isolating scope
//
// A peer may itself contain ':' (the WebUI's "webui:<uuid>"), so the peer is
// matched as a suffix rather than as the last ':'-separated segment.
func matchesSender(key, channel string, peers map[string]bool) bool {
	parsed := routing.ParseAgentSessionKey(key)
	if parsed == nil {
		return false
	}
	for peer := range peers {
		head, ok := strings.CutSuffix(parsed.Rest, ":"+peer)
		if !ok {
			continue
		}
		if head == "direct" {
			return true
		}
		if head == "device" && channel == "device" {
			return true
		}
		segs := strings.Split(head, ":")
		if len(segs) < 2 || segs[0] != channel {
			continue
		}
		switch segs[len(segs)-1] {
		case "direct", "group", "channel":
			return true
		}
	}
	return false
}

// FormatReport renders rep the way the CLI prints it: one line per session,
// then the shared-session and cogmem notes and a total.
func FormatReport(rep EraseReport, req EraseRequest) string {
	var b strings.Builder
	for _, key := range rep.Erased {
		fmt.Fprintf(&b, "erased %s\n", key)
	}
	for _, s := range rep.Skipped {
		fmt.Fprintf(&b, "skipped %s\n", s)
	}
	if rep.Shared != "" {
		fmt.Fprintf(&b, "kept %s: session mode is unified, so this sender's messages are mixed with "+
			"every other sender's there and the archive has no per-sender column; re-run with --all "+
			"to delete the whole shared session\n", rep.Shared)
	}
	fmt.Fprintf(&b, "%s\n", rep.Cogmem)
	if len(rep.Erased) == 0 && rep.Shared == "" && len(rep.Skipped) == 0 {
		fmt.Fprintf(&b, "No sessions found for %s/%s.\n", req.Channel, req.ChatID)
	} else {
		fmt.Fprintf(&b, "Erased %d session(s).\n", len(rep.Erased))
	}
	return b.String()
}
