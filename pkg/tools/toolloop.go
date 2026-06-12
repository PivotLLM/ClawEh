// ClawEh - Personal AI Assistant
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// ToolLoopConfig configures the tool execution loop.
type ToolLoopConfig struct {
	Provider      providers.LLMProvider
	Model         string
	Tools         *ToolRegistry
	MaxIterations int
	LLMOptions    map[string]any

	// Per-candidate dispatch: when Dispatcher and Candidates are set, RunToolLoop
	// selects the provider for each LLM call through the dispatcher using the
	// candidate list, falling back through candidates on error via Fallback.
	// If Fallback is nil, only the first candidate is tried.
	Dispatcher *providers.ProviderDispatcher
	Candidates []providers.FallbackCandidate
	Fallback   *providers.FallbackChain
}

// ToolLoopResult contains the result of running the tool loop.
type ToolLoopResult struct {
	Content    string
	Iterations int
}

// filterOptsForProvider returns a copy of opts with temperature removed when the
// provider is CLI-based. CLI providers (claude-cli, codex-cli, gemini-cli) invoke a
// subprocess and do not accept HTTP request parameters such as temperature.
func filterOptsForProvider(p providers.LLMProvider, opts map[string]any) map[string]any {
	if _, isCLI := p.(providers.CLIProvider); !isCLI {
		return opts
	}
	if _, hasTemp := opts["temperature"]; !hasTemp {
		return opts
	}
	filtered := make(map[string]any, len(opts))
	for k, v := range opts {
		if k != "temperature" {
			filtered[k] = v
		}
	}
	return filtered
}

// RunToolLoop executes the LLM + tool call iteration loop.
// This is the core agent logic that can be reused by both main agent and subagents.
func RunToolLoop(
	ctx context.Context,
	config ToolLoopConfig,
	messages []providers.Message,
	channel, chatID string,
) (*ToolLoopResult, error) {
	iteration := 0
	var finalContent string

	for iteration < config.MaxIterations {
		iteration++

		logger.DebugCF("toolloop", "LLM iteration",
			map[string]any{
				"iteration": iteration,
				"max":       config.MaxIterations,
			})

		// 1. Build tool definitions
		var providerToolDefs []providers.ToolDefinition
		if config.Tools != nil {
			providerToolDefs = config.Tools.ToProviderDefs()
		}

		// 2. Set default LLM options
		llmOpts := config.LLMOptions
		if llmOpts == nil {
			llmOpts = map[string]any{}
		}
		// 3. Call LLM — use per-candidate dispatch when configured, else fall back
		// to the plain Provider/Model pair.
		var response *providers.LLMResponse
		var err error

		dispatchProvider := ""
		dispatchModel := config.Model
		if len(config.Candidates) > 0 {
			dispatchProvider = config.Candidates[0].Provider
			dispatchModel = config.Candidates[0].Model
		}

		logger.InfoCF("toolloop", "LLM dispatch", map[string]any{
			"agent_id":     channel,
			"iteration":    iteration,
			"provider":     dispatchProvider,
			"model":        dispatchModel,
			"num_messages": len(messages),
			"num_tools":    len(providerToolDefs),
		})
		dispatchStart := time.Now()

		if config.Dispatcher != nil && len(config.Candidates) > 0 {
			if config.Fallback != nil && len(config.Candidates) > 1 {
				fbResult, fbErr := config.Fallback.Execute(
					ctx,
					config.Candidates,
					func(ctx context.Context, c providers.FallbackCandidate) (*providers.LLMResponse, error) {
						key := c.Alias
						if key == "" {
							key = c.Provider + "/" + c.Model
						}
						if p, perr := config.Dispatcher.Get(key); perr == nil {
							return p.Chat(ctx, messages, providerToolDefs, c.Model, filterOptsForProvider(p, llmOpts))
						}
						return config.Provider.Chat(ctx, messages, providerToolDefs, c.Model, filterOptsForProvider(config.Provider, llmOpts))
					},
				)
				if fbErr != nil {
					err = fbErr
				} else {
					response = fbResult.Response
					if fbResult.Provider != "" {
						dispatchProvider = fbResult.Provider
					}
				}
			} else {
				first := config.Candidates[0]
				key := first.Alias
				if key == "" {
					key = first.Provider + "/" + first.Model
				}
				if p, perr := config.Dispatcher.Get(key); perr == nil {
					response, err = p.Chat(ctx, messages, providerToolDefs, first.Model, filterOptsForProvider(p, llmOpts))
				} else {
					response, err = config.Provider.Chat(ctx, messages, providerToolDefs, first.Model, filterOptsForProvider(config.Provider, llmOpts))
				}
			}
		} else {
			response, err = config.Provider.Chat(ctx, messages, providerToolDefs, config.Model, filterOptsForProvider(config.Provider, llmOpts))
		}

		emitToolLoopFinishEvent(channel, iteration, dispatchProvider, dispatchModel, dispatchStart, response, err)

		if err != nil {
			logger.ErrorCF("toolloop", "LLM call failed",
				map[string]any{
					"iteration": iteration,
					"error":     err.Error(),
				})
			return nil, fmt.Errorf("LLM call failed: %w", err)
		}

		// 4. If no tool calls, we're done
		if len(response.ToolCalls) == 0 {
			finalContent = response.Content
			logger.InfoCF("toolloop", "LLM response without tool calls (direct answer)",
				map[string]any{
					"iteration":     iteration,
					"content_chars": len(finalContent),
				})
			break
		}

		normalizedToolCalls := make([]providers.ToolCall, 0, len(response.ToolCalls))
		for _, tc := range response.ToolCalls {
			normalizedToolCalls = append(normalizedToolCalls, providers.NormalizeToolCall(tc))
		}

		// 5. Log tool calls
		toolNames := make([]string, 0, len(normalizedToolCalls))
		for _, tc := range normalizedToolCalls {
			toolNames = append(toolNames, tc.Name)
		}
		logger.InfoCF("toolloop", "LLM requested tool calls",
			map[string]any{
				"tools":     toolNames,
				"count":     len(normalizedToolCalls),
				"iteration": iteration,
			})

		// 6. Build assistant message with tool calls
		assistantMsg := providers.Message{
			Role:    "assistant",
			Content: response.Content,
		}
		for _, tc := range normalizedToolCalls {
			argumentsJSON, _ := json.Marshal(tc.Arguments)
			assistantMsg.ToolCalls = append(assistantMsg.ToolCalls, providers.ToolCall{
				ID:        tc.ID,
				Type:      "function",
				Name:      tc.Name,
				Arguments: tc.Arguments,
				Function: &providers.FunctionCall{
					Name:      tc.Name,
					Arguments: string(argumentsJSON),
				},
			})
		}
		messages = append(messages, assistantMsg)

		// 7. Execute tool calls in parallel
		type indexedResult struct {
			result *ToolResult
			tc     providers.ToolCall
		}

		results := make([]indexedResult, len(normalizedToolCalls))
		var wg sync.WaitGroup

		for i, tc := range normalizedToolCalls {
			results[i].tc = tc

			wg.Add(1)
			go func(idx int, tc providers.ToolCall) {
				defer wg.Done()

				redacted := RedactArgs(tc.Name, tc.Arguments)
				logger.InfoCF("toolloop", "Tool call dispatched",
					map[string]any{
						"tool":      tc.Name,
						"iteration": iteration,
						"args":      redacted,
					})
				logger.DebugCF("toolloop", "Tool call dispatched (raw args)",
					map[string]any{
						"tool":      tc.Name,
						"iteration": iteration,
						"args":      tc.Arguments,
					})

				var toolResult *ToolResult
				if config.Tools != nil {
					toolResult = config.Tools.ExecuteWithContext(ctx, tc.Name, tc.Arguments, channel, chatID, nil)
				} else {
					toolResult = ErrorResult("No tools available")
				}
				results[idx].result = toolResult
			}(i, tc)
		}
		wg.Wait()

		// Append results in original order
		for _, r := range results {
			contentForLLM := r.result.ForLLM
			if contentForLLM == "" && r.result.Err != nil {
				contentForLLM = r.result.Err.Error()
			}

			messages = append(messages, providers.Message{
				Role:       "tool",
				Content:    contentForLLM,
				ToolCallID: r.tc.ID,
			})
		}
	}

	return &ToolLoopResult{
		Content:    finalContent,
		Iterations: iteration,
	}, nil
}

