package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/logger"
)

// GeminiCliProvider implements LLMProvider using the gemini CLI as a subprocess.
type GeminiCliProvider struct {
	command   string
	workspace string
	timeout   time.Duration
	extraArgs []string
	env       map[string]string
}

// NewGeminiCliProvider creates a new Gemini CLI provider.
// When command is empty, it defaults to "gemini".
func NewGeminiCliProvider(command, workspace string, extraArgs []string, env map[string]string) *GeminiCliProvider {
	if command == "" {
		command = "gemini"
	}
	return &GeminiCliProvider{
		command:   command,
		workspace: workspace,
		extraArgs: extraArgs,
		env:       env,
	}
}

// NewGeminiCliProviderWithTimeout creates a new Gemini CLI provider with a request timeout.
// When command is empty, it defaults to "gemini".
func NewGeminiCliProviderWithTimeout(command, workspace string, timeout time.Duration, extraArgs []string, env map[string]string) *GeminiCliProvider {
	if command == "" {
		command = "gemini"
	}
	return &GeminiCliProvider{
		command:   command,
		workspace: workspace,
		timeout:   timeout,
		extraArgs: extraArgs,
		env:       env,
	}
}

// Chat implements LLMProvider.Chat by executing the gemini CLI.
func (p *GeminiCliProvider) Chat(
	ctx context.Context, messages []Message, tools []ToolDefinition, model string, options map[string]any,
) (*LLMResponse, error) {
	if p.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.timeout)
		defer cancel()
	}

	// CLI providers run their own internal agentic loop and return one final
	// answer per invocation. The `tools` parameter is intentionally ignored:
	// the CLI cannot use claw's host-side tools by writing JSON in its prose
	// (that pattern caused infinite outer loops). Use the MCP server in
	// pkg/mcpserver to expose claw tools to the CLI natively.
	_ = tools
	prompt := p.buildPrompt(messages)

	// --prompt "" triggers non-interactive stdin mode; the empty string is appended to stdin input.
	args := []string{"--output-format", "json", "--prompt", ""}
	args = append(args, p.extraArgs...)
	if model != "" && model != "gemini-cli" {
		args = append(args, "--model", model)
	}

	cmd := exec.CommandContext(ctx, p.command, args...)
	if p.workspace != "" {
		cmd.Dir = p.workspace
	}
	cmd.Stdin = bytes.NewReader([]byte(prompt))
	cmd.Env = applyProviderEnv(p.env)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("gemini cli timed out after %s: %w", p.timeout, context.DeadlineExceeded)
		}
		if ctx.Err() == context.Canceled {
			return nil, ctx.Err()
		}

		// Attempt to parse stdout before treating as error — gemini CLI may exit non-zero
		// but still write a valid JSON response to stdout.
		if stdoutStr := strings.TrimSpace(stdout.String()); stdoutStr != "" {
			if resp, parseErr := p.parseGeminiCliResponse(stdoutStr); parseErr == nil && resp.Content != "" {
				exitCode := -1
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) {
					exitCode = exitErr.ExitCode()
				}
				logger.WarnCF("provider", "gemini-cli exited non-zero but returned valid content",
					map[string]any{"exit_code": exitCode})
				return resp, nil
			}
		}

		exitCode := -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
		stderrStr := strings.TrimSpace(stderr.String())
		stdoutStr := strings.TrimSpace(stdout.String())
		fields := map[string]any{
			"agent_id":  AgentIDFromContext(ctx),
			"exit_code": exitCode,
		}
		if logger.GetLogMessageContent() {
			fields["stderr"] = stderrStr
			fields["stdout"] = stdoutStr
		}
		logger.ErrorCF("provider", "gemini-cli subprocess failed", fields)
		switch {
		case stderrStr != "" && stdoutStr != "":
			return nil, fmt.Errorf("gemini cli error: %w\nstderr: %s\nstdout: %s", err, stderrStr, stdoutStr)
		case stderrStr != "":
			return nil, fmt.Errorf("gemini cli error: %s", stderrStr)
		case stdoutStr != "":
			return nil, fmt.Errorf("gemini cli error: %w\noutput: %s", err, stdoutStr)
		default:
			return nil, fmt.Errorf("gemini cli error: %w", err)
		}
	}

	// Log non-empty stderr on successful exit.
	if stderrStr := strings.TrimSpace(stderr.String()); stderrStr != "" {
		logger.WarnCF("provider", "gemini-cli wrote to stderr on successful exit",
			map[string]any{"stderr": stderrStr})
	}

	resp, err := p.parseGeminiCliResponse(stdout.String())
	if err != nil {
		return nil, err
	}
	if resp.Content == "" {
		warnFields := map[string]any{}
		if logger.GetLogMessageContent() {
			warnFields["raw_stdout"] = strings.TrimSpace(stdout.String())
		}
		logger.WarnCF("provider", "gemini-cli returned empty content", warnFields)
	}
	return resp, nil
}

