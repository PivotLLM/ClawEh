// ClawEh
// License: MIT

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/classifier"
	"github.com/PivotLLM/ClawEh/classifier/classifierfs"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/constants"
	"github.com/PivotLLM/ClawEh/fileutil"
	"github.com/PivotLLM/ClawEh/internal/audit"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/routing"
	"github.com/PivotLLM/ClawEh/tools"
	toolschedule "github.com/PivotLLM/ClawEh/tools/schedule"
	"github.com/PivotLLM/ClawEh/tools/untrusted"
)

// ClassifierHost is ClawEh's side of the classifier package's host contract
// over one agent loop: classifier.ToolRunner (the owning agent's own tool
// registry), classifier.ModelCaller (the agent's classifier model),
// classifier.Notifier (the run summary posted to the agent's default chat,
// the notice of a failed run in its conversation),
// classifier.Auditor (the audit log), and the modes, maximums and scopes the
// service reads. The gateway creates it before the loop, because the
// classifier service must exist when the loop builds the agents' tools, and
// binds it once the loop exists.
type ClassifierHost struct {
	loop atomic.Pointer[AgentLoop]
}

var (
	_ classifier.ToolRunner  = (*ClassifierHost)(nil)
	_ classifier.ModelCaller = (*ClassifierHost)(nil)
	_ classifier.Notifier    = (*ClassifierHost)(nil)
	_ classifier.Auditor     = (*ClassifierHost)(nil)
)

// llmKindClassifier names a classifier's model call on the "LLM finish" line.
const llmKindClassifier = "classifier"

// classifierTempPrefix starts the name of the empty folder a CLI classifier
// model runs in, under the temporary agents' root (internal/temp), followed
// by the classifier's id. The folder is removed after the call; one a crash
// leaves behind is removed with the other orphaned folders there at the next
// start.
const classifierTempPrefix = "classifier-"

// errLoopNotReady is every operation's refusal before Bind.
var errLoopNotReady = errors.New("the agent loop is not ready")

// NewClassifierHost returns a host bound to no loop yet.
func NewClassifierHost() *ClassifierHost { return &ClassifierHost{} }

// Bind attaches the agent loop the host works on.
func (h *ClassifierHost) Bind(al *AgentLoop) { h.loop.Store(al) }

// bound returns the loop and its current configuration.
func (h *ClassifierHost) bound() (*AgentLoop, *config.Config, error) {
	al := h.loop.Load()
	if al == nil {
		return nil, nil, errLoopNotReady
	}
	cfg := al.GetConfig()
	if cfg == nil {
		return nil, nil, errLoopNotReady
	}
	return al, cfg, nil
}

// Modes is classifier.Host.Modes: the modes of what a classifier folder
// holds, its area's in the loop's current configuration (the agents area for
// an agent's workspace); 0700/0600 before Bind.
func (h *ClassifierHost) Modes(base string) (dir, file os.FileMode) {
	if _, cfg, err := h.bound(); err == nil {
		m := cfg.AgentModes(base)
		return m.Dir, m.File
	}
	return config.DefaultDirMode, config.DefaultFileMode
}

// Ceilings is classifier.Host.Ceilings: the install's maximums and the
// processed-id retention from the loop's current configuration
// (classifiers.limits, classifiers.processed_days; defaults for unset
// fields), so a reload applies to the next validation and run. Before Bind
// it returns the defaults.
func (h *ClassifierHost) Ceilings() classifier.Ceilings {
	var section config.ClassifiersConfig
	if _, cfg, err := h.bound(); err == nil {
		section = cfg.Classifiers
	}
	c := section.Effective()
	return classifier.Ceilings{
		MaxRules:         c.Limits.MaxRules,
		MaxPromptChars:   c.Limits.MaxPromptChars,
		MaxItemChars:     c.Limits.MaxItemChars,
		MaxItemsPerRun:   c.Limits.MaxItemsPerRun,
		MaxParallel:      c.Limits.MaxParallel,
		MaxActivityHours: c.Limits.MaxActivityHours,
		MinEverySeconds:  c.Limits.MinEverySeconds,
		ProcessedDays:    c.ProcessedDays,
	}
}

