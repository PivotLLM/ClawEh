package openai_compat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/providers/common"
	"github.com/PivotLLM/ClawEh/pkg/providers/protocoltypes"
)

// warnFn is the logger hook the provider uses for non-fatal warnings (e.g.
// extra_body merge collisions). Tests swap this for a capturing function.
var warnFn = logger.WarnCF

type (
	ToolCall               = protocoltypes.ToolCall
	FunctionCall           = protocoltypes.FunctionCall
	LLMResponse            = protocoltypes.LLMResponse
	UsageInfo              = protocoltypes.UsageInfo
	DispatchStatus         = protocoltypes.DispatchStatus
	Message                = protocoltypes.Message
	ToolDefinition         = protocoltypes.ToolDefinition
	ToolFunctionDefinition = protocoltypes.ToolFunctionDefinition
	ExtraContent           = protocoltypes.ExtraContent
	GoogleExtra            = protocoltypes.GoogleExtra
	ReasoningDetail        = protocoltypes.ReasoningDetail
)

type Provider struct {
	apiKey              string
	apiBase             string
	maxTokensField      string         // Field name for max tokens (e.g., "max_completion_tokens" for o1/glm models)
	strictCompat        bool           // Strip non-standard fields for strict OpenAI-compatible endpoints
	noParallelToolCalls bool           // Send parallel_tool_calls=false (required by Groq/some Llama providers)
	responseLogFile     string         // Append raw response bodies here when non-empty (diagnostic only)
	reasoningEffort     string         // OpenAI/Grok `reasoning_effort` request field; empty omits it
	extraBody           map[string]any // Free-form passthrough merged into the request body before marshal
	modelLabel          string         // User-facing model name used in WarnCF for merge collisions; empty falls back to wire model
	httpClient          *http.Client

	logMu      sync.Mutex // serialises appends to responseLogFile across goroutines sharing this Provider
	logErrOnce sync.Once  // emits at most one stderr warning per Provider lifetime on log write failure
}

type Option func(*Provider)

const defaultRequestTimeout = common.DefaultRequestTimeout

func WithMaxTokensField(maxTokensField string) Option {
	return func(p *Provider) {
		p.maxTokensField = maxTokensField
	}
}

func WithRequestTimeout(timeout time.Duration) Option {
	return func(p *Provider) {
		if timeout > 0 {
			p.httpClient.Timeout = timeout
		}
	}
}

func WithStrictCompat(v bool) Option {
	return func(p *Provider) {
		p.strictCompat = v
	}
}

func WithNoParallelToolCalls(v bool) Option {
	return func(p *Provider) {
		p.noParallelToolCalls = v
	}
}

// WithResponseLogFile enables append-only raw response capture to the given
// path. Empty disables it (the default). Diagnostic feature; see
// (*Provider).appendResponseLog for the record format.
func WithResponseLogFile(path string) Option {
	return func(p *Provider) {
		p.responseLogFile = path
	}
}

// WithReasoningEffort sets the `reasoning_effort` request field that some
// providers (notably xAI Grok and OpenAI o-series) honour. Empty omits the
// field. Validated upstream in pkg/config; the provider trusts what it gets.
func WithReasoningEffort(level string) Option {
	return func(p *Provider) {
		p.reasoningEffort = level
	}
}

// WithExtraBody supplies a free-form map merged into the JSON request body
// just before marshal. Used as the per-model passthrough for provider-specific
// knobs claw does not model natively. Collisions with claw-managed fields are
// rejected at config load; the merge step here is purely defensive.
func WithExtraBody(extra map[string]any) Option {
	return func(p *Provider) {
		p.extraBody = extra
	}
}

// WithModelLabel records the user-facing model name (ModelConfig.ModelName)
// so log lines about this provider can identify the offending entry. Optional;
// when unset, logs fall back to the wire-format model identifier.
func WithModelLabel(label string) Option {
	return func(p *Provider) {
		p.modelLabel = label
	}
}

func NewProvider(apiKey, apiBase, proxy string, opts ...Option) *Provider {
	p := &Provider{
		apiKey:     apiKey,
		apiBase:    strings.TrimRight(apiBase, "/"),
		httpClient: common.NewHTTPClient(proxy),
	}

	for _, opt := range opts {
		if opt != nil {
			opt(p)
		}
	}

	return p
}

