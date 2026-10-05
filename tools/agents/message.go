// ClawEh
// License: MIT

package agents

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/tools"
)

const messageToolDescription = "Send a message to another agent. " +
	"wait_seconds 0 whispers: the message is added privately to the start of the agent's next message, " +
	"and this returns at once. wait_seconds above 0 asks: the agent handles the message in a normal turn, " +
	"with its own tools, and this returns its reply (or says it did not reply in time). " +
	"Only agents in your subagents.allow_agents can be messaged."

// maxWaitSeconds bounds wait_seconds (a day).
const maxWaitSeconds = 24 * 60 * 60

func messageToolSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"agent": map[string]any{
				"type":        "string",
				"description": "The agent to message, by id or name",
			},
			"message": map[string]any{
				"type":        "string",
				"description": "The message",
			},
			"wait_seconds": map[string]any{
				"type":        "integer",
				"minimum":     0,
				"description": "0 to whisper (no reply); more than 0 to ask and wait up to this many seconds for the reply",
			},
		},
		"required": []string{"agent", "message", "wait_seconds"},
	}
}

// messageTool builds agent_message for the agent host was built for. host is
// the zero ToolDeps during deps-free enumeration; the handler then reports
// the tool unavailable.
func messageTool(host tools.ToolDeps) global.ToolDefinition {
	return global.ToolDefinition{
		Name:        "message",
		Description: messageToolDescription,
		RawSchema:   messageToolSchema(),
		Category:    "agents",
		Handler: func(call *global.ToolCall) (*global.Result, error) {
			return runMessageTool(host, call), nil
		},
	}
}

// runMessageTool validates the call, checks the target against the caller's
// subagents.allow_agents, then whispers (wait_seconds 0) or asks.
func runMessageTool(host tools.ToolDeps, call *global.ToolCall) *global.Result {
	fail := func(format string, args ...any) *global.Result {
		return &global.Result{IsError: true, ForLLM: fmt.Sprintf(format, args...)}
	}
	if host.Cfg == nil || host.Agents == nil || host.Messenger == nil {
		return fail("agent_message is not available")
	}
	ref := strings.TrimSpace(strArg(call.Args, "agent"))
	message := strArg(call.Args, "message")
	if ref == "" {
		return fail("agent is required")
	}
	if strings.TrimSpace(message) == "" {
		return fail("message is required")
	}
	seconds, ok := numArg(call.Args, "wait_seconds")
	if !ok || seconds < 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return fail("wait_seconds is required: 0 to whisper, or the seconds to wait for a reply")
	}

	target := host.Cfg.FindAgent(ref)
	if target == nil {
		return fail("There is no agent named %s.", ref)
	}
	name := target.DisplayName()
	if !host.Agents.CanTarget(target.ID) {
		return fail("You may not message %s: it is not in your subagents.allow_agents.", name)
	}
	from := host.AgentID
	if caller := host.Cfg.AgentByID(host.AgentID); caller != nil {
		from = caller.DisplayName()
	}

	if seconds == 0 {
		if err := host.Messenger.Whisper(call.Ctx, from, target.ID, message); err != nil {
			return fail("Could not whisper to %s: %v", name, err)
		}
		return &global.Result{ForLLM: "Whispered to " + name + "."}
	}

	// The target's request_timeout caps the wait anyway; the clamp only keeps
	// the conversion to a Duration in range.
	wait := time.Duration(min(seconds, maxWaitSeconds) * float64(time.Second))
	reply, err := host.Messenger.Ask(call.Ctx, from, target.ID, message, wait)
	if err != nil {
		if errors.Is(err, tools.ErrMaxDepth) || errors.Is(err, tools.ErrAskLoop) {
			return fail("%v", err)
		}
		return fail("Could not ask %s: %v", name, err)
	}
	return askResult(name, reply)
}

// askResult renders an ask's reply as the tool result.
func askResult(name string, reply tools.AgentReply) *global.Result {
	switch reply.Outcome {
	case tools.OutcomeTimeout:
		return &global.Result{ForLLM: reply.Text}
	case bus.OutcomeError:
		return &global.Result{IsError: true, ForLLM: fmt.Sprintf("%s's turn failed: %s", name, reply.Text)}
	case bus.OutcomeCancelled:
		return &global.Result{ForLLM: name + "'s turn was cancelled before it replied."}
	}
	if strings.TrimSpace(reply.Text) == "" {
		return &global.Result{ForLLM: name + " gave no reply."}
	}
	return &global.Result{ForLLM: reply.Text}
}

// numArg returns the numeric argument under key (a JSON number, or an
// integer passed by a Go caller).
func numArg(args map[string]any, key string) (float64, bool) {
	switch v := args[key].(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	default:
		return 0, false
	}
}

// hostDeps recovers Claw's ToolDeps from deps (the zero value during
// deps-free enumeration).
func hostDeps(deps global.Deps) tools.ToolDeps {
	if host, ok := deps.Host.(tools.ToolDeps); ok {
		return host
	}
	return tools.ToolDeps{}
}
