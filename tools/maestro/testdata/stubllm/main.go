// ClawEh
// License: MIT

// stubllm is a deterministic OpenAI-compatible chat server used by the Maestro
// host-mode test harness (test-maestro-host.sh). It never calls a tool and
// answers by rule:
//
//   - a prompt containing "[[FAIL]]" gets HTTP 500 (a failed model call);
//   - a prompt containing "REQUIRED RESPONSE FORMAT" gets a JSON object, so a
//     schema-validated Maestro task passes;
//   - anything else gets "OK".
//
// Both plain and streaming (SSE) responses are supported.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:0", "address to listen on")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"stub-1","object":"model"}]}`))
	})
	mux.HandleFunc("/v1/chat/completions", handleChat)

	srv := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	ln, err := netListen(*listen)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	fmt.Printf("listening on %s\n", ln.Addr().String())
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
	var all strings.Builder
	for _, m := range req.Messages {
		all.Write(m.Content)
	}
	text := all.String()
	switch {
	case strings.Contains(text, "[[FAIL]]"):
		log.Printf("chat: failing on request (marker)")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"stub failure requested by prompt","type":"server_error"}}`))
		return
	}
	reply := "OK"
	if strings.Contains(text, "REQUIRED RESPONSE FORMAT") {
		reply = `{"status": "ok", "summary": "stub result"}`
	}
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
	chunk := func(delta map[string]any, finish any) {
		b, _ := json.Marshal(map[string]any{
			"id": "chatcmpl-stub", "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": model,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
		})
		fmt.Fprintf(w, "data: %s\n\n", b)
		if fl != nil {
			fl.Flush()
		}
	}
	chunk(map[string]any{"role": "assistant", "content": reply}, nil)
	chunk(map[string]any{}, "stop")
	b, _ := json.Marshal(map[string]any{
		"id": "chatcmpl-stub", "object": "chat.completion.chunk", "model": model, "choices": []any{},
		"usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 5, "total_tokens": 25},
	})
	fmt.Fprintf(w, "data: %s\n\n", b)
	fmt.Fprint(w, "data: [DONE]\n\n")
	if fl != nil {
		fl.Flush()
	}
}
