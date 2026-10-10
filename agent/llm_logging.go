// ClawEh
// License: MIT

package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/dump"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/utils"
)

// formatMessagesForLog formats messages for logging
func formatMessagesForLog(messages []providers.Message) string {
	if len(messages) == 0 {
		return "[]"
	}

	var sb strings.Builder
	sb.WriteString("[\n")
	for i, msg := range messages {
		fmt.Fprintf(&sb, "  [%d] Role: %s\n", i, msg.Role)
		if len(msg.ToolCalls) > 0 {
			sb.WriteString("  ToolCalls:\n")
			for _, tc := range msg.ToolCalls {
				fmt.Fprintf(&sb, "    - ID: %s, Type: %s, Name: %s\n", tc.ID, tc.Type, tc.Name)
				if tc.Function != nil {
					fmt.Fprintf(
						&sb,
						"      Arguments: %s\n",
						utils.Truncate(tc.Function.Arguments, 200),
					)
				}
			}
		}
		if msg.Content != "" {
			content := utils.Truncate(msg.Content, 200)
			fmt.Fprintf(&sb, "  Content: %s\n", content)
		}
		if msg.ToolCallID != "" {
			fmt.Fprintf(&sb, "  ToolCallID: %s\n", msg.ToolCallID)
		}
		sb.WriteString("\n")
	}
	sb.WriteString("]")
	return sb.String()
}

// formatToolsForLog formats tool definitions for logging
func formatToolsForLog(toolDefs []providers.ToolDefinition) string {
	if len(toolDefs) == 0 {
		return "[]"
	}

	var sb strings.Builder
	sb.WriteString("[\n")
	for i, tool := range toolDefs {
		fmt.Fprintf(&sb, "  [%d] Type: %s, Name: %s\n", i, tool.Type, tool.Function.Name)
		fmt.Fprintf(&sb, "      Description: %s\n", tool.Function.Description)
		if len(tool.Function.Parameters) > 0 {
			fmt.Fprintf(
				&sb,
				"      Parameters: %s\n",
				utils.Truncate(fmt.Sprintf("%v", tool.Function.Parameters), 200),
			)
		}
	}
	sb.WriteString("]")
	return sb.String()
}

// dumpRefusal writes a diagnostic file capturing the full LLM input and output
// when the provider returns finish_reason "refusal". Called only when
// cfg.Logging.DumpRefusals is true and dumpsDir is set.
func (al *AgentLoop) dumpRefusal(
	agent *AgentInstance,
	messages []providers.Message,
	response *providers.LLMResponse,
	opts processOptions,
	model string,
	iteration int,
) {
	inputBytes, err := json.Marshal(messages)
	if err != nil {
		logger.WarnCF("agent", "Failed to marshal refusal dump input",
			map[string]any{"agent_id": agent.ID, "error": err.Error()})
		return
	}
	outputBytes, err := json.Marshal(response)
	if err != nil {
		logger.WarnCF("agent", "Failed to marshal refusal dump output",
			map[string]any{"agent_id": agent.ID, "error": err.Error()})
		return
	}

	meta := map[string]any{
		"agent":     agent.ID,
		"model":     model,
		"session":   opts.SessionKey,
		"channel":   opts.Channel,
		"iteration": iteration,
		"timestamp": time.Now().Format(time.RFC3339),
	}

	basename, err := dump.Write(al.dumpsDir, "refusal", meta, json.RawMessage(inputBytes), json.RawMessage(outputBytes))
	if err != nil {
		logger.WarnCF("agent", "Failed to write refusal dump",
			map[string]any{"agent_id": agent.ID, "error": err.Error()})
		return
	}
	logger.WarnCF("agent", "LLM refusal detected — dump written",
		map[string]any{
			"agent_id":    agent.ID,
			"model":       model,
			"session_key": opts.SessionKey,
			"channel":     opts.Channel,
			"iteration":   iteration,
			"dump_base":   basename,
		})
}

// dumpAll writes a diagnostic file capturing the full LLM input and output
// for every response when cfg.Logging.DumpAll is true and dumpsDir is set.
func (al *AgentLoop) dumpAll(
	agent *AgentInstance,
	messages []providers.Message,
	response *providers.LLMResponse,
	opts processOptions,
	model string,
	iteration int,
) {
	inputBytes, err := json.Marshal(messages)
	if err != nil {
		logger.WarnCF("agent", "Failed to marshal dump_all input",
			map[string]any{"agent_id": agent.ID, "error": err.Error()})
		return
	}
	outputBytes, err := json.Marshal(response)
	if err != nil {
		logger.WarnCF("agent", "Failed to marshal dump_all output",
			map[string]any{"agent_id": agent.ID, "error": err.Error()})
		return
	}

	meta := map[string]any{
		"agent":         agent.ID,
		"model":         model,
		"session":       opts.SessionKey,
		"channel":       opts.Channel,
		"iteration":     iteration,
		"finish_reason": response.FinishReason,
		"timestamp":     time.Now().Format(time.RFC3339),
	}

	basename, err := dump.Write(al.dumpsDir, "dump_all", meta, json.RawMessage(inputBytes), json.RawMessage(outputBytes))
	if err != nil {
		logger.WarnCF("agent", "Failed to write dump_all file",
			map[string]any{"agent_id": agent.ID, "error": err.Error()})
		return
	}
	logger.DebugCF("agent", "LLM response dump written (dump_all)",
		map[string]any{
			"agent_id":      agent.ID,
			"model":         model,
			"session_key":   opts.SessionKey,
			"finish_reason": response.FinishReason,
			"iteration":     iteration,
			"dump_base":     basename,
		})
}
