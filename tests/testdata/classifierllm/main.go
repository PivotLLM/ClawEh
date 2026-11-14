// ClawEh
// License: MIT

// classifierllm is a deterministic OpenAI-compatible chat server for the
// classifier end-to-end test (tests/test_classifier.sh). It never calls a
// tool and picks a category from a marker in the request's user messages:
//
//   - "INVOICE" gets "invoice";
//   - "NEWSLETTER" gets "newsletter";
//   - "GIBBERISH" gets a reply that names no category (the item ends up
//     unclassified after the classifier's retry);
//   - anything else gets "other".
//
// Before those, a rule in the system prompt decides: a line "When the item
// says WORD, answer CATEGORY." makes an item holding WORD get CATEGORY (the
// first such rule that matches), so a test can prove that instructions,
// such as a classifier's Markdown rule files, reach the model. Such a reply
// cites the rule on a third line, "Rules: <label>", the label being the
// heading of the section the rule line is in ("## <label>") or "rule N"
// for an inline rule; the other replies cite nothing.
//
// Every request is logged on stdout as one "chat: reply=..." line, so the
// test can count the model calls. Plain and streaming (SSE) responses are
// supported.
//
// It is also a minimal Telegram Bot API server (/bot<token>/<method>), so a
// Telegram channel pointed at it (base_url) is a hermetic chat: getUpdates
// returns nothing, and every sendMessage is logged on stdout as one
// "telegram: chat=<id> text=..." line.
//
// It is also an external MCP server (streamable HTTP at /mcp) with a
// mailbox: "list" returns {"items":[{"id":"x1"}]} and "read" the message
// with that id, whose body says INVOICE. ClawEh marks such results as
// untrusted content for the model; a classifier must read them as JSON.
//
// With -admin-credentials <CLAW_HOME> it instead writes the WebUI admin
// account from CLAW_ADMIN_USER and CLAW_ADMIN_PASSWORD (what `claw install`
// does without a terminal) and exits, so the test can log in to the API.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/PivotLLM/ClawEh/internal/admin"
)

// mailServer is the MCP mailbox: list and read, each answering JSON text.
func mailServer() *server.MCPServer {
	s := server.NewMCPServer("mail", "0.0.1")
	s.AddTool(mcp.NewTool("list", mcp.WithDescription("List the messages")),
		func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			log.Printf("mcp: list")
			return mcp.NewToolResultText(`{"items":[{"id":"x1"}]}`), nil
		})
	s.AddTool(mcp.NewTool("read", mcp.WithDescription("Read one message"), mcp.WithString("id", mcp.Required())),
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			id := req.GetString("id", "")
			log.Printf("mcp: read id=%s", id)
			return mcp.NewToolResultText(fmt.Sprintf(`{"subject":"Bill %s","body":"INVOICE for %s"}`, id, id)), nil
		})
	return s
}

func main() {
	listen := flag.String("listen", "127.0.0.1:0", "address to listen on")
	credentials := flag.String("admin-credentials", "", "write the admin account into this CLAW_HOME and exit")
	flag.Parse()
	log.SetOutput(os.Stdout)

	if *credentials != "" {
		if _, _, err := admin.EnsureAccount(admin.EnsureOptions{Home: *credentials}); err != nil {
			log.Fatalf("admin account: %v", err)
		}
		return
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", handleChat)
	mux.Handle("/mcp", server.NewStreamableHTTPServer(mailServer()))
	mux.HandleFunc("/", handleTelegram) // /bot<token>/<method>
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	log.Printf("listening on %s", ln.Addr().String())
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.Serve(ln))
}

type chatRequest struct {
	Model    string `json:"model"`
	Stream   bool   `json:"stream"`
	Messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
}

// ruleLine is a system-prompt rule: the item's word, then the category.
var ruleLine = regexp.MustCompile(`When the item says ([A-Z]+), answer ([a-z]+)\.`)

// numberedRule is an inline rule line under "Rules:": its number.
var numberedRule = regexp.MustCompile(`^(\d+)\. `)