func NewProviderWithMaxTokensField(apiKey, apiBase, proxy, maxTokensField string) *Provider {
	return NewProvider(apiKey, apiBase, proxy, WithMaxTokensField(maxTokensField))
}

func NewProviderWithMaxTokensFieldAndTimeout(
	apiKey, apiBase, proxy, maxTokensField string,
	requestTimeoutSeconds int,
) *Provider {
	return NewProvider(
		apiKey,
		apiBase,
		proxy,
		WithMaxTokensField(maxTokensField),
		WithRequestTimeout(time.Duration(requestTimeoutSeconds)*time.Second),
	)
}

func (p *Provider) Chat(
	ctx context.Context,
	messages []Message,
	tools []ToolDefinition,
	model string,
	options map[string]any,
) (*LLMResponse, error) {
	if p.apiBase == "" {
		return nil, fmt.Errorf("API base not configured")
	}

	model = normalizeModel(model, p.apiBase)

	requestBody := map[string]any{
		"model":    model,
		"messages": serializeMessages(messages, p.strictCompat),
	}

	if len(tools) > 0 {
		requestBody["tools"] = tools
		requestBody["tool_choice"] = "auto"
		if p.noParallelToolCalls {
			requestBody["parallel_tool_calls"] = false
		}
		// OpenRouter routing hint: require an upstream that supports the
		// `parameters` field on tool definitions, i.e. one that can actually
		// honour function calling. Without this, OpenRouter silently down-
		// routes tools-enabled requests to upstreams that drop the tools and
		// produce text-only replies (sometimes with the tool descriptor
		// echoed back inside the content). Non-OpenRouter upstreams ignore
		// unknown top-level fields, so this is safe for every endpoint.
		requestBody["provider"] = map[string]any{
			"require_parameters": true,
		}
	}

	if maxTokens, ok := common.AsInt(options["max_tokens"]); ok {
		// Use configured maxTokensField if specified, otherwise fallback to model-based detection
		fieldName := p.maxTokensField
		if fieldName == "" {
			// Fallback: detect from model name for backward compatibility
			lowerModel := strings.ToLower(model)
			if strings.Contains(lowerModel, "glm") || strings.Contains(lowerModel, "o1") ||
				strings.Contains(lowerModel, "gpt-5") {
				fieldName = "max_completion_tokens"
			} else {
				fieldName = "max_tokens"
			}
		}
		requestBody[fieldName] = maxTokens
	}

	if temperature, ok := common.AsFloat(options["temperature"]); ok {
		lowerModel := strings.ToLower(model)
		// Kimi k2 models only support temperature=1.
		if strings.Contains(lowerModel, "kimi") && strings.Contains(lowerModel, "k2") {
			requestBody["temperature"] = 1.0
		} else {
			requestBody["temperature"] = temperature
		}
	}

	// Prompt caching: pass a stable cache key so OpenAI can bucket requests
	// with the same key and reuse prefix KV cache across calls.
	// The key is typically the agent ID — stable per agent, shared across requests.
	// See: https://platform.openai.com/docs/guides/prompt-caching
	// Prompt caching is only supported by OpenAI-native endpoints.
	// Non-OpenAI providers (Mistral, Gemini, DeepSeek, etc.) reject unknown
	// fields with 422 errors, so only include it for OpenAI APIs.
	if cacheKey, ok := options["prompt_cache_key"].(string); ok && cacheKey != "" {
		if supportsPromptCacheKey(p.apiBase) {
			requestBody["prompt_cache_key"] = cacheKey
		}
	}

	if p.reasoningEffort != "" {
		requestBody["reasoning_effort"] = p.reasoningEffort
	}

	// Merge extra_body last so any forgotten claw-managed field still wins.
	// The config-load collision guard ensures this loop should be a no-op for
	// reserved keys; the defensive check below logs and skips if one slips
	// through (e.g. a key claw added after the config validator was last
	// updated).
	for k, v := range p.extraBody {
		if _, clash := requestBody[k]; clash {
			warnFn("openai_compat", "extra_body key collides with claw-managed request field; skipping",
				map[string]any{
					"model": p.modelLabelOr(model),
					"key":   k,
				})
			continue
		}
		requestBody[k] = v
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", p.apiBase+"/chat/completions", bytes.NewReader(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	bytesSent := int64(len(jsonData))
	start := time.Now()
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return httpErrorStatus(model, "error", time.Since(start).Milliseconds(), bytesSent, 0),
			fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	// When response logging is enabled, slurp the full body up front so we can
	// append it to the diagnostic file before handing it off to the existing
	// error/parse helpers. The body is then replaced with an in-memory reader
	// so downstream code is unchanged.
	if p.responseLogFile != "" {
		raw, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return httpErrorStatus(model, "error", time.Since(start).Milliseconds(), bytesSent, int64(len(raw))),
				fmt.Errorf("failed to read response body: %w", readErr)
		}
		p.appendResponseLog(req.URL.String(), model, resp.StatusCode, raw)
		resp.Body = io.NopCloser(bytes.NewReader(raw))
	}

	if resp.StatusCode != http.StatusOK {
		respErr := common.HandleErrorResponse(resp, p.apiBase)
		return httpErrorStatus(model, "error", time.Since(start).Milliseconds(), bytesSent, 0), respErr
	}

	out, bytesReceived, err := common.ReadParseAndMeasure(resp, p.apiBase, common.ToolNameSet(tools))
	durationMs := time.Since(start).Milliseconds()
	if err != nil {
		return httpErrorStatus(model, "parse_error", durationMs, bytesSent, bytesReceived), err
	}
	if out.Status != nil {
		out.Status.Success = true
		out.Status.DurationMs = durationMs
		out.Status.BytesSent = bytesSent
		out.Status.BytesReceived = bytesReceived
		if out.Status.Model == "" {
			out.Status.Model = model
		}
	}
	return out, nil
}