// Scopes is classifier.Host.Scopes: every configured agent whose
// `classifiers` switch is on, with <workspace>/classifiers. Temporary agents
// are never scopes: a clone's classifier tools act on its source's.
func (h *ClassifierHost) Scopes() []classifier.Scope {
	al, cfg, err := h.bound()
	if err != nil {
		return nil
	}
	r := al.GetRegistry()
	if r == nil {
		return nil
	}
	var scopes []classifier.Scope
	for _, id := range r.List() {
		if !cfg.AgentSuiteEnabled(id, "classifiers") {
			continue
		}
		a, ok := r.GetConfigured(id)
		if !ok || a == nil || a.Workspace == "" {
			continue
		}
		base, err := filepath.Abs(filepath.Join(a.Workspace, classifierfs.BaseDirName))
		if err != nil {
			continue
		}
		scopes = append(scopes, classifier.Scope{AgentID: id, BaseDirectory: base})
	}
	return scopes
}

// registryFor returns the tool registry of the configured agent agentID,
// looked up now, so a change to its grants applies at the next call.
func (h *ClassifierHost) registryFor(agentID string) (*tools.ToolRegistry, error) {
	al, _, err := h.bound()
	if err != nil {
		return nil, err
	}
	r := al.GetRegistry()
	if r == nil {
		return nil, errLoopNotReady
	}
	a, ok := r.GetConfigured(agentID)
	if !ok || a == nil || a.Tools == nil {
		return nil, fmt.Errorf("agent %s is not running", agentID)
	}
	return a.Tools, nil
}

// Has implements classifier.ToolRunner: the agent has tool registered and
// it may run with no session behind it. A classifier tool is refused here
// too, whatever the definition's validation said.
func (h *ClassifierHost) Has(agentID, tool string) error {
	if classifier.IsClassifierTool(tool) {
		return errors.New(classifier.ClassifierToolRefusal(tool, ""))
	}
	reg, err := h.registryFor(agentID)
	if err != nil {
		return err
	}
	t, ok := reg.Get(tool)
	if !ok {
		return fmt.Errorf("tool %s is not available to agent %s", tool, agentID)
	}
	if scoped, isScoped := t.(tools.SessionScoped); isScoped && scoped.IsSessionScoped() {
		return fmt.Errorf("tool %s works only in a conversation and cannot be used by a classifier", tool)
	}
	return nil
}

// Call implements classifier.ToolRunner: tool runs as agentID through the
// agent's own registry, unattended (no session, no chat) and bounded by the
// same timeout, exactly as a scheduled watch probe does. A tool the agent
// lacks, a session-scoped one and a classifier tool are refused with
// classifier.ToolUnavailable; the tool's own error and a timeout carry the
// reason only (the classifier names the tool with its role). A result marked
// as untrusted external content (an MCP server's, a web tool's) is returned
// unwrapped, so the classifier reads its JSON; a model never sees it here.
func (h *ClassifierHost) Call(ctx context.Context, agentID, tool string, args map[string]any) (string, error) {
	if classifier.IsClassifierTool(tool) {
		return "", classifier.ToolUnavailable(classifier.ClassifierToolRefusal(tool, ""))
	}
	al, _, err := h.bound()
	if err != nil {
		return "", err
	}
	reg, err := h.registryFor(agentID)
	if err != nil {
		return "", err
	}
	callCtx, cancel := al.clk().WithTimeout(ctx, toolschedule.WatchProbeTimeout)
	defer cancel()
	out, err := reg.ExecuteUnattended(callCtx, tool, args)
	if ctx.Err() == nil && errors.Is(callCtx.Err(), context.DeadlineExceeded) {
		return "", fmt.Errorf("no answer within %s", toolschedule.WatchProbeTimeout)
	}
	switch {
	case errors.Is(err, tools.ErrToolNotRegistered):
		return "", classifier.ToolUnavailable(fmt.Sprintf("tool %s is not available to agent %s", tool, agentID))
	case errors.Is(err, tools.ErrToolSessionScoped):
		return "", classifier.ToolUnavailable(fmt.Sprintf("tool %s works only in a conversation and cannot be used by a classifier", tool))
	case err != nil:
		// ExecuteUnattended names the tool; the classifier names it with
		// its role, so only the tool's message is kept.
		if msg, ok := strings.CutPrefix(err.Error(), fmt.Sprintf("tool %q failed: ", tool)); ok {
			return "", errors.New(msg)
		}
		return "", err
	}
	if content, ok := untrusted.Unwrap(out); ok {
		return content, nil
	}
	return out, nil
}

