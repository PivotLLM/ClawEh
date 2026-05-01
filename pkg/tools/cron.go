package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/bus"
	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/constants"
	"github.com/PivotLLM/ClawEh/pkg/cron"
	"github.com/PivotLLM/ClawEh/pkg/utils"
)

// JobExecutor is the interface used by isolated-mode cron jobs to run directly
// through the agent without going through the inbound bus queue.
type JobExecutor interface {
	ProcessDirectWithChannel(ctx context.Context, content, sessionKey, channel, chatID, peerKind string) (string, error)
}

// CronTool provides scheduling capabilities for the agent
type CronTool struct {
	cronService *cron.CronService
	executor    JobExecutor
	msgBus      *bus.MessageBus
	execTool    *ExecTool
}

// NewCronTool creates a new CronTool
// execTimeout: 0 means no timeout, >0 sets the timeout duration
func NewCronTool(
	cronService *cron.CronService, executor JobExecutor, msgBus *bus.MessageBus, workspace string, restrict bool,
	execTimeout time.Duration, config *config.Config,
) (*CronTool, error) {
	execTool, err := NewExecToolWithConfig(workspace, restrict, config)
	if err != nil {
		return nil, fmt.Errorf("unable to configure exec tool: %w", err)
	}

	execTool.SetTimeout(execTimeout)
	return &CronTool{
		cronService: cronService,
		executor:    executor,
		msgBus:      msgBus,
		execTool:    execTool,
	}, nil
}

// Name returns the tool name
func (t *CronTool) Name() string {
	return "cron"
}

// Description returns the tool description
func (t *CronTool) Description() string {
	return "Schedule reminders, tasks, or system commands. IMPORTANT: When user asks to be reminded or scheduled, you MUST call this tool. Use 'at_seconds' for one-time reminders (e.g., 'remind me in 10 minutes' → at_seconds=600). Use 'every_seconds' ONLY for recurring tasks (e.g., 'every 2 hours' → every_seconds=7200). Use 'cron_expr' for complex recurring schedules. Use 'command' to execute shell commands directly. Use mode='agent' (default) so the agent remembers the conversation; use mode='isolated' for fire-and-forget tasks with no persistent context. When using via MCP, explicitly pass channel and chat_id from your Current Session context."
}

// Parameters returns the tool parameters schema
func (t *CronTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"add", "list", "remove", "enable", "disable"},
				"description": "Action to perform. Use 'add' when user wants to schedule a reminder or task.",
			},
			"message": map[string]any{
				"type":        "string",
				"description": "The reminder/task message to display when triggered. If 'command' is used, this describes what the command does.",
			},
			"command": map[string]any{
				"type":        "string",
				"description": "Optional: Shell command to execute directly (e.g., 'df -h'). If set, the agent will run this command and report output instead of just showing the message. 'deliver' will be forced to false for commands.",
			},
			"command_confirm": map[string]any{
				"type":        "boolean",
				"description": "Required when using command=true. Must be true to explicitly confirm scheduling a shell command.",
			},
			"at_seconds": map[string]any{
				"type":        "integer",
				"description": "One-time reminder: seconds from now when to trigger (e.g., 600 for 10 minutes later). Use this for one-time reminders like 'remind me in 10 minutes'.",
			},
			"every_seconds": map[string]any{
				"type":        "integer",
				"description": "Recurring interval in seconds (e.g., 3600 for every hour). Use this ONLY for recurring tasks like 'every 2 hours' or 'daily reminder'.",
			},
			"cron_expr": map[string]any{
				"type":        "string",
				"description": "Cron expression for complex recurring schedules (e.g., '0 9 * * *' for daily at 9am). Use this for complex recurring schedules.",
			},
			"job_id": map[string]any{
				"type":        "string",
				"description": "Job ID (for remove/enable/disable)",
			},
			"mode": map[string]any{
				"type":        "string",
				"enum":        []string{"agent", "isolated", "deliver", "command"},
				"description": "Execution mode. 'agent' (default): agent processes message in the user's ongoing session — replies continue the conversation. 'isolated': agent processes message in a fresh one-off session — no shared context. 'deliver': send message verbatim, no LLM. 'command': run a shell command (use the 'command' field).",
			},
			"peer_kind": map[string]any{
				"type":        "string",
				"enum":        []string{"channel", "direct"},
				"description": "Set to 'direct' when 'to' is a DM user ID rather than a channel ID. Ensures replies land in the same session. Default: 'channel'.",
			},
			"channel": map[string]any{
				"type":        "string",
				"description": "Channel identifier for the current session (e.g. 'slack', 'telegram'). Required when scheduling via MCP — read from '## Current Session' in your context.",
			},
			"chat_id": map[string]any{
				"type":        "string",
				"description": "Chat/user ID for the current session. Required when scheduling via MCP — read from '## Current Session' in your context.",
			},
		},
		"required": []string{"action"},
	}
}