// httpErrorStatus builds an LLMResponse whose Status records a failed HTTP dispatch.
func httpErrorStatus(model, stopReason string, durationMs, bytesSent, bytesReceived int64) *LLMResponse {
	return &LLMResponse{
		Status: &DispatchStatus{
			Success:       false,
			Model:         model,
			StopReason:    stopReason,
			DurationMs:    durationMs,
			BytesSent:     bytesSent,
			BytesReceived: bytesReceived,
		},
	}
}

// openaiMessage is the wire-format message for OpenAI-compatible APIs.
// It mirrors protocoltypes.Message but omits SystemParts, which is an
// internal field that would be unknown to third-party endpoints.
type openaiMessage struct {
	Role             string     `json:"role"`
	Content          *string    `json:"content,omitempty"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
}

// msgContent returns the content pointer for an outbound message.
// When content is empty and tool_calls are present, nil is returned so the
// field is omitted entirely. The OpenAI spec allows content to be absent (or
// null) when tool_calls is set, and some strict providers reject "" in that
// position, causing intermittent failures.
func msgContent(content string, toolCalls []ToolCall) *string {
	if content == "" && len(toolCalls) > 0 {
		return nil
	}
	return &content
}

// serializeMessages converts internal Message structs to the OpenAI wire format.
//   - Strips SystemParts (unknown to third-party endpoints)
//   - Converts messages with Media to multipart content format (text + image_url parts)
//   - Preserves ToolCallID, ToolCalls, and ReasoningContent for all messages
//   - When strictCompat is true, strips non-standard fields (reasoning_content, extra_content,
//     thought_signature) that some strict OpenAI-compatible providers reject
func serializeMessages(messages []Message, strictCompat bool) []any {
	out := make([]any, 0, len(messages))
	for _, m := range messages {
		toolCalls := m.ToolCalls
		reasoningContent := m.ReasoningContent

		if strictCompat {
			reasoningContent = ""
			if len(toolCalls) > 0 {
				sanitized := make([]ToolCall, len(toolCalls))
				for i, tc := range toolCalls {
					sanitized[i] = tc
					sanitized[i].ExtraContent = nil
					if tc.Function != nil {
						fnCopy := *tc.Function
						fnCopy.ThoughtSignature = ""
						sanitized[i].Function = &fnCopy
					}
				}
				toolCalls = sanitized
			}
		}

		if len(m.Media) == 0 {
			out = append(out, openaiMessage{
				Role:             m.Role,
				Content:          msgContent(m.Content, toolCalls),
				ReasoningContent: reasoningContent,
				ToolCalls:        toolCalls,
				ToolCallID:       m.ToolCallID,
			})
			continue
		}

		// Multipart content format for messages with media
		parts := make([]map[string]any, 0, 1+len(m.Media))
		if m.Content != "" {
			parts = append(parts, map[string]any{
				"type": "text",
				"text": m.Content,
			})
		}
		for _, mediaURL := range m.Media {
			if strings.HasPrefix(mediaURL, "data:image/") {
				parts = append(parts, map[string]any{
					"type": "image_url",
					"image_url": map[string]any{
						"url": mediaURL,
					},
				})
			}
		}

		msg := map[string]any{
			"role":    m.Role,
			"content": parts,
		}
		if m.ToolCallID != "" {
			msg["tool_call_id"] = m.ToolCallID
		}
		if len(toolCalls) > 0 {
			msg["tool_calls"] = toolCalls
		}
		if reasoningContent != "" {
			msg["reasoning_content"] = reasoningContent
		}
		out = append(out, msg)
	}
	return out
}

func normalizeModel(model, apiBase string) string {
	before, after, ok := strings.Cut(model, "/")
	if !ok {
		return model
	}

	if strings.Contains(strings.ToLower(apiBase), "openrouter.ai") {
		return model
	}

	prefix := strings.ToLower(before)
	switch prefix {
	case "litellm", "moonshot", "nvidia", "groq", "ollama", "deepseek", "google",
		"openrouter", "mistral":
		return after
	default:
		return model
	}
}

// appendResponseLog writes one record describing an HTTP response to the
// configured response_log_file. Each record is:
//
//	=== <ISO-8601 ts> status=<code> model=<model> url=<url> ===\n
//	<raw body — exact bytes received, no trimming>
//	\n---END---\n
//
// A trailing newline is inserted before the separator only when the body does
// not already end in '\n', so single-line JSON responses stay readable and the
// separator always sits on its own line.
//
// Failures are swallowed: the request must complete regardless of whether the
// diagnostic file is writable. One stderr warning is emitted per Provider
// lifetime (via sync.Once) to surface persistent misconfiguration without
// flooding the log under sustained failure.
func (p *Provider) appendResponseLog(reqURL, model string, status int, body []byte) {
	if p.responseLogFile == "" {
		return
	}

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "=== %s status=%d model=%s url=%s ===\n",
		time.Now().Format(time.RFC3339), status, model, reqURL)
	buf.Write(body)
	if len(body) == 0 || body[len(body)-1] != '\n' {
		buf.WriteByte('\n')
	}
	buf.WriteString("---END---\n")

	p.logMu.Lock()
	defer p.logMu.Unlock()

	f, err := os.OpenFile(p.responseLogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		p.logErrOnce.Do(func() {
			log.Printf("openai_compat: response_log_file %q open failed: %v (further failures suppressed)",
				p.responseLogFile, err)
		})
		return
	}
	defer f.Close()
	if _, err := f.Write(buf.Bytes()); err != nil {
		p.logErrOnce.Do(func() {
			log.Printf("openai_compat: response_log_file %q write failed: %v (further failures suppressed)",
				p.responseLogFile, err)
		})
	}
}

// modelLabelOr returns the configured user-facing model label, or fallback
// when the label is empty. Keeps WarnCF messages identifying the offending
// model_list entry rather than the bare wire identifier.
func (p *Provider) modelLabelOr(fallback string) string {
	if p.modelLabel != "" {
		return p.modelLabel
	}
	return fallback
}

// supportsPromptCacheKey reports whether the given API base is known to
// support the prompt_cache_key request field. Currently only OpenAI's own
// API and Azure OpenAI support this. All other OpenAI-compatible providers
// (Mistral, Gemini, DeepSeek, Groq, etc.) reject unknown fields with 422 errors.
func supportsPromptCacheKey(apiBase string) bool {
	u, err := url.Parse(apiBase)
	if err != nil {
		return false
	}
	host := u.Hostname()
	return host == "api.openai.com" || strings.HasSuffix(host, ".openai.azure.com")
}