// emitToolLoopFinishEvent writes the INFO "LLM finish" log paired with the dispatch event
// for the tool loop. Mirrors emitLLMFinishEvent in the agent loop.
func emitToolLoopFinishEvent(
	agentID string,
	iteration int,
	provider, requestModel string,
	dispatchStart time.Time,
	response *providers.LLMResponse,
	callErr error,
) {
	elapsed := time.Since(dispatchStart).Milliseconds()

	var status *providers.DispatchStatus
	if response != nil {
		status = response.Status
	}
	if status == nil {
		stopReason := "success"
		if callErr != nil {
			stopReason = "error"
		}
		status = &providers.DispatchStatus{
			Success:    callErr == nil,
			Model:      requestModel,
			StopReason: stopReason,
			DurationMs: elapsed,
		}
	}

	model := status.Model
	if model == "" {
		model = requestModel
	}

	fields := map[string]any{
		"agent_id":              agentID,
		"iteration":             iteration,
		"provider":              provider,
		"model":                 model,
		"success":               status.Success && callErr == nil,
		"num_turns":             status.NumTurns,
		"input_tokens":          status.InputTokens,
		"output_tokens":         status.OutputTokens,
		"cache_read_tokens":     status.CacheReadTokens,
		"cache_creation_tokens": status.CacheCreationTokens,
		"stop_reason":           status.StopReason,
		"cost_usd":              status.CostUSD,
		"duration_ms":           status.DurationMs,
		"bytes_sent":            status.BytesSent,
		"bytes_received":        status.BytesReceived,
	}
	if callErr != nil {
		fields["error"] = truncateErrorString(callErr.Error(), 500)
	}
	logger.InfoCF("toolloop", "LLM finish", fields)
}

func truncateErrorString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}
