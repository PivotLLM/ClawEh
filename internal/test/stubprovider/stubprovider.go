// ClawEh
// License: MIT

// Package stubprovider is an in-process OpenAI-compatible chat-completions
// server for tests. A Server answers POST <URL>/chat/completions from a
// scripted sequence of Steps (one per request, in order); once the script is
// exhausted it answers with its Default step, and an optional per-request Hook
// can override both. Every request is captured for assertions.
//
// It speaks enough of the protocol for spawnllm's openai_compat provider:
// plain JSON replies, tool calls, SSE streaming when the request asks for it,
// and the failure shapes the fallback chain classifies (429 with Retry-After,
// 5xx, 413, a context_length_exceeded body, 401), plus a hang and a malformed
// body. It never calls out and has no state beyond its script.
package stubprovider

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Request is one captured chat-completions request.
type Request struct {
	Model    string
	Stream   bool
	Messages []Message
	// Tools is the number of tool definitions offered.
	Tools int
	// ResponseFormat is the raw response_format field, nil when absent.
	ResponseFormat json.RawMessage
	Header         http.Header
	Body           []byte
}

// Message is one request message with its content flattened to text.
type Message struct {
	Role    string
	Content string
}

// Text returns every message's content joined by newlines, for substring
// checks on what the model was shown.
func (r Request) Text() string {
	var sb strings.Builder
	for _, m := range r.Messages {
		sb.WriteString(m.Content)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// Step is one scripted response.
type Step struct {
	// Status is the HTTP status; 0 means 200.
	Status int
	// Body is sent verbatim for non-200 statuses and for Malformed. Ignored
	// for a 200 built from Content/ToolCalls.
	Body string
	// Header entries are added to the response.
	Header map[string]string
	// Content is the assistant text of a 200 reply.
	Content string
	// ToolCalls, when non-empty, make the 200 reply a tool-call turn.
	ToolCalls []ToolCall
	// Hang blocks until the request context ends or HangFor elapses, then
	// returns 503. HangFor 0 means wait only for the context.
	Hang    bool
	HangFor time.Duration
	// Malformed sends Body (or a default non-JSON body) with status 200.
	Malformed bool
}

// ToolCall is one function call in a tool-call reply.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string // JSON object text; "{}" when empty
}

// Reply is a 200 with the given assistant text.
func Reply(text string) Step { return Step{Content: text} }

// CallTool is a 200 asking for one function call.
func CallTool(name, argsJSON string) Step {
	if argsJSON == "" {
		argsJSON = "{}"
	}
	return Step{ToolCalls: []ToolCall{{ID: "call_" + name, Name: name, Arguments: argsJSON}}}
}

// StatusError is an arbitrary non-200 with a JSON error body.
func StatusError(status int, message string) Step {
	return Step{Status: status, Body: errorBody(message, "server_error", "")}
}

// RateLimited is a 429 carrying Retry-After in whole seconds (rounded up).
func RateLimited(retryAfter time.Duration) Step {
	secs := int((retryAfter + time.Second - 1) / time.Second)
	return Step{
		Status: http.StatusTooManyRequests,
		Body:   errorBody("Rate limit reached for requests", "requests", "rate_limit_exceeded"),
		Header: map[string]string{"Retry-After": strconv.Itoa(secs)},
	}
}

// ServerError is a 5xx (500, 502, 503, …).
func ServerError(status int) Step {
	return Step{Status: status, Body: errorBody("The server had an error processing your request", "server_error", "")}
}

// PayloadTooLarge is a 413, the status the fallback chain treats as a
// context overflow.
func PayloadTooLarge() Step {
	return Step{Status: http.StatusRequestEntityTooLarge, Body: errorBody("Request too large", "invalid_request_error", "")}
}

// ContextLengthExceeded is the OpenAI overflow shape: HTTP 400 with
// code "context_length_exceeded".
func ContextLengthExceeded() Step {
	return Step{
		Status: http.StatusBadRequest,
		Body: errorBody("This model's maximum context length is 8192 tokens. However, your messages resulted in 9000 tokens.",
			"invalid_request_error", "context_length_exceeded"),
	}
}

// Unauthorized is a 401 with an invalid_api_key body.
func Unauthorized() Step {
	return Step{Status: http.StatusUnauthorized, Body: errorBody("Incorrect API key provided", "invalid_request_error", "invalid_api_key")}
}

// Hang never answers: the handler waits for the request context (the client
// giving up) or limit, whichever comes first, then returns 503.
func Hang(limit time.Duration) Step { return Step{Hang: true, HangFor: limit} }

// Malformed is a 200 whose body is not JSON.
func Malformed() Step { return Step{Malformed: true, Body: "<html>not json</html>"} }

// Hook decides a request's response; returning nil falls through to the
// script. It runs on the server goroutine, so it must not block on the test.
type Hook func(req Request) *Step

// Server is one stub provider.
type Server struct {
	srv *httptest.Server

	mu       sync.Mutex
	script   []Step
	def      Step
	hook     Hook
	requests []Request
}

// New starts a stub and stops it when the test ends. It answers Reply("OK")
// until Script or SetDefault says otherwise.
func New(tb testing.TB) *Server {
	tb.Helper()
	s := &Server{def: Reply("OK")}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeString(w, `{"object":"list","data":[{"id":"stub","object":"model"}]}`)
	})
	mux.HandleFunc("/v1/chat/completions", s.handleChat)
	s.srv = httptest.NewServer(mux)
	tb.Cleanup(s.srv.Close)
	return s
}