// Complete implements classifier.ModelCaller: one request, with no tools, on
// the agent's classifier model (agents.list[].classifiers.model), which must
// be one of the agent's models. There is no fallback: a model in cooldown is
// refused with a *classifier.CooldownError, and a failure is recorded against
// the shared cooldown policy like any other call. A CLI model runs isolated:
// in an empty folder of its own, removed afterwards, and never with the
// provider's bypass flags. Its cost counts toward the daily spend alert like
// any other model call.
func (h *ClassifierHost) Complete(ctx context.Context, agentID, system, user string) (string, string, error) {
	al, cfg, err := h.bound()
	if err != nil {
		return "", "", err
	}
	a := cfg.AgentByID(agentID)
	if a == nil {
		return "", "", fmt.Errorf("agent %s is not configured: %w", agentID, classifier.ErrNoModel)
	}
	alias := a.ClassifierModel()
	if alias == "" {
		return "", "", fmt.Errorf("agent %s has no classifier model: %w", a.DisplayName(), classifier.ErrNoModel)
	}
	// Checked whatever the switch says: an item still in flight after the
	// switch was turned off is held to the same rule.
	if !cfg.ClassifierModelUsable(alias) {
		return "", "", fmt.Errorf("model %s is not in Classification (agent %s): %w", alias, a.DisplayName(), classifier.ErrNoModel)
	}
	mc, err := cfg.GetModelConfig(alias)
	if err != nil || mc == nil {
		return "", "", fmt.Errorf("model %s is not configured or not enabled: %w", alias, classifier.ErrNoModel)
	}
	prov, err := cfg.GetProvider(mc.Provider)
	if err != nil || prov == nil {
		return "", "", fmt.Errorf("model %s has no usable provider: %w", alias, classifier.ErrNoModel)
	}
	tracker := al.cooldownTracker()
	if tracker != nil && !tracker.IsAvailable(mc.Provider, mc.Model) {
		return "", "", &classifier.CooldownError{Model: alias, Remaining: tracker.CooldownRemaining(mc.Provider, mc.Model)}
	}
	if al.dispatcher == nil {
		return "", "", fmt.Errorf("no model dispatcher: %w", classifier.ErrNoModel)
	}

	var provider providers.LLMProvider
	if config.CLIAgentByProtocol(prov.Protocol) != nil {
		dir, cleanup, derr := h.isolatedFolder(ctx, al, cfg, agentID)
		if derr != nil {
			return "", "", derr
		}
		defer cleanup()
		provider, err = al.dispatcher.GetIsolated(alias, dir)
	} else {
		provider, err = al.dispatcher.Get(alias)
	}
	if err != nil {
		return "", "", fmt.Errorf("model %s: %w", alias, err)
	}

	start := time.Now()
	resp, chatErr := provider.Chat(ctx, []providers.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	}, nil, mc.Model, nil)
	emitLLMFinishEvent(llmFinish{
		caller:         llmCaller{agentID: a.ID, label: a.DisplayName()},
		kind:           llmKindClassifier,
		ourProvider:    mc.Provider,
		ourModel:       alias,
		requestedModel: mc.Model,
		elapsedMs:      time.Since(start).Milliseconds(),
		resp:           resp,
		err:            chatErr,
	})
	if chatErr != nil {
		if resp != nil && resp.Status != nil && resp.Status.CostUSD != nil {
			al.recordSpend(*resp.Status.CostUSD)
		}
		if ctx.Err() == nil && tracker != nil {
			if fe := providers.ClassifyError(chatErr, mc.Provider, mc.Model); fe != nil {
				tracker.MarkFailure(mc.Provider, mc.Model, fe.Reason, fe.Status, fe.RetryAfter)
			}
		}
		logger.WarnCF("classifier", "Classifier model call failed", map[string]any{
			"agent_id":   a.ID,
			"classifier": classifier.ClassifierID(ctx),
			"model":      alias,
			"error":      redactedError(chatErr),
		})
		return "", alias, fmt.Errorf("model %s: %w", alias, chatErr)
	}
	if tracker != nil {
		tracker.MarkSuccess(mc.Provider, mc.Model)
	}
	if resp != nil && resp.Status != nil && resp.Status.CostUSD != nil {
		al.recordSpend(*resp.Status.CostUSD)
	}
	if resp == nil {
		return "", alias, fmt.Errorf("model %s returned no reply", alias)
	}
	return resp.Content, alias, nil
}

