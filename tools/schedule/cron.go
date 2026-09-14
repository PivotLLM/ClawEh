package schedule

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/cron"
	"github.com/PivotLLM/ClawEh/cronmsg"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/routing"
	"github.com/PivotLLM/ClawEh/tools"
	"github.com/PivotLLM/ClawEh/utils"
)

// CronTool provides scheduling capabilities for the agent. A scheduled job is
// addressed to an agent; when it fires it is delivered to that agent's default
// channel (resolved live from config), where the agent processes it — the model
// never chooses the destination.
type CronTool struct {
	cronService *cron.CronService
	msgBus      *bus.MessageBus
	// getConfig returns the live config (re-read on each call so default-channel
	// changes and global_cron grants take effect without restart).
	getConfig func() *config.Config
	// agentTools resolves an agent's tool registry, for watch probes. Resolved
	// per run rather than captured once, so a tool revoked in config stops being
	// probed. Nil disables watch jobs rather than failing the whole cron service.
	agentTools func(agentID string) *tools.ToolRegistry

	// Listen jobs: one goroutine per enabled job, kept in step with the store
	// by a supervisor. See listen.go.
	listenMu   sync.Mutex
	listeners  map[string]*listener
	listenStop context.CancelFunc
	listenKick chan struct{}
	listenWG   sync.WaitGroup
}

// NewCronTool creates a new CronTool. getConfig must return the current config.
func NewCronTool(cronService *cron.CronService, msgBus *bus.MessageBus, getConfig func() *config.Config) *CronTool {
	return &CronTool{cronService: cronService, msgBus: msgBus, getConfig: getConfig}
}

// SetAgentTools wires the per-agent tool registry lookup that watch probes need.
// Injected after construction because the agent registry is built later than the
// cron service.
func (t *CronTool) SetAgentTools(fn func(agentID string) *tools.ToolRegistry) {
	t.agentTools = fn
}

// config returns the live config, or nil if unavailable.
func (t *CronTool) config() *config.Config {
	if t.getConfig == nil {
		return nil
	}
	return t.getConfig()
}

// IsSessionScoped makes the MCP dispatcher (and agent loop) inject the session
// key, so the tool can scope jobs to the calling agent.
func (t *CronTool) IsSessionScoped() bool { return true }

// callerAgentID extracts the calling agent's ID from the session key in context
// (e.g. "agent:amber:main" → "amber"). Empty when unavailable — callers treat
// an empty id as "owns nothing" so cron stays fail-closed.
func callerAgentID(ctx context.Context) string {
	if pk := routing.ParseAgentSessionKey(tools.ToolSessionKey(ctx)); pk != nil {
		return pk.AgentID
	}
	return ""
}

// Name returns the tool name
func (t *CronTool) Name() string {
	return "cron_schedule"
}

// Description returns the tool description
func (t *CronTool) Description() string {
	return "Schedule a message for later — a reminder or a recurring task. When the user asks to be reminded or to schedule something, you MUST call this tool. When the job fires, the message is delivered to the target agent's default channel (configured by the operator — you do not choose where it goes); by default the target is you. Use 'at_seconds' for a one-time reminder (e.g. 'in 10 minutes' → at_seconds=600), 'every_seconds' for a simple recurring interval (e.g. 'every 2 hours' → every_seconds=7200), or 'cron_expr' for calendar schedules (e.g. '0 9 * * *' for daily at 9am). With 'watch_tool' the job becomes a change probe; with 'listen' it becomes a persistent callback that keeps a long-poll tool running and delivers each new event. Use 'list'/'get' to review jobs and 'remove'/'enable'/'disable' to manage them. The optional 'agent' parameter schedules for another agent (only permitted for authorized agents)."
}

