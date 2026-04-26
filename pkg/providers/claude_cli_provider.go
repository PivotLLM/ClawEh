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

// ClaudeCliProvider implements LLMProvider using the claude CLI as a subprocess.
type ClaudeCliProvider struct {
	command   string
	workspace string
	timeout   time.Duration
	extraArgs []string
	env       map[string]string
}

// NewClaudeCliProvider creates a new Claude CLI provider.
// When command is empty, it defaults to "claude".
func NewClaudeCliProvider(command, workspace string, extraArgs []string, env map[string]string) *ClaudeCliProvider {
	if command == "" {
		command = "claude"
	}
	return &ClaudeCliProvider{
		command:   command,
		workspace: workspace,
		extraArgs: extraArgs,
		env:       env,
	}
}

// NewClaudeCliProviderWithTimeout creates a new Claude CLI provider with a request timeout.
// When command is empty, it defaults to "claude".
func NewClaudeCliProviderWithTimeout(command, workspace string, timeout time.Duration, extraArgs []string, env map[string]string) *ClaudeCliProvider {
	if command == "" {
		command = "claude"
	}
	return &ClaudeCliProvider{
		command:   command,
		workspace: workspace,
		timeout:   timeout,
		extraArgs: extraArgs,
		env:       env,
	}
}

// Chat implements LLMProvider.Chat by executing the claude CLI.
func (p *ClaudeCliProvider) Chat(
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
	prompt := p.buildStdinPrompt(messages)

	args := []string{"-p", "--output-format", "json"}
	args = append(args, p.extraArgs...)
	if model != "" && model != "claude-code" && model != "claude-cli" {
		args = append(args, "--model", model)
	}
	args = append(args, "-") // read from stdin

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
			return nil, fmt.Errorf("claude cli timed out after %s: %w", p.timeout, context.DeadlineExceeded)
		}

		// Attempt to parse stdout before treating as error — claude CLI may exit non-zero
		// but still write a valid JSON response to stdout.
		if stdoutStr := strings.TrimSpace(stdout.String()); stdoutStr != "" {
			if resp, parseErr := p.parseClaudeCliResponse(stdoutStr); parseErr == nil && resp.Content != "" {
				exitCode := -1
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) {
					exitCode = exitErr.ExitCode()
				}
				logger.WarnCF("provider", "claude-cli exited non-zero but returned valid content",
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
		logger.ErrorCF("provider", "claude-cli subprocess failed",
			map[string]any{
				"agent_id":  AgentIDFromContext(ctx),
				"exit_code": exitCode,
				"stderr":    stderrStr,
				"stdout":    stdoutStr,
			})
		switch {
		case stderrStr != "" && stdoutStr != "":
			return nil, fmt.Errorf("claude cli error: %w\nstderr: %s\nstdout: %s", err, stderrStr, stdoutStr)
		case stderrStr != "":
			return nil, fmt.Errorf("claude cli error: %s", stderrStr)
		case stdoutStr != "":
			return nil, fmt.Errorf("claude cli error: %w\noutput: %s", err, stdoutStr)
		default:
			return nil, fmt.Errorf("claude cli error: %w", err)
		}
	}

	// Log non-empty stderr on successful exit.
	if stderrStr := strings.TrimSpace(stderr.String()); stderrStr != "" {
		logger.WarnCF("provider", "claude-cli wrote to stderr on successful exit",
			map[string]any{"stderr": stderrStr})
	}

	resp, err := p.parseClaudeCliResponse(stdout.String())
	if err != nil {
		return nil, err
	}
	if resp.Content == "" {
		logger.WarnCF("provider", "claude-cli returned empty content",
			map[string]any{"raw_stdout": strings.TrimSpace(stdout.String())})
	}
	return resp, nil
}

// GetDefaultModel returns the default model identifier.
func (p *ClaudeCliProvider) GetDefaultModel() string {
	return "claude-code"
}

// IsCLI implements CLIProvider. CLI providers invoke a subprocess and do not
// accept HTTP request parameters such as temperature.
func (p *ClaudeCliProvider) IsCLI() bool { return true }