// isolatedFolder makes the empty folder one CLI call runs in, under the
// temporary agents' root (internal/temp/classifier-<id>-*), with that
// root's exact modes, and returns its removal.
func (h *ClassifierHost) isolatedFolder(ctx context.Context, al *AgentLoop, cfg *config.Config, agentID string) (string, func(), error) {
	r := al.GetRegistry()
	if r == nil {
		return "", nil, errLoopNotReady
	}
	root := r.TempRoot()
	if root == "" {
		return "", nil, errors.New("no folder for an isolated CLI call in this process")
	}
	modes := cfg.AgentModes(root)
	if err := fileutil.MkdirAllExact(root, modes.Dir); err != nil {
		return "", nil, fmt.Errorf("folder for an isolated CLI call: %w", err)
	}
	name := classifier.ClassifierID(ctx)
	if name == "" {
		name = agentID
	}
	dir, err := os.MkdirTemp(root, classifierTempPrefix+name+"-")
	if err != nil {
		return "", nil, fmt.Errorf("folder for an isolated CLI call: %w", err)
	}
	if err := os.Chmod(dir, modes.Dir); err != nil {
		return "", nil, fmt.Errorf("folder for an isolated CLI call: %w", errors.Join(err, os.RemoveAll(dir)))
	}
	return dir, func() {
		if err := os.RemoveAll(dir); err != nil {
			logger.WarnCF("classifier", "Could not remove a classifier's CLI folder", map[string]any{
				"agent_id": agentID, "classifier": name, "path": dir, "error": err.Error(),
			})
		}
	}, nil
}

// Summary implements classifier.Notifier: text is posted as it is, after the
// agent's name, to the agent's default chat (its default binding). It is a
// message from the classifier, not a turn: the agent does not see it. An
// agent with no default chat gets no summary (the error says so and the
// service logs it).
func (h *ClassifierHost) Summary(ctx context.Context, agentID, text string) error {
	al, cfg, err := h.bound()
	if err != nil {
		return err
	}
	name := agentID
	if a := cfg.AgentByID(agentID); a != nil {
		name = a.DisplayName()
	}
	channel, chatID, _, ok := cfg.CronTarget(agentID)
	if !ok {
		return fmt.Errorf("agent %s has no default chat", name)
	}
	classifierID := classifier.ClassifierID(ctx)
	pubCtx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()
	return al.bus.PublishOutbound(pubCtx, bus.OutboundMessage{
		Channel: channel,
		ChatID:  chatID,
		Content: name + ": " + text,
		OnDelivery: func(err error) {
			if err != nil {
				logger.WarnCF("classifier", "Classifier summary not delivered", map[string]any{
					"agent_id": agentID, "classifier": classifierID, "channel": channel, "error": err.Error(),
				})
			}
		},
	})
}

// classifierNoticeSender is the sender id of a classifier's failure notice,
// as the agent sees it ("[System: classifier] ...").
const classifierNoticeSender = "classifier"