// Parameters returns the tool parameters schema
func (t *CronTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"add", "list", "get", "remove", "enable", "disable"},
				"description": "Action to perform. Use 'add' to schedule a reminder/task; 'list' to see all jobs; 'get' for one job's full detail; 'remove'/'enable'/'disable' to manage a job.",
			},
			"message": map[string]any{
				"type":        "string",
				"description": "The reminder/task message delivered when the job fires (for 'add').",
			},
			"at_seconds": map[string]any{
				"type":        "integer",
				"description": "One-time reminder: seconds from now when to trigger (e.g. 600 for 10 minutes later).",
			},
			"every_seconds": map[string]any{
				"type":        "integer",
				"description": "Recurring interval in seconds (e.g. 3600 for hourly).",
			},
			"cron_expr": map[string]any{
				"type":        "string",
				"description": "Cron expression for calendar schedules (e.g. '0 9 * * *' for daily at 9am).",
			},
			"watch_tool": map[string]any{
				"type":        "string",
				"description": "Optional. Makes this a change watch instead of an unconditional reminder: the named tool is called on the schedule WITHOUT waking a model, and 'message' is only delivered when the watched fields change. Use the published tool name, e.g. 'google_gmail_messages_list'. The tool must be one you are allowed to use and must not be session-scoped.",
			},
			"watch_args": map[string]any{
				"type":        "object",
				"description": "Arguments passed to watch_tool, exactly as you would pass them when calling it yourself.",
			},
			"watch_fields": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Dot-paths into watch_tool's result naming the values that decide 'changed', e.g. ['messages.id']. A path crossing a list applies to every element and collects the results, so 'messages.id' is the set of ids currently present. Choose fields that only move when something you care about happens — watching a whole result also catches unread counts and timestamps, which fire constantly. Omit to compare the entire result.",
			},
			"listen": map[string]any{
				"type":        "boolean",
				"description": "Optional. With watch_tool, makes the job a persistent callback instead of a scheduled probe: the tool is called continuously in the background and each call waits for it to return (an event, a dropped connection, or the timeout), then it is called again at once. Whenever the watched fields are present, 'message' is delivered followed by the tool's full result — every time, identical results included, unless suppress_repeats is set. Use it for long-poll event tools such as documents_event_wait. Takes no schedule: do not pass at_seconds, every_seconds or cron_expr.",
			},
			"watch_timeout_seconds": map[string]any{
				"type":        "integer",
				"description": "Optional, for listen jobs: how long one call may wait for an event before it is dropped and made again (default 300).",
			},
			"suppress_repeats": map[string]any{
				"type":        "boolean",
				"description": "Optional, for listen jobs. By default every result with the watched fields present is delivered, even one identical to the last — each occurrence may matter, e.g. a document edited several times in a row. Set true for a source that replays its latest event on every reconnect, so an identical result is delivered once.",
			},
			"job_id": map[string]any{
				"type":        "string",
				"description": "Job ID (for 'get', 'remove', 'enable', 'disable').",
			},
			"agent": map[string]any{
				"type":        "string",
				"description": "Target agent id. Optional; defaults to you. Scheduling or managing another agent's jobs requires authorization (global_cron).",
			},
		},
		"required": []string{"action"},
	}
}

// Execute runs the tool with the given arguments
func (t *CronTool) Execute(ctx context.Context, args map[string]any) *tools.ToolResult {
	action, ok := args["action"].(string)
	if !ok {
		return tools.ErrorResult("action is required")
	}

	// Jobs are addressed to an agent. By default that's the caller; an authorized
	// (global_cron) agent may target another agent via the `agent` arg. Operators
	// see everything via the `claw cron` CLI.
	callerID := callerAgentID(ctx)
	target, errRes := t.resolveTargetAgent(args, callerID)
	if errRes != nil {
		return errRes
	}

	switch action {
	case "add":
		return t.addJob(args, target)
	case "list":
		return t.listJobs(target)
	case "get":
		return t.getJob(args, target)
	case "remove":
		return t.removeJob(args, target)
	case "enable":
		return t.enableJob(args, true, target)
	case "disable":
		return t.enableJob(args, false, target)
	default:
		return tools.ErrorResult(fmt.Sprintf("unknown action: %s", action))
	}
}

// resolveTargetAgent returns the agent a cron action operates on: the caller by
// default, or the `agent` arg when the caller is authorized (global_cron) to act
// on another agent. Returns an error result if unauthorized or no caller agent.
func (t *CronTool) resolveTargetAgent(args map[string]any, callerID string) (string, *tools.ToolResult) {
	target := callerID
	if a, ok := args["agent"].(string); ok {
		if a = strings.TrimSpace(a); a != "" && !strings.EqualFold(a, callerID) {
			cfg := t.config()
			if cfg == nil || !cfg.AgentHasGlobalCron(callerID) {
				return "", tools.ErrorResult("not authorized to schedule or manage another agent's cron jobs (requires global_cron)")
			}
			// Normalize to the lowercased form used for session-derived agent ids,
			// so the target agent can later see/manage the job by its own id.
			target = strings.ToLower(a)
		}
	}
	if target == "" {
		return "", tools.ErrorResult("no calling agent in context")
	}
	return target, nil
}