// GetDefaultModel returns the default model identifier.
func (p *GeminiCliProvider) GetDefaultModel() string {
	return "gemini-cli"
}

// IsCLI implements CLIProvider. CLI providers invoke a subprocess and do not
// accept HTTP request parameters such as temperature.
func (p *GeminiCliProvider) IsCLI() bool { return true }

// buildPrompt converts messages to a prompt string for the Gemini CLI.
// System messages are prepended as instructions since Gemini CLI has no --system-prompt flag.
// Tool definitions are intentionally not included — see Chat().
func (p *GeminiCliProvider) buildPrompt(messages []Message) string {
	var systemParts []string
	var conversationParts []string

	for _, msg := range messages {
		switch msg.Role {
		case "system":
			systemParts = append(systemParts, msg.Content)
		case "user":
			conversationParts = append(conversationParts, "User: "+msg.Content)
		case "assistant":
			conversationParts = append(conversationParts, "Assistant: "+msg.Content)
		case "tool":
			conversationParts = append(conversationParts,
				fmt.Sprintf("[Tool Result for %s]: %s", msg.ToolCallID, msg.Content))
		}
	}

	var sb strings.Builder

	if len(systemParts) > 0 {
		sb.WriteString("## System Instructions\n\n")
		sb.WriteString(strings.Join(systemParts, "\n\n"))
		sb.WriteString("\n\n## Task\n\n")
	}

	// Simplify single user message (no prefix) when there is no system context
	if len(conversationParts) == 1 && len(systemParts) == 0 {
		return strings.TrimPrefix(conversationParts[0], "User: ")
	}

	sb.WriteString(strings.Join(conversationParts, "\n"))
	return sb.String()
}

// parseGeminiCliResponse parses the JSON output from the gemini CLI.
func (p *GeminiCliProvider) parseGeminiCliResponse(output string) (*LLMResponse, error) {
	var resp geminiCliJSONResponse
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		return nil, fmt.Errorf("failed to parse gemini cli response: %w", err)
	}

	// CLI is itself agentic — its `response` is the final assistant text.
	// We do NOT extract tool calls: the agent loop must treat each CLI
	// invocation as one complete round.
	content := resp.Response

	var usage *UsageInfo
	if resp.Stats.Models != nil {
		var totalInput, totalCandidates, totalAll int
		for _, m := range resp.Stats.Models {
			totalInput += m.Tokens.Input
			totalCandidates += m.Tokens.Candidates
			totalAll += m.Tokens.Total
		}
		if totalInput > 0 || totalCandidates > 0 || totalAll > 0 {
			usage = &UsageInfo{
				PromptTokens:     totalInput,
				CompletionTokens: totalCandidates,
				TotalTokens:      totalAll,
			}
		}
	}

	result := &LLMResponse{
		Content:      strings.TrimSpace(content),
		FinishReason: "stop",
		Usage:        usage,
	}

	logFields := map[string]any{
		"content_chars": len(strings.TrimSpace(content)),
	}
	if usage != nil {
		logFields["prompt_tokens"] = usage.PromptTokens
		logFields["completion_tokens"] = usage.CompletionTokens
		logFields["total_tokens"] = usage.TotalTokens
	}
	logger.InfoCF("provider", "gemini-cli response", logFields)

	return result, nil
}

// geminiCliJSONResponse represents the JSON output from the gemini CLI.
type geminiCliJSONResponse struct {
	SessionID string              `json:"session_id"`
	Response  string              `json:"response"`
	Stats     geminiCliStatsBlock `json:"stats"`
}

// geminiCliStatsBlock holds the stats section of the gemini CLI response.
type geminiCliStatsBlock struct {
	Models map[string]geminiCliModelStats `json:"models"`
}

// geminiCliModelStats holds token usage for a single model in the stats block.
type geminiCliModelStats struct {
	Tokens geminiCliTokens `json:"tokens"`
}

// geminiCliTokens holds the token counts for a model.
type geminiCliTokens struct {
	Input      int `json:"input"`
	Candidates int `json:"candidates"`
	Total      int `json:"total"`
}