// Execute runs the tool with the given arguments
func (t *CronTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	action, ok := args["action"].(string)
	if !ok {
		return ErrorResult("action is required")
	}

	switch action {
	case "add":
		return t.addJob(ctx, args)
	case "list":
		return t.listJobs()
	case "remove":
		return t.removeJob(args)
	case "enable":
		return t.enableJob(args, true)
	case "disable":
		return t.enableJob(args, false)
	default:
		return ErrorResult(fmt.Sprintf("unknown action: %s", action))
	}
}

func (t *CronTool) addJob(ctx context.Context, args map[string]any) *ToolResult {
	channel := ToolChannel(ctx)
	chatID := ToolChatID(ctx)

	// When called via MCP the context has no session info; accept explicit args.
	if channel == "" {
		channel, _ = args["channel"].(string)
	}
	if chatID == "" {
		chatID, _ = args["chat_id"].(string)
	}

	if channel == "" || chatID == "" {
		return ErrorResult("no session context (channel/chat_id not set). Use this tool in an active conversation.")
	}

	message, ok := args["message"].(string)
	if !ok || message == "" {
		return ErrorResult("message is required for add")
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
		return ErrorResult("one of at_seconds, every_seconds, or cron_expr is required")
	}

	// Read mode parameter, default to "agent"
	mode, _ := args["mode"].(string)
	if mode == "" {
		mode = "agent"
	}

	peerKind, _ := args["peer_kind"].(string)
	if peerKind == "" {
		peerKind = "channel"
	}

	// GHSA-pv8c-p6jf-3fpp: command scheduling requires internal channel + explicit confirm.
	// Non-command reminders (plain messages) remain open to all channels.
	command, _ := args["command"].(string)
	commandConfirm, _ := args["command_confirm"].(bool)
	if command != "" {
		if !constants.IsInternalChannel(channel) {
			return ErrorResult("scheduling command execution is restricted to internal channels")
		}
		if !commandConfirm {
			return ErrorResult("command_confirm=true is required to schedule command execution")
		}
		mode = "command"
	}

	// Truncate message for job name (max 30 chars)
	messagePreview := utils.Truncate(message, 30)

	job, err := t.cronService.AddJob(
		messagePreview,
		schedule,
		message,
		mode,
		channel,
		chatID,
		peerKind,
	)
	if err != nil {
		return ErrorResult(fmt.Sprintf("Error adding job: %v", err))
	}

	if command != "" {
		job.Payload.Command = command
		// Need to save the updated payload
		t.cronService.UpdateJob(job)
	}

	return SilentResult(fmt.Sprintf("Cron job added: %s (id: %s)", job.Name, job.ID))
}

func (t *CronTool) listJobs() *ToolResult {
	jobs := t.cronService.ListJobs(false)

	if len(jobs) == 0 {
		return SilentResult("No scheduled jobs")
	}

	var result strings.Builder
	result.WriteString("Scheduled jobs:\n")
	for _, j := range jobs {
		var scheduleInfo string
		if j.Schedule.Kind == "every" && j.Schedule.EveryMS != nil {
			scheduleInfo = fmt.Sprintf("every %ds", *j.Schedule.EveryMS/1000)
		} else if j.Schedule.Kind == "cron" {
			scheduleInfo = j.Schedule.Expr
		} else if j.Schedule.Kind == "at" {
			scheduleInfo = "one-time"
		} else {
			scheduleInfo = "unknown"
		}
		result.WriteString(fmt.Sprintf("- %s (id: %s, %s)\n", j.Name, j.ID, scheduleInfo))
	}

	return SilentResult(result.String())
}