// ownedJob returns the job with the given id if it belongs to agentID, else nil.
// The caller's authorization to act on agentID is already settled by
// resolveTargetAgent. Returns nil when agentID is empty.
func (t *CronTool) ownedJob(jobID, agentID string) *cron.CronJob {
	if jobID == "" || agentID == "" {
		return nil
	}
	jobs := t.cronService.ListJobs(true)
	for i := range jobs {
		if jobs[i].ID == jobID && jobs[i].AgentID == agentID {
			return &jobs[i]
		}
	}
	return nil
}

func (t *CronTool) addJob(args map[string]any, agentID string) *tools.ToolResult {
	// The destination is resolved at fire time from the target agent's default
	// channel — never captured here and never chosen by the model. Require that
	// the target agent has a usable default channel now, so the job is deliverable.
	cfg := t.config()
	if cfg == nil {
		return tools.ErrorResult("cron unavailable: configuration not loaded")
	}
	if _, _, _, ok := cfg.CronTarget(agentID); !ok {
		return tools.ErrorResult(fmt.Sprintf("agent %q has no default channel configured; set a default channel (binding) before scheduling", agentID))
	}

	message, ok := args["message"].(string)
	if !ok || message == "" {
		return tools.ErrorResult("message is required for add")
	}

	var schedule cron.CronSchedule

	// Check for at_seconds (one-time), every_seconds (recurring), or cron_expr
	atSeconds, hasAt := args["at_seconds"].(float64)
	everySeconds, hasEvery := args["every_seconds"].(float64)
	cronExpr, hasCron := args["cron_expr"].(string)
	listen, _ := args["listen"].(bool)

	// Fix: type assertions return true for zero values, need additional validity checks
	// This prevents LLMs that fill unused optional parameters with defaults (0) from triggering wrong type
	hasAt = hasAt && atSeconds > 0
	hasEvery = hasEvery && everySeconds > 0
	hasCron = hasCron && cronExpr != ""

	// Priority: listen > at_seconds > every_seconds > cron_expr
	if listen {
		if hasAt || hasEvery || hasCron {
			return tools.ErrorResult("a listen job runs continuously and takes no schedule; drop at_seconds/every_seconds/cron_expr")
		}
		schedule = cron.CronSchedule{Kind: cron.KindListen}
	} else if hasAt {
		atMS := time.Now().UnixMilli() + int64(atSeconds)*1000
		schedule = cron.CronSchedule{
			Kind: "at",
			AtMS: &atMS,
		}
	} else if hasEvery {
		everyMS := int64(everySeconds) * 1000
		schedule = cron.CronSchedule{
			Kind:    "every",
			EveryMS: &everyMS,
		}
	} else if hasCron {
		schedule = cron.CronSchedule{
			Kind: "cron",
			Expr: cronExpr,
		}
	} else {
		return tools.ErrorResult("one of at_seconds, every_seconds, cron_expr, or listen is required")
	}

	// Job name = a short preview of the message (max 30 chars).
	messagePreview := utils.Truncate(message, 30)

	watch, werr := parseWatchArgs(args)
	if werr != nil {
		return tools.ErrorResult(werr.Error())
	}
	if listen {
		if watch == nil {
			return tools.ErrorResult("listen requires watch_tool: the tool to keep calling")
		}
		if secs, ok := args["watch_timeout_seconds"].(float64); ok && secs > 0 {
			watch.TimeoutSec = int(secs)
		}
		if suppress, ok := args["suppress_repeats"].(bool); ok && suppress {
			watch.SuppressRepeats = true
		}
	}

	// Destination (channel/chat) is left empty: ExecuteJob resolves the target
	// agent's default channel at fire time, so changing the default redirects
	// existing jobs.
	mode := "agent"
	if listen {
		mode = cron.KindListen
	} else if watch != nil {
		mode = "watch"
	}
	job, err := t.cronService.AddJob(messagePreview, schedule, message, mode, "", "", "")
	if err != nil {
		return tools.ErrorResult(fmt.Sprintf("Error adding job: %v", err))
	}

	// Address the job to the target agent: its default channel is the delivery
	// destination and it (or an authorized agent) manages the job.
	job.AgentID = agentID
	job.Payload.Watch = watch
	t.cronService.UpdateJob(job)
	t.kickListeners()

	if listen {
		when := fmt.Sprintf("every time it returns with %s present, repeats included", describeFields(watch.Fields))
		if watch.SuppressRepeats {
			when = fmt.Sprintf("each time %s carry a new value", describeFields(watch.Fields))
		}
		return tools.SilentResult(fmt.Sprintf(
			"Listener added for %s: %s (id: %s). It keeps %q running in the background and messages you with the full result %s.",
			agentID, job.Name, job.ID, watch.Tool, when))
	}
	if watch != nil {
		return tools.SilentResult(fmt.Sprintf(
			"Watch added for %s: %s (id: %s). It calls %q on the schedule and only messages you when %s change. "+
				"The first run records a baseline silently.",
			agentID, job.Name, job.ID, watch.Tool, describeFields(watch.Fields)))
	}
	return tools.SilentResult(fmt.Sprintf("Cron job added for %s: %s (id: %s)", agentID, job.Name, job.ID))
}

