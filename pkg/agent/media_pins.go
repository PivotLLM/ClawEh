// ClawEh
// License: MIT

package agent

import (
	"regexp"
	"strings"

	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/media"
	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// mediaRefPattern matches media store refs (media://<uuid>) wherever they
// appear — in Message.Media entries or folded into message text by the
// attachment marker / vision-describe paths.
var mediaRefPattern = regexp.MustCompile(`media://[0-9a-fA-F-]+`)

// mediaRefsIn returns the media:// refs from a media list, preserving order.
// Non-ref entries (data: URIs, paths) are skipped.
func mediaRefsIn(refs []string) []string {
	var out []string
	for _, r := range refs {
		if strings.HasPrefix(r, "media://") {
			out = append(out, r)
		}
	}
	return out
}

// appendMediaRefMarker appends the attachment-ref marker to message content so
// the ref survives in stored history even when the media itself is described
// (Flow B) or stripped for a non-vision model. The refs are actionable: they
// can be handed to agent_spawn's media parameter.
func appendMediaRefMarker(content string, refs []string) string {
	base := strings.TrimSpace(content)
	if base != "" {
		base += "\n"
	}
	return base + "[attachment ref(s): " + strings.Join(refs, ", ") + "]"
}

// collectMediaRefs scans messages for media:// refs in both Media entries and
// message text (attachment markers), de-duplicated in first-seen order.
func collectMediaRefs(messages []providers.Message) []string {
	seen := make(map[string]struct{})
	var out []string
	add := func(ref string) {
		if _, ok := seen[ref]; !ok {
			seen[ref] = struct{}{}
			out = append(out, ref)
		}
	}
	for i := range messages {
		for _, ref := range mediaRefsIn(messages[i].Media) {
			add(ref)
		}
		if strings.Contains(messages[i].Content, "media://") {
			for _, ref := range mediaRefPattern.FindAllString(messages[i].Content, -1) {
				add(ref)
			}
		}
	}
	return out
}

// refPinner returns the media store's optional pin capability, nil when the
// store is absent or does not support pinning.
func (al *AgentLoop) refPinner() media.RefPinner {
	p, _ := al.mediaStore.(media.RefPinner)
	return p
}

// pinSessionMediaRefs pins refs for the session so TTL cleanup keeps the
// underlying files alive while the session's history references them.
func (al *AgentLoop) pinSessionMediaRefs(sessionKey string, refs []string) {
	p := al.refPinner()
	if p == nil {
		return
	}
	for _, ref := range refs {
		if err := p.Pin(ref, sessionKey); err != nil {
			logger.WarnCF("agent", "media pin failed", map[string]any{
				"session_key": sessionKey, "ref": ref, "error": err.Error(),
			})
		}
	}
}

// reconcileSessionPins resets the session's pin set to the refs currently
// present in its built context. Run once per turn: refs compacted or cleared
// out of the context are unpinned and age out via the normal TTL sweep.
func (al *AgentLoop) reconcileSessionPins(sessionKey string, messages []providers.Message) {
	p := al.refPinner()
	if p == nil {
		return
	}
	p.SetPins(sessionKey, collectMediaRefs(messages))
}

// releaseSessionPins drops all pins held by the session (session_clear, or a
// sub-agent session being cleaned up).
func (al *AgentLoop) releaseSessionPins(sessionKey string) {
	p := al.refPinner()
	if p == nil {
		return
	}
	p.ReleasePins(sessionKey)
}