func (t *CronTool) removeJob(args map[string]any) *ToolResult {
	jobID, ok := args["job_id"].(string)
	if !ok || jobID == "" {
		return ErrorResult("job_id is required for remove")
	}

	removed, err := t.cronService.RemoveJob(jobID)
	if err != nil {
		return ErrorResult(fmt.Sprintf("Failed to remove job %s: %v", jobID, err))
	}
	if removed {
		return SilentResult(fmt.Sprintf("Cron job removed: %s", jobID))
	}
	return ErrorResult(fmt.Sprintf("Job %s not found", jobID))
}

func (t *CronTool) enableJob(args map[string]any, enable bool) *ToolResult {
	jobID, ok := args["job_id"].(string)
	if !ok || jobID == "" {
		return ErrorResult("job_id is required for enable/disable")
	}

	job, err := t.cronService.EnableJob(jobID, enable)
	if err != nil {
		return ErrorResult(fmt.Sprintf("Failed to update job %s: %v", jobID, err))
	}
	if job == nil {
		return ErrorResult(fmt.Sprintf("Job %s not found", jobID))
	}

	status := "enabled"
	if !enable {
		status = "disabled"
	}
	return SilentResult(fmt.Sprintf("Cron job '%s' %s", job.Name, status))
}

// cronPrefixedMessage returns msg prefixed with a cron metadata header indicating
// the fire time so the LLM knows the message originated from a scheduled job.
func cronPrefixedMessage(fireTime time.Time, msg string) string {
	ts := fireTime.Format("2006-01-02 15:04 MST")
	return fmt.Sprintf("The following message is from a cron job that fired at %s:\n\n%s", ts, msg)
}

// ExecuteJob executes a cron job through the agent
func (t *CronTool) ExecuteJob(ctx context.Context, job *cron.CronJob) string {
	fireTime := time.Now()

	// Get channel/chatID from job payload
	channel := job.Payload.Channel
	chatID := job.Payload.To

	// Default values if not set
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

	mode := job.Payload.Mode
	if mode == "" {
		mode = "agent"
	}

	switch mode {
	case "command":
		args := map[string]any{
			"command":   job.Payload.Command,
			"__channel": channel,
			"__chat_id": chatID,
		}
		result := t.execTool.Execute(ctx, args)
		var output string
		if result.IsError {
			output = fmt.Sprintf("Error executing scheduled command: %s", result.ForLLM)
		} else {
			output = fmt.Sprintf("Scheduled command '%s' executed:\n%s", job.Payload.Command, result.ForLLM)
		}
		pubCtx, pubCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer pubCancel()
		t.msgBus.PublishOutbound(pubCtx, bus.OutboundMessage{
			Channel: channel,
			ChatID:  chatID,
			Content: output,
		})

	case "deliver":
		pubCtx, pubCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer pubCancel()
		t.msgBus.PublishOutbound(pubCtx, bus.OutboundMessage{
			Channel: channel,
			ChatID:  chatID,
			Content: job.Payload.Message,
		})

	case "isolated":
		// Run concurrently in a goroutine — isolated jobs use a unique session key
		// (cron-<id>) that is never shared with live user messages, so there is no
		// risk of state collision. No need to queue behind the main agent loop.
		sessionKey := fmt.Sprintf("cron-%s", job.ID)
		jobChannel := channel
		jobChatID := chatID
		jobContent := cronPrefixedMessage(fireTime, job.Payload.Message)
		jobPeerKind := peerKind
		go func() {
			response, err := t.executor.ProcessDirectWithChannel(context.Background(), jobContent, sessionKey, jobChannel, jobChatID, jobPeerKind)
			if err != nil || response == "" {
				return
			}
			pubCtx, pubCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer pubCancel()
			t.msgBus.PublishOutbound(pubCtx, bus.OutboundMessage{
				Channel: jobChannel,
				ChatID:  jobChatID,
				Content: response,
			})
		}()

	default: // "agent"
		// Queue through the inbound bus — processed in order, same session routing as
		// a live user message from the same channel/chatID/peer.
		msg := bus.InboundMessage{
			Channel:  channel,
			SenderID: "cron",
			ChatID:   chatID,
			Content:  cronPrefixedMessage(fireTime, job.Payload.Message),
		}
		if chatID != "" && chatID != "direct" {
			msg.Peer = bus.Peer{Kind: peerKind, ID: chatID}
		}
		pubCtx, pubCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer pubCancel()
		if err := t.msgBus.PublishInbound(pubCtx, msg); err != nil {
			return fmt.Sprintf("Error queuing agent job: %v", err)
		}
	}

	return "ok"
}