// buildStdinPrompt combines the system context and conversation into a single stdin payload.
// Passing system instructions via stdin avoids exposing them in the process argument list and
// sidesteps operating-system ARG_MAX limits when many tools are registered.
func (p *ClaudeCliProvider) buildStdinPrompt(messages []Message) string {
	system := p.buildSystemPrompt(messages)
	conversation := p.messagesToPrompt(messages)
	if system == "" {
		return conversation
	}
	return system + "\n\n---\n\n" + conversation
}

// messagesToPrompt converts non-system messages to a CLI-compatible prompt string.
func (p *ClaudeCliProvider) messagesToPrompt(messages []Message) string {
	var parts []string

	for _, msg := range messages {
		switch msg.Role {
		case "system":
			// included in system context block; see buildStdinPrompt
		case "user":
			parts = append(parts, "User: "+msg.Content)
		case "assistant":
			parts = append(parts, "Assistant: "+msg.Content)
		case "tool":
			parts = append(parts, fmt.Sprintf("[Tool Result for %s]: %s", msg.ToolCallID, msg.Content))
		}
	}

	// Simplify single user message
	if len(parts) == 1 && strings.HasPrefix(parts[0], "User: ") {
		return strings.TrimPrefix(parts[0], "User: ")
	}

	return strings.Join(parts, "\n")
}

// buildSystemPrompt concatenates system messages.
// Tool definitions are intentionally not included — see Chat().
func (p *ClaudeCliProvider) buildSystemPrompt(messages []Message) string {
	var parts []string

	for _, msg := range messages {
		if msg.Role == "system" {
			parts = append(parts, msg.Content)
		}
	}

	return strings.Join(parts, "\n\n")
}

// parseClaudeCliResponse parses the JSON output from the claude CLI.
func (p *ClaudeCliProvider) parseClaudeCliResponse(output string) (*LLMResponse, error) {
	var resp claudeCliJSONResponse
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		return nil, fmt.Errorf("failed to parse claude cli response: %w", err)
	}

	if resp.IsError {
		return nil, fmt.Errorf("claude cli returned error: %s", resp.Result)
	}

	// CLI is itself agentic — its `result` is the final assistant text.
	// We do NOT extract tool calls from this text: the agent loop must
	// treat each CLI invocation as one complete round.
	content := resp.Result
	finishReason := "stop"

	var usage *UsageInfo
	if resp.Usage.InputTokens > 0 || resp.Usage.OutputTokens > 0 {
		usage = &UsageInfo{
			PromptTokens:     resp.Usage.InputTokens + resp.Usage.CacheCreationInputTokens + resp.Usage.CacheReadInputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.InputTokens + resp.Usage.CacheCreationInputTokens + resp.Usage.CacheReadInputTokens + resp.Usage.OutputTokens,
		}
	}

	result := &LLMResponse{
		Content:      strings.TrimSpace(content),
		FinishReason: finishReason,
		Usage:        usage,
	}

	logger.InfoCF("provider", "claude-cli response",
		map[string]any{
			"subtype":       resp.Subtype,
			"num_turns":     resp.NumTurns,
			"cost_usd":      resp.TotalCostUSD,
			"duration_ms":   resp.DurationMS,
			"content_chars": len(strings.TrimSpace(content)),
		})

	return result, nil
}

// claudeCliJSONResponse represents the JSON output from the claude CLI.
// Matches the real claude CLI v2.x output format.
type claudeCliJSONResponse struct {
	Type         string             `json:"type"`
	Subtype      string             `json:"subtype"`
	IsError      bool               `json:"is_error"`
	Result       string             `json:"result"`
	SessionID    string             `json:"session_id"`
	TotalCostUSD float64            `json:"total_cost_usd"`
	DurationMS   int                `json:"duration_ms"`
	DurationAPI  int                `json:"duration_api_ms"`
	NumTurns     int                `json:"num_turns"`
	Usage        claudeCliUsageInfo `json:"usage"`
}

// claudeCliUsageInfo represents token usage from the claude CLI response.
type claudeCliUsageInfo struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}