// pick is the reply for a request whose system prompt is system and whose
// user messages hold text. A rule line that decides cites the label of the
// section it is in: the "## <label>" heading above it, or "rule N" for
// numbered rule N.
func pick(system, text string) string {
	label := ""
	for line := range strings.SplitSeq(system, "\n") {
		if h, ok := strings.CutPrefix(line, "## "); ok {
			label = h
		} else if m := numberedRule.FindStringSubmatch(line); m != nil {
			label = "rule " + m[1]
		}
		if m := ruleLine.FindStringSubmatch(line); m != nil && strings.Contains(text, m[1]) {
			return m[2] + "\nA rule says so.\nRules: " + label
		}
	}
	switch {
	case strings.Contains(text, "INVOICE"):
		return "invoice\nIt is an invoice."
	case strings.Contains(text, "NEWSLETTER"):
		return "newsletter\nIt is a newsletter."
	case strings.Contains(text, "GIBBERISH"):
		return "I cannot tell."
	default:
		return "other"
	}
}

func handleChat(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	// Only the user messages carry the item; the system prompt lists every
	// category and decides the answer only through a rule line.
	var user, system strings.Builder
	for _, m := range req.Messages {
		switch m.Role {
		case "user":
			user.Write(m.Content)
		case "system":
			var text string
			if json.Unmarshal(m.Content, &text) == nil {
				system.WriteString(text)
			} else {
				system.Write(m.Content)
			}
		}
	}
	reply := pick(system.String(), user.String())
	log.Printf("chat: stream=%v reply=%q", req.Stream, reply)
	if req.Stream {
		writeStream(w, req.Model, reply)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": "chatcmpl-stub", "object": "chat.completion", "created": time.Now().Unix(), "model": req.Model,
		"choices": []any{map[string]any{
			"index": 0, "finish_reason": "stop",
			"message": map[string]any{"role": "assistant", "content": reply},
		}},
		"usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 5, "total_tokens": 25},
	})
}

func writeStream(w http.ResponseWriter, model, reply string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	fl, _ := w.(http.Flusher)
	send := func(v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: %s\n\n", b)
		if fl != nil {
			fl.Flush()
		}
	}
	chunk := func(delta map[string]any, finish any) {
		send(map[string]any{
			"id": "chatcmpl-stub", "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": model,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
		})
	}
	chunk(map[string]any{"role": "assistant", "content": reply}, nil)
	chunk(map[string]any{}, "stop")
	send(map[string]any{
		"id": "chatcmpl-stub", "object": "chat.completion.chunk", "model": model, "choices": []any{},
		"usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 5, "total_tokens": 25},
	})
	fmt.Fprint(w, "data: [DONE]\n\n")
	if fl != nil {
		fl.Flush()
	}
}

// handleTelegram answers the Bot API methods a Telegram channel calls. A
// method's parameters arrive as JSON or as a form.
func handleTelegram(w http.ResponseWriter, r *http.Request) {
	rest, ok := strings.CutPrefix(r.URL.Path, "/bot")
	if !ok {
		http.NotFound(w, r)
		return
	}
	_, method, ok := strings.Cut(rest, "/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	params := map[string]any{}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if json.Unmarshal(body, &params) != nil {
		if form, err := parseForm(r, body); err == nil {
			for k, v := range form {
				params[k] = v
			}
		}
	}
	var result any = true
	switch method {
	case "getMe":
		result = map[string]any{"id": 1, "is_bot": true, "first_name": "Stub", "username": "stub_bot"}
	case "getUpdates":
		select { // a short long-poll, so the channel does not spin
		case <-time.After(500 * time.Millisecond):
		case <-r.Context().Done():
		}
		result = []any{}
	case "getMyCommands":
		result = []any{}
	case "getWebhookInfo":
		result = map[string]any{"url": "", "has_custom_certificate": false, "pending_update_count": 0}
	case "sendMessage":
		chat := fmt.Sprint(params["chat_id"])
		log.Printf("telegram: chat=%s text=%q", chat, fmt.Sprint(params["text"]))
		result = map[string]any{
			"message_id": time.Now().UnixNano() % 1_000_000, "date": time.Now().Unix(),
			"chat": map[string]any{"id": params["chat_id"], "type": "private"},
			"text": params["text"],
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}

// parseForm reads a url-encoded or multipart form body.
func parseForm(r *http.Request, body []byte) (map[string]string, error) {
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			return nil, err
		}
	} else if err := r.ParseForm(); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for k, v := range r.Form {
		if len(v) > 0 {
			out[k] = v[0]
		}
	}
	return out, nil
}