// RunFailed implements classifier.Notifier: the notice of a failed run is
// queued as a system message for the agent, in its one conversation, and
// the call returns without waiting for the agent's turn. The agent's answer
// goes to its default chat (its default binding); with none, it stays in
// the conversation.
func (h *ClassifierHost) RunFailed(ctx context.Context, agentID, text string) error {
	al, cfg, err := h.bound()
	if err != nil {
		return err
	}
	// processSystemMessage keeps a message of the ask channel in the agent's
	// main conversation and sends its answer to no chat.
	channel, chatID := constants.AgentMessageChannel, ""
	if c, id, _, ok := cfg.CronTarget(agentID); ok {
		channel, chatID = c, id
	}
	pubCtx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()
	if err := al.bus.PublishInbound(pubCtx, bus.InboundMessage{
		Channel:    "system",
		SenderID:   classifierNoticeSender,
		ChatID:     chatID,
		Content:    text,
		SessionKey: routing.BuildAgentMainSessionKey(agentID),
		Metadata: map[string]string{
			metadataKeyPreresolvedAgentID: agentID,
			bus.MetaOriginChannel:         channel,
		},
		Internal: true,
	}); err != nil {
		return fmt.Errorf("queue the notice for agent %s: %w", agentID, err)
	}
	return nil
}

// classifierStateAlertPrefix starts the alert id of an agent whose
// classifier state cannot be opened ("classifier-state:<agent>").
const classifierStateAlertPrefix = "classifier-state:"

// StateFailed implements classifier.Notifier: the operator alert for an
// agent whose classifiers' state.db cannot be opened, so none of them runs.
// Nothing runs before Bind, so an unbound host has nothing to raise.
func (h *ClassifierHost) StateFailed(_ context.Context, agentID, path string, err error) {
	al, cfg, bErr := h.bound()
	if bErr != nil {
		return
	}
	name := agentID
	if a := cfg.AgentByID(agentID); a != nil {
		name = a.DisplayName()
	}
	details := ""
	if err != nil {
		details = err.Error()
	}
	al.Alerter().Send(alerter.Alert{
		Title:       "Classifier state unavailable",
		Description: "agent " + name + ": " + path + " cannot be opened, so its classifiers do not run",
		Details:     details,
		EventID:     classifierStateAlertPrefix + agentID,
	})
}

// Redact is classifier.Host.Redact: the secrets of a provider's or tool's
// error text (API keys, credentials) replaced, as for a chat or the log.
func (h *ClassifierHost) Redact(s string) string { return redactSecrets(s) }

// Record implements classifier.Auditor: a definition written is a
// classifier_write row, an item handled a classification row, both under
// the owning agent and summarised as <agent>/<classifier>. A change a clone
// made names the clone in the details ("clone": "1a2b3c4d").
func (h *ClassifierHost) Record(ev classifier.AuditEvent) {
	store := audit.Default()
	if store == nil {
		return
	}
	details := make(map[string]any, len(ev.Details)+3)
	maps.Copy(details, ev.Details)
	if ev.Clone != "" {
		details["clone"] = ev.Clone
	}
	outcome := audit.OutcomeOK
	kind := audit.KindClassifierWrite
	if ev.Kind == classifier.AuditClassification {
		kind = audit.KindClassification
		details["item_id"], details["category"], details["outcome"] = ev.ItemID, ev.Category, ev.Outcome
		if ev.Outcome == classifier.OutcomeFailed {
			outcome = audit.OutcomeError
		}
	} else if ev.Outcome != "" && ev.Outcome != audit.OutcomeOK {
		outcome = audit.OutcomeError
	}
	b, err := json.Marshal(details)
	if err != nil {
		b = []byte(`{}`)
	}
	store.Record(audit.Event{
		Kind:    kind,
		Agent:   ev.Agent,
		Tool:    ev.Action,
		Summary: ev.Agent + "/" + ev.Classifier,
		Details: string(b),
		Outcome: outcome,
	})
}
