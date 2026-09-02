// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package schedule

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/cron"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/tools"
)

// extractField walks a dot-path through a decoded JSON value and returns what it
// selects.
//
// A path segment indexes an object. When the path meets an array, the remaining
// path is applied to every element and the results are collected, so
// "messages.id" over {"messages":[{"id":1},{"id":2}]} yields [1,2]. That fan-out
// is what makes a single path express "the set of things currently there",
// which is the shape almost every change probe needs.
//
// A path that does not resolve returns nil. That is deliberate rather than an
// error: a field being absent is itself a state worth detecting, and a probe
// whose field disappears should register as a change, not as a broken job.
func extractField(v any, path string) any {
	if path == "" {
		return v
	}
	return extractSegments(v, strings.Split(path, "."))
}

func extractSegments(v any, segs []string) any {
	if len(segs) == 0 {
		return v
	}
	switch node := v.(type) {
	case map[string]any:
		next, ok := node[segs[0]]
		if !ok {
			return nil
		}
		return extractSegments(next, segs[1:])
	case []any:
		// Fan out: apply the remaining path to each element.
		out := make([]any, 0, len(node))
		for _, item := range node {
			if got := extractSegments(item, segs); got != nil {
				out = append(out, got)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	default:
		return nil
	}
}

// watchSnapshot is the selected state of a probe: the value of each watched
// field, keyed by its path. Marshaling a map sorts keys, so the encoding is
// stable across runs and the digest only moves when a value does.
type watchSnapshot map[string]any

// buildSnapshot decodes a tool result and selects the watched fields. With no
// fields configured the whole result is the snapshot.
//
// A result that is not JSON is treated as a single opaque value rather than an
// error, so a tool returning plain text can still be watched for change.
func buildSnapshot(result string, fields []string) watchSnapshot {
	var decoded any
	if err := json.Unmarshal([]byte(result), &decoded); err != nil {
		return watchSnapshot{"": result}
	}
	if len(fields) == 0 {
		return watchSnapshot{"": decoded}
	}
	snap := make(watchSnapshot, len(fields))
	for _, f := range fields {
		snap[f] = extractField(decoded, f)
	}
	return snap
}

// digest fingerprints a snapshot. Equal digests mean nothing being watched
// moved.
func (s watchSnapshot) digest() (string, error) {
	encoded, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("fingerprint watch fields: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// describe renders the snapshot for the message delivered to the agent.
//
// The watched values travel with the notification on purpose: telling an agent
// only that "something changed" guarantees its first act is to call the same
// tool again, which is the cost the probe exists to avoid.
func (s watchSnapshot) describe() string {
	if len(s) == 1 {
		if v, ok := s[""]; ok {
			return compactJSON(v)
		}
	}
	var b strings.Builder
	for _, k := range sortedKeys(s) {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%s: %s", k, compactJSON(s[k]))
	}
	return b.String()
}

func sortedKeys(s watchSnapshot) []string {
	keys := make([]string, 0, len(s))
	for k := range s {
		keys = append(keys, k)
	}
	// Small maps; insertion sort keeps this dependency-free and stable.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

func compactJSON(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

// watchFailureNotifyThreshold is how many consecutive probe failures pass before
// the agent is told. Below it, failures are logged and retried silently: a
// transient network blip should not page anyone. At it, the agent is notified
// once — a probe that has been broken for five runs is not "quiet", it is
// blind, and silence is the failure mode this counter exists to break.
const watchFailureNotifyThreshold = 5

// watchOutcome is what a probe decided, for logging and for the caller's return
// string.
type watchOutcome struct {
	Changed bool
	Message string
}

// runWatch executes a watch job's probe and reports whether the agent should be
// woken. It never returns an error for a failed probe: a probe that cannot run
// is a state to record and eventually report, not a reason to fail the job and
// have the scheduler treat it as a crash.
func (t *CronTool) runWatch(ctx context.Context, job *cron.CronJob) watchOutcome {
	w := job.Payload.Watch
	state := job.State

	result, err := t.probe(ctx, job.AgentID, w)
	if err != nil {
		failures := state.WatchFailures + 1
		// The digest is deliberately left untouched: a failed probe knows nothing
		// about the watched state, and advancing it would silently swallow the
		// change that happened while the probe was broken.
		if serr := t.cronService.UpdateWatchState(job.ID, state.WatchDigest, failures); serr != nil {
			logger.WarnCF("cron", "watch: could not persist failure count", map[string]any{"id": job.ID, "error": serr.Error()})
		}
		logger.WarnCF("cron", "watch probe failed", map[string]any{
			"id": job.ID, "tool": w.Tool, "failures": failures, "error": err.Error(),
		})
		if failures == watchFailureNotifyThreshold {
			return watchOutcome{
				Changed: true,
				Message: fmt.Sprintf(
					"Watch %q has failed %d times in a row calling %q and cannot tell whether anything changed. Last error: %v",
					job.Name, failures, w.Tool, err),
			}
		}
		return watchOutcome{}
	}

	snap := buildSnapshot(result, w.Fields)
	digest, derr := snap.digest()
	if derr != nil {
		logger.WarnCF("cron", "watch: could not fingerprint result", map[string]any{"id": job.ID, "error": derr.Error()})
		return watchOutcome{}
	}

	// First successful probe establishes the baseline without notifying, so
	// creating a watch does not immediately report the entire existing inbox.
	if state.WatchDigest == "" {
		if serr := t.cronService.UpdateWatchState(job.ID, digest, 0); serr != nil {
			logger.WarnCF("cron", "watch: could not persist baseline", map[string]any{"id": job.ID, "error": serr.Error()})
		}
		logger.InfoCF("cron", "watch baseline recorded", map[string]any{"id": job.ID, "tool": w.Tool})
		return watchOutcome{}
	}

	if digest == state.WatchDigest {
		// The whole point: no model was involved in learning this.
		if state.WatchFailures != 0 {
			_ = t.cronService.UpdateWatchState(job.ID, digest, 0)
		}
		logger.DebugCF("cron", "watch: no change", map[string]any{"id": job.ID, "tool": w.Tool})
		return watchOutcome{}
	}

	if serr := t.cronService.UpdateWatchState(job.ID, digest, 0); serr != nil {
		logger.WarnCF("cron", "watch: could not persist digest", map[string]any{"id": job.ID, "error": serr.Error()})
	}
	logger.InfoCF("cron", "watch detected a change", map[string]any{"id": job.ID, "tool": w.Tool})

	return watchOutcome{
		Changed: true,
		Message: fmt.Sprintf("%s\n\nWatched fields now read:\n%s", job.Payload.Message, snap.describe()),
	}
}

// probe calls the watched tool with no model in the loop.
//
// It resolves the owning agent's registry so the probe sees exactly the tools
// that agent is allowed to use, re-checked on every run rather than only when
// the watch was created — a tool revoked in config must stop being probed.
func (t *CronTool) probe(ctx context.Context, agentID string, w *cron.CronWatch) (string, error) {
	if t.agentTools == nil {
		return "", fmt.Errorf("watch jobs are not available: no tool registry wired")
	}
	registry := t.agentTools(agentID)
	if registry == nil {
		return "", fmt.Errorf("agent %q has no tool registry", agentID)
	}

	tool, ok := registry.GetForHost(w.Tool)
	if !ok {
		return "", fmt.Errorf("tool %q is not available to agent %q", w.Tool, agentID)
	}
	// Session-scoped tools need a conversation to act on; a probe has none, and
	// silently handing them an empty session key would read from the wrong place.
	if scoped, isScoped := tool.(tools.SessionScoped); isScoped && scoped.IsSessionScoped() {
		return "", fmt.Errorf("tool %q is session-scoped and cannot be used as a watch probe", w.Tool)
	}

	probeCtx, cancel := context.WithTimeout(ctx, watchProbeTimeout)
	defer cancel()

	res := registry.ExecuteForHost(probeCtx, w.Tool, w.Args, "", "", nil)
	if res == nil {
		return "", fmt.Errorf("tool %q returned no result", w.Tool)
	}
	if res.IsError {
		return "", fmt.Errorf("tool %q failed: %s", w.Tool, res.ForLLM)
	}
	return res.ForLLM, nil
}

// watchProbeTimeout bounds a single probe. A watch runs unattended and on a
// schedule, so a hung tool must not accumulate goroutines run after run.
const watchProbeTimeout = 60 * time.Second

// parseWatchArgs builds a watch spec from the tool arguments, or nil when no
// watch was requested. Validation happens here, at creation, so a malformed
// watch is rejected while the model can still fix it — rather than failing
// silently every run at 3am.
func parseWatchArgs(args map[string]any) (*cron.CronWatch, error) {
	toolName, _ := args["watch_tool"].(string)
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		// watch_args or watch_fields without watch_tool is a half-written watch;
		// silently creating a plain reminder would not be what was asked for.
		if _, has := args["watch_args"]; has {
			return nil, fmt.Errorf("watch_args was given without watch_tool")
		}
		if _, has := args["watch_fields"]; has {
			return nil, fmt.Errorf("watch_fields was given without watch_tool")
		}
		return nil, nil
	}

	w := &cron.CronWatch{Tool: toolName}

	if raw, has := args["watch_args"]; has && raw != nil {
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("watch_args must be an object of tool parameters")
		}
		w.Args = m
	}

	if raw, has := args["watch_fields"]; has && raw != nil {
		list, ok := raw.([]any)
		if !ok {
			return nil, fmt.Errorf("watch_fields must be a list of dot-path strings")
		}
		for _, item := range list {
			f, isStr := item.(string)
			if !isStr {
				return nil, fmt.Errorf("watch_fields entries must be strings, got %T", item)
			}
			if f = strings.TrimSpace(f); f != "" {
				w.Fields = append(w.Fields, f)
			}
		}
	}
	return w, nil
}

// describeFields renders the watched fields for the confirmation message.
func describeFields(fields []string) string {
	if len(fields) == 0 {
		return "the tool's result"
	}
	return strings.Join(fields, ", ")
}
