package schedule

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/bus"
	"github.com/PivotLLM/ClawEh/pkg/cron"
	"github.com/PivotLLM/ClawEh/pkg/cronmsg"
	"github.com/PivotLLM/ClawEh/pkg/tools"
	"github.com/PivotLLM/ClawEh/pkg/utils"
)

// CronTool provides scheduling capabilities for the agent. A scheduled job
// delivers its message to the channel it was created on (captured from context),
// where the bound agent processes it — the model never chooses the destination.
type CronTool struct {
	cronService *cron.CronService
	msgBus      *bus.MessageBus
}

// NewCronTool creates a new CronTool.
func NewCronTool(cronService *cron.CronService, msgBus *bus.MessageBus) *CronTool {
	return &CronTool{cronService: cronService, msgBus: msgBus}
}

// Name returns the tool name
func (t *CronTool) Name() string {
	return "cron_schedule"
}

// Description returns the tool description
func (t *CronTool) Description() string {
	return "Schedule a message to yourself for later — a reminder or a recurring task. When the user asks to be reminded or to schedule something, you MUST call this tool. The message is delivered back to THIS conversation when it fires (you cannot send it elsewhere), so it arrives on whatever channel the user is using now; if they want it on a specific channel, they should ask there. Use 'at_seconds' for a one-time reminder (e.g. 'in 10 minutes' → at_seconds=600), 'every_seconds' for a simple recurring interval (e.g. 'every 2 hours' → every_seconds=7200), or 'cron_expr' for calendar schedules (e.g. '0 9 * * *' for daily at 9am). Use 'list'/'get' to review jobs and 'remove'/'enable'/'disable' to manage them."
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
			"job_id": map[string]any{
				"type":        "string",
				"description": "Job ID (for 'get', 'remove', 'enable', 'disable').",
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

	switch action {
	case "add":
		return t.addJob(ctx, args)
	case "list":
		return t.listJobs()
	case "get":
		return t.getJob(args)
	case "remove":
		return t.removeJob(args)
	case "enable":
		return t.enableJob(args, true)
	case "disable":
		return t.enableJob(args, false)
	default:
		return tools.ErrorResult(fmt.Sprintf("unknown action: %s", action))
	}
}

func (t *CronTool) addJob(ctx context.Context, args map[string]any) *tools.ToolResult {
	// The destination is the conversation the job is created in — captured from
	// context, never chosen by the model. When it fires, the message is delivered
	// here and the bound agent handles it.
	channel := tools.ToolChannel(ctx)
	chatID := tools.ToolChatID(ctx)
	if channel == "" || chatID == "" {
		return tools.ErrorResult("no session context (channel/chat_id not set). Use this tool in an active conversation.")
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

	// Fix: type assertions return true for zero values, need additional validity checks
	// This prevents LLMs that fill unused optional parameters with defaults (0) from triggering wrong type
	hasAt = hasAt && atSeconds > 0
	hasEvery = hasEvery && everySeconds > 0
	hasCron = hasCron && cronExpr != ""

	// Priority: at_seconds > every_seconds > cron_expr
	if hasAt {
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
		return tools.ErrorResult("one of at_seconds, every_seconds, or cron_expr is required")
	}

	// Job name = a short preview of the message (max 30 chars).
	messagePreview := utils.Truncate(message, 30)

	// Single behavior: the job injects the message inbound to the captured
	// channel/chat when it fires ("agent" mode); peer defaults to "channel".
	job, err := t.cronService.AddJob(
		messagePreview,
		schedule,
		message,
		"agent",
		channel,
		chatID,
		"channel",
	)
	if err != nil {
		return tools.ErrorResult(fmt.Sprintf("Error adding job: %v", err))
	}

	return tools.SilentResult(fmt.Sprintf("Cron job added: %s (id: %s)", job.Name, job.ID))
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

func (t *CronTool) listJobs() *tools.ToolResult {
	jobs := t.cronService.ListJobs(false)

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
		result.WriteString(fmt.Sprintf("- %s (id: %s, %s, %s, next: %s)\n",
			j.Name, j.ID, formatSchedule(j), state, formatNextRun(j)))
	}

	return tools.SilentResult(result.String())
}

func (t *CronTool) getJob(args map[string]any) *tools.ToolResult {
	jobID, ok := args["job_id"].(string)
	if !ok || jobID == "" {
		return tools.ErrorResult("job_id is required for get")
	}
	var job *cron.CronJob
	jobs := t.cronService.ListJobs(true)
	for i := range jobs {
		if jobs[i].ID == jobID {
			job = &jobs[i]
			break
		}
	}
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

func (t *CronTool) removeJob(args map[string]any) *tools.ToolResult {
	jobID, ok := args["job_id"].(string)
	if !ok || jobID == "" {
		return tools.ErrorResult("job_id is required for remove")
	}

	removed, err := t.cronService.RemoveJob(jobID)
	if err != nil {
		return tools.ErrorResult(fmt.Sprintf("Failed to remove job %s: %v", jobID, err))
	}
	if removed {
		return tools.SilentResult(fmt.Sprintf("Cron job removed: %s", jobID))
	}
	return tools.ErrorResult(fmt.Sprintf("Job %s not found", jobID))
}

func (t *CronTool) enableJob(args map[string]any, enable bool) *tools.ToolResult {
	jobID, ok := args["job_id"].(string)
	if !ok || jobID == "" {
		return tools.ErrorResult("job_id is required for enable/disable")
	}

	job, err := t.cronService.EnableJob(jobID, enable)
	if err != nil {
		return tools.ErrorResult(fmt.Sprintf("Failed to update job %s: %v", jobID, err))
	}
	if job == nil {
		return tools.ErrorResult(fmt.Sprintf("Job %s not found", jobID))
	}

	status := "enabled"
	if !enable {
		status = "disabled"
	}
	return tools.SilentResult(fmt.Sprintf("Cron job '%s' %s", job.Name, status))
}

// ExecuteJob executes a cron job through the agent
func (t *CronTool) ExecuteJob(ctx context.Context, job *cron.CronJob) string {
	fireTime := time.Now()

	// Get channel/chatID from job payload
	channel := job.Payload.Channel
	chatID := job.Payload.To
	if channel == "" {
		channel = "cli"
	}
	if chatID == "" {
		chatID = "direct"
	}
	peerKind := job.Payload.PeerKind
	if peerKind == "" {
		peerKind = "channel"
	}

	// Single behavior: inject the message inbound to the job's channel/chat — the
	// same routing a live user message gets — so the bound agent processes it and
	// replies on that channel. Legacy payload modes (deliver/isolated/command) are
	// ignored; every job now behaves this way.
	msg := bus.InboundMessage{
		Channel:  channel,
		SenderID: "cron",
		ChatID:   chatID,
		Content:  cronmsg.Build(job.Fingerprint, fireTime, job.Payload.Message),
	}
	if chatID != "" && chatID != "direct" {
		msg.Peer = bus.Peer{Kind: peerKind, ID: chatID}
	}
	pubCtx, pubCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer pubCancel()
	if err := t.msgBus.PublishInbound(pubCtx, msg); err != nil {
		return fmt.Sprintf("Error queuing cron job: %v", err)
	}
	return "ok"
}