// writeString writes s to the response. A failure means the client went away
// (a cancelled request, the hang case), which is not the stub's concern; it
// is logged so an unexpected one is still visible in test output.
func writeString(w io.Writer, s string) {
	if _, err := io.WriteString(w, s); err != nil {
		log.Printf("stubprovider: write response: %v", err)
	}
}

// mustJSON marshals a value the stub built itself; failure is a programming
// error in the stub, not a test outcome.
func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic("stubprovider: marshal response: " + err.Error())
	}
	return b
}

// URL is the OpenAI-style base URL (ending in /v1) to put in a provider's
// base_url.
func (s *Server) URL() string { return s.srv.URL + "/v1" }

// Script replaces the queued responses. Each request consumes one step.
func (s *Server) Script(steps ...Step) {
	s.mu.Lock()
	s.script = append([]Step(nil), steps...)
	s.mu.Unlock()
}

// SetDefault sets the response used once the script is exhausted.
func (s *Server) SetDefault(step Step) {
	s.mu.Lock()
	s.def = step
	s.mu.Unlock()
}

// SetHook installs the per-request override.
func (s *Server) SetHook(h Hook) {
	s.mu.Lock()
	s.hook = h
	s.mu.Unlock()
}

// Requests returns a copy of every request seen so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// Count is the number of chat requests seen so far.
func (s *Server) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

// Remaining is how many scripted steps have not been consumed.
func (s *Server) Remaining() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.script)
}

