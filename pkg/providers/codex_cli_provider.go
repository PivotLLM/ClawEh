package providers

import (
	"bufio"
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

// CodexCliProvider implements LLMProvider by wrapping the codex CLI as a subprocess.
type CodexCliProvider struct {
	command   string
	workspace string
	timeout   time.Duration
	extraArgs []string
	env       map[string]string
}

// NewCodexCliProvider creates a new Codex CLI provider.
// When command is empty, it defaults to "codex".
func NewCodexCliProvider(command, workspace string, extraArgs []string, env map[string]string) *CodexCliProvider {
	if command == "" {
		command = "codex"
	}
	return &CodexCliProvider{
		command:   command,
		workspace: workspace,
		extraArgs: extraArgs,
		env:       env,
	}
}

// NewCodexCliProviderWithTimeout creates a new Codex CLI provider with a request timeout.
// When command is empty, it defaults to "codex".
func NewCodexCliProviderWithTimeout(command, workspace string, timeout time.Duration, extraArgs []string, env map[string]string) *CodexCliProvider {
	if command == "" {
		command = "codex"
	}
	return &CodexCliProvider{
		command:   command,
		workspace: workspace,
		timeout:   timeout,
		extraArgs: extraArgs,
		env:       env,
	}
}

// Chat implements LLMProvider.Chat by executing the codex CLI in non-interactive mode.
func (p *CodexCliProvider) Chat(
	ctx context.Context, messages []Message, tools []ToolDefinition, model string, options map[string]any,
) (*LLMResponse, error) {
	if p.command == "" {
		return nil, fmt.Errorf("codex command not configured")
	}

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

	args := []string{"exec", "--json", "--color", "never"}
	args = append(args, p.extraArgs...)
	if model != "" && model != "codex-cli" {
		args = append(args, "-m", model)
	}
	if p.workspace != "" {
		args = append(args, "-C", p.workspace)
	}
	args = append(args, "-") // read prompt from stdin

	cmd := exec.CommandContext(ctx, p.command, args...)
	cmd.Stdin = bytes.NewReader([]byte(prompt))
	cmd.Env = applyProviderEnv(p.env)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	// Parse JSONL from stdout even if exit code is non-zero,
	// because codex writes diagnostic noise to stderr (e.g. rollout errors)
	// but still produces valid JSONL output.
	if stdoutStr := stdout.String(); stdoutStr != "" {
		resp, parseErr := p.parseJSONLEvents(stdoutStr)
		if parseErr == nil && resp != nil && resp.Content != "" {
			return resp, nil
		}
	}

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("codex cli timed out after %s: %w", p.timeout, context.DeadlineExceeded)
		}
		if ctx.Err() == context.Canceled {
			return nil, ctx.Err()
		}
		exitCode := -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
		stderrStr := strings.TrimSpace(stderr.String())
		logger.ErrorCF("provider", "codex-cli subprocess failed",
			map[string]any{
				"agent_id":  AgentIDFromContext(ctx),
				"exit_code": exitCode,
				"stderr":    stderrStr,
			})
		if stderrStr != "" {
			return nil, fmt.Errorf("codex cli error: %s", stderrStr)
		}
		return nil, fmt.Errorf("codex cli error: %w", err)
	}

	return p.parseJSONLEvents(stdout.String())
}

// GetDefaultModel returns the default model identifier.
func (p *CodexCliProvider) GetDefaultModel() string {
	return "codex-cli"
}

// IsCLI implements CLIProvider. CLI providers invoke a subprocess and do not
// accept HTTP request parameters such as temperature.
func (p *CodexCliProvider) IsCLI() bool { return true }

// buildPrompt converts messages to a prompt string for the Codex CLI.
// System messages are prepended as instructions since Codex CLI has no --system-prompt flag.
// Tool definitions are intentionally not included — see Chat().
func (p *CodexCliProvider) buildPrompt(messages []Message) string {
	var systemParts []string
	var conversationParts []string

	for _, msg := range messages {
		switch msg.Role {
		case "system":
			systemParts = append(systemParts, msg.Content)
		case "user":
			conversationParts = append(conversationParts, msg.Content)
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

	// Simplify single user message (no prefix)
	if len(conversationParts) == 1 && len(systemParts) == 0 {
		return conversationParts[0]
	}

	sb.WriteString(strings.Join(conversationParts, "\n"))
	return sb.String()
}

// codexEvent represents a single JSONL event from `codex exec --json`.
type codexEvent struct {
	Type     string          `json:"type"`
	ThreadID string          `json:"thread_id,omitempty"`
	Message  string          `json:"message,omitempty"`
	Item     *codexEventItem `json:"item,omitempty"`
	Usage    *codexUsage     `json:"usage,omitempty"`
	Error    *codexEventErr  `json:"error,omitempty"`
}

type codexEventItem struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Command  string `json:"command,omitempty"`
	Status   string `json:"status,omitempty"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Output   string `json:"output,omitempty"`
}

type codexUsage struct {
	InputTokens       int `json:"input_tokens"`
	CachedInputTokens int `json:"cached_input_tokens"`
	OutputTokens      int `json:"output_tokens"`
}

type codexEventErr struct {
	Message string `json:"message"`
}

// parseJSONLEvents processes the JSONL output from codex exec --json.
func (p *CodexCliProvider) parseJSONLEvents(output string) (*LLMResponse, error) {
	var contentParts []string
	var usage *UsageInfo
	var lastError string

	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var event codexEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue // skip malformed lines
		}

		switch event.Type {
		case "item.completed":
			if event.Item != nil && event.Item.Type == "agent_message" && event.Item.Text != "" {
				contentParts = append(contentParts, event.Item.Text)
			}
		case "turn.completed":
			if event.Usage != nil {
				promptTokens := event.Usage.InputTokens + event.Usage.CachedInputTokens
				usage = &UsageInfo{
					PromptTokens:     promptTokens,
					CompletionTokens: event.Usage.OutputTokens,
					TotalTokens:      promptTokens + event.Usage.OutputTokens,
				}
			}
		case "error":
			lastError = event.Message
		case "turn.failed":
			if event.Error != nil {
				lastError = event.Error.Message
			}
		}
	}

	if lastError != "" && len(contentParts) == 0 {
		return nil, fmt.Errorf("codex cli: %s", lastError)
	}

	// CLI is itself agentic — its output is the final assistant text.
	// We do NOT extract tool calls: the agent loop must treat each CLI
	// invocation as one complete round.
	content := strings.Join(contentParts, "\n")

	return &LLMResponse{
		Content:      strings.TrimSpace(content),
		FinishReason: "stop",
		Usage:        usage,
	}, nil
}