// formatSchedule renders a job's schedule as readable text.
func formatSchedule(j *cron.CronJob) string {
	switch {
	case j.Schedule.Kind == "every" && j.Schedule.EveryMS != nil:
		return fmt.Sprintf("every %ds", *j.Schedule.EveryMS/1000)
	case j.Schedule.Kind == "cron":
		return j.Schedule.Expr
	case j.Schedule.Kind == "at":
		return "one-time"
	case j.Schedule.Kind == cron.KindListen:
		return "listen (continuous)"
	default:
		return "unknown"
	}
}

// formatNextRun renders the next scheduled run time, or "—" when none/disabled.
func formatNextRun(j *cron.CronJob) string {
	if j.State.NextRunAtMS == nil {
		return "—"
	}
	return time.UnixMilli(*j.State.NextRunAtMS).Format("2006-01-02 15:04")
}

func (t *CronTool) listJobs(agentID string) *tools.ToolResult {
	all := t.cronService.ListJobs(false)
	jobs := make([]cron.CronJob, 0, len(all))
	for _, j := range all {
		if agentID != "" && j.AgentID == agentID {
			jobs = append(jobs, j)
		}
	}

	if len(jobs) == 0 {
		return tools.SilentResult("No scheduled jobs")
	}

	var result strings.Builder
	result.WriteString("Scheduled jobs:\n")
	for i := range jobs {
		j := &jobs[i]
		state := "enabled"
		if !j.Enabled {
			state = "disabled"
		}
		fmt.Fprintf(&result, "- %s (id: %s, %s, %s, next: %s)\n",
			j.Name, j.ID, formatSchedule(j), state, formatNextRun(j))
	}

	return tools.SilentResult(result.String())
}