type wireRequest struct {
	Model    string `json:"model"`
	Stream   bool   `json:"stream"`
	Messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
	Tools          []json.RawMessage `json:"tools"`
	ResponseFormat json.RawMessage   `json:"response_format"`
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var wire wireRequest
	if err := json.Unmarshal(body, &wire); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	req := Request{
		Model:          wire.Model,
		Stream:         wire.Stream,
		Tools:          len(wire.Tools),
		ResponseFormat: wire.ResponseFormat,
		Header:         r.Header.Clone(),
		Body:           body,
	}
	for _, m := range wire.Messages {
		req.Messages = append(req.Messages, Message{Role: m.Role, Content: flattenContent(m.Content)})
	}

	step := s.next(req)

	switch {
	case step.Hang:
		wait := step.HangFor
		if wait <= 0 {
			wait = time.Hour
		}
		select {
		case <-r.Context().Done():
		case <-time.After(wait):
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	case step.Malformed:
		w.Header().Set("Content-Type", "application/json")
		writeString(w, step.Body)
		return
	case step.Status != 0 && step.Status != http.StatusOK:
		w.Header().Set("Content-Type", "application/json")
		for k, v := range step.Header {
			w.Header().Set(k, v)
		}
		w.WriteHeader(step.Status)
		writeString(w, step.Body)
		return
	}

	for k, v := range step.Header {
		w.Header().Set(k, v)
	}
	if wire.Stream {
		writeStream(w, wire.Model, step)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	writeString(w, string(mustJSON(completion(wire.Model, step))))
}

// next picks the response for req and records the request.
func (s *Server) next(req Request) Step {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, req)
	if s.hook != nil {
		if st := s.hook(req); st != nil {
			return *st
		}
	}
	if len(s.script) > 0 {
		st := s.script[0]
		s.script = s.script[1:]
		return st
	}
	return s.def
}

func completion(model string, step Step) map[string]any {
	msg := map[string]any{"role": "assistant", "content": step.Content}
	finish := "stop"
	if len(step.ToolCalls) > 0 {
		finish = "tool_calls"
		msg["content"] = nil
		msg["tool_calls"] = wireToolCalls(step.ToolCalls, false)
	}
	return map[string]any{
		"id": "chatcmpl-stub", "object": "chat.completion", "created": time.Now().Unix(), "model": model,
		"choices": []any{map[string]any{"index": 0, "finish_reason": finish, "message": msg}},
		"usage":   map[string]any{"prompt_tokens": 20, "completion_tokens": 5, "total_tokens": 25},
	}
}

func wireToolCalls(calls []ToolCall, indexed bool) []any {
	out := make([]any, 0, len(calls))
	for i, c := range calls {
		args := c.Arguments
		if args == "" {
			args = "{}"
		}
		tc := map[string]any{
			"id": c.ID, "type": "function",
			"function": map[string]any{"name": c.Name, "arguments": args},
		}
		if indexed {
			tc["index"] = i
		}
		out = append(out, tc)
	}
	return out
}

func writeStream(w http.ResponseWriter, model string, step Step) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	fl, canFlush := w.(http.Flusher)
	event := func(b []byte) {
		writeString(w, fmt.Sprintf("data: %s\n\n", b))
		if canFlush {
			fl.Flush()
		}
	}
	chunk := func(delta map[string]any, finish any) {
		event(mustJSON(map[string]any{
			"id": "chatcmpl-stub", "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": model,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
		}))
	}
	if len(step.ToolCalls) > 0 {
		chunk(map[string]any{"role": "assistant", "tool_calls": wireToolCalls(step.ToolCalls, true)}, nil)
		chunk(map[string]any{}, "tool_calls")
	} else {
		chunk(map[string]any{"role": "assistant", "content": step.Content}, nil)
		chunk(map[string]any{}, "stop")
	}
	event(mustJSON(map[string]any{
		"id": "chatcmpl-stub", "object": "chat.completion.chunk", "model": model, "choices": []any{},
		"usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 5, "total_tokens": 25},
	}))
	writeString(w, "data: [DONE]\n\n")
	if canFlush {
		fl.Flush()
	}
}

func errorBody(message, typ, code string) string {
	e := map[string]any{"message": message, "type": typ}
	if code != "" {
		e["code"] = code
	}
	return string(mustJSON(map[string]any{"error": e}))
}

// flattenContent renders a message content field (a string or an array of
// typed parts) as text.
func flattenContent(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var sb strings.Builder
		for _, p := range parts {
			if p.Text != "" {
				sb.WriteString(p.Text)
			} else if p.Type != "" {
				sb.WriteString("[" + p.Type + "]")
			}
		}
		return sb.String()
	}
	return string(raw)
}