func (t *CronTool) getJob(args map[string]any, agentID string) *tools.ToolResult {
	jobID, ok := args["job_id"].(string)
	if !ok || jobID == "" {
		return tools.ErrorResult("job_id is required for get")
	}
	job := t.ownedJob(jobID, agentID)
	if job == nil {
		return tools.ErrorResult(fmt.Sprintf("Job %s not found", jobID))
	}

	state := "enabled"
	if !job.Enabled {
		state = "disabled"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Job %s\n", job.ID)
	fmt.Fprintf(&b, "  name:     %s\n", job.Name)
	fmt.Fprintf(&b, "  schedule: %s\n", formatSchedule(job))
	fmt.Fprintf(&b, "  status:   %s\n", state)
	fmt.Fprintf(&b, "  next run: %s\n", formatNextRun(job))
	if job.State.LastRunAtMS != nil {
		fmt.Fprintf(&b, "  last run: %s\n", time.UnixMilli(*job.State.LastRunAtMS).Format("2006-01-02 15:04"))
	}
	fmt.Fprintf(&b, "  delivers to: %s / %s\n", job.Payload.Channel, job.Payload.To)
	fmt.Fprintf(&b, "  message:  %s", job.Payload.Message)
	return tools.SilentResult(b.String())
}

func (t *CronTool) removeJob(args map[string]any, agentID string) *tools.ToolResult {
	jobID, ok := args["job_id"].(string)
	if !ok || jobID == "" {
		return tools.ErrorResult("job_id is required for remove")
	}
	// Only the owning agent may remove a job.
	if t.ownedJob(jobID, agentID) == nil {
		return tools.ErrorResult(fmt.Sprintf("Job %s not found", jobID))
	}

	removed, err := t.cronService.RemoveJob(jobID)
	if err != nil {
		return tools.ErrorResult(fmt.Sprintf("Failed to remove job %s: %v", jobID, err))
	}
	t.kickListeners()
	if removed {
		return tools.SilentResult(fmt.Sprintf("Cron job removed: %s", jobID))
	}
	return tools.ErrorResult(fmt.Sprintf("Job %s not found", jobID))
}

func (t *CronTool) enableJob(args map[string]any, enable bool, agentID string) *tools.ToolResult {
	jobID, ok := args["job_id"].(string)
	if !ok || jobID == "" {
		return tools.ErrorResult("job_id is required for enable/disable")
	}
	// Only the owning agent may enable/disable a job.
	if t.ownedJob(jobID, agentID) == nil {
		return tools.ErrorResult(fmt.Sprintf("Job %s not found", jobID))
	}

	job, err := t.cronService.EnableJob(jobID, enable)
	if err != nil {
		return tools.ErrorResult(fmt.Sprintf("Failed to update job %s: %v", jobID, err))
	}
	if job == nil {
		return tools.ErrorResult(fmt.Sprintf("Job %s not found", jobID))
	}
	t.kickListeners()

	status := "enabled"
	if !enable {
		status = "disabled"
	}
	return tools.SilentResult(fmt.Sprintf("Cron job '%s' %s", job.Name, status))
}

// ExecuteJob executes a cron job through the agent
func (t *CronTool) ExecuteJob(ctx context.Context, job *cron.CronJob) string {
	// A listen job has no scheduled fire: its loop delivers on its own.
	if job.Schedule.Kind == cron.KindListen {
		return "listen job; delivered by its listener"
	}

	// A watch job asks a tool whether anything changed and stays silent when
	// nothing did. This is the whole point: the model is never woken to discover
	// that there is no new mail.
	message := job.Payload.Message
	if job.Payload.Watch != nil {
		outcome := t.runWatch(ctx, job)
		if !outcome.Changed {
			return "no change"
		}
		message = outcome.Message
	}
	return t.deliver(ctx, job, cronmsg.Build(job.Fingerprint, time.Now(), message))
}

// deliver injects content inbound to the job's destination — the same routing
// a live user message gets — so the agent processes it and replies there. The
// caller chooses the envelope: a cron fire (cronmsg.Build) or a monitor event
// (cronmsg.BuildEvent).
func (t *CronTool) deliver(_ context.Context, job *cron.CronJob, content string) string {
	// Resolve the destination. Agent-addressed jobs (created via the tool) deliver
	// to the target agent's default channel, resolved live so a changed default
	// redirects the job; the resolved (channel, chat, peer) are the binding's own
	// Match fields, so delivery routes straight back to the agent. Operator/CLI
	// jobs carry no agent id and use the explicit channel/to stored on the payload.
	var channel, chatID, peerKind string
	if job.AgentID != "" {
		cfg := t.config()
		if cfg == nil {
			logger.WarnCF("cron", "job skipped: configuration not loaded", map[string]any{"id": job.ID, "agent_id": job.AgentID})
			return "configuration not loaded"
		}
		var ok bool
		channel, chatID, peerKind, ok = cfg.CronTarget(job.AgentID)
		if !ok {
			logger.WarnCF("cron", "job skipped: agent has no default channel", map[string]any{"id": job.ID, "agent_id": job.AgentID})
			return fmt.Sprintf("agent %q has no default channel; job skipped", job.AgentID)
		}
	} else {
		channel, chatID, peerKind = job.Payload.Channel, job.Payload.To, job.Payload.PeerKind
		if channel == "" || chatID == "" {
			logger.WarnCF("cron", "job skipped: operator job missing channel/to", map[string]any{"id": job.ID})
			return "operator job missing channel/to; skipped"
		}
		if peerKind == "" {
			peerKind = "channel"
		}
	}

	msg := bus.InboundMessage{
		Channel:  channel,
		SenderID: "cron",
		ChatID:   chatID,
		Content:  content,
		Peer:     bus.Peer{Kind: peerKind, ID: chatID},
	}
	pubCtx, pubCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer pubCancel()
	if err := t.msgBus.PublishInbound(pubCtx, msg); err != nil {
		return fmt.Sprintf("Error queuing cron job: %v", err)
	}
	return "ok"
}
