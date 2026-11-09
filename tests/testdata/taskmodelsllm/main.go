// ClawEh
// License: MIT

// taskmodelsllm is a deterministic stand-in for every API a task model
// calls, for the task-model end-to-end test (tests/test_task_models.sh):
//
//   - POST /v1/chat/completions: model "down-1" answers 503; model
//     "image-chat-1" answers with one PNG image (OpenRouter's images array)
//     and the words "IMAGE-WORDS"; any other model answers "TEXT-REPLY".
//     Plain and streaming (SSE) requests are both answered.
//   - POST /v1/images/generations and /v1/images/edits: one PNG as b64_json.
//   - POST /v1/audio/speech: "ID3" followed by stub bytes, as audio/mpeg.
//   - POST /v1/audio/transcriptions: {"text": "TRANSCRIPT-TEXT"}.
//   - POST /v1beta/models/<model>:generateContent (Gemini): raw PCM as
//     inlineData with mimeType audio/L16;codec=pcm;rate=24000.
//
// Every request is logged on stdout as one "<path> model=<model>" line, so
// the test can count the calls per model.
package main

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

// pngB64 is a valid 1x1 PNG.
const pngB64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="

// pcm is four 16-bit samples.
var pcm = []byte{1, 0, 2, 0, 3, 0, 4, 0}

func main() {
	listen := flag.String("listen", "127.0.0.1:0", "address to listen on")
	flag.Parse()
	log.SetOutput(os.Stdout)
	log.SetFlags(0)

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", handleChat)
	mux.HandleFunc("/v1/images/generations", handleImages)
	mux.HandleFunc("/v1/images/edits", handleImages)
	mux.HandleFunc("/v1/audio/speech", handleSpeech)
	mux.HandleFunc("/v1/audio/transcriptions", handleTranscription)
	mux.HandleFunc("/v1beta/", handleGemini)
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	log.Printf("listening on %s", ln.Addr().String())
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.Serve(ln))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write: %v", err)
	}
}

func handleChat(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
		Tools  []any  `json:"tools"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	log.Printf("%s model=%s stream=%v tools=%d", r.URL.Path, req.Model, req.Stream, len(req.Tools))
	if req.Model == "down-1" {
		http.Error(w, `{"error":{"message":"overloaded"}}`, http.StatusServiceUnavailable)
		return
	}
	msg := map[string]any{"role": "assistant", "content": "TEXT-REPLY"}
	if req.Model == "image-chat-1" {
		msg["content"] = "IMAGE-WORDS"
		msg["images"] = []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64," + pngB64}}}
	}
	if req.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := map[string]any{"id": "c", "object": "chat.completion.chunk", "model": req.Model,
			"choices": []any{map[string]any{"index": 0, "delta": msg}}}
		b, err := json.Marshal(chunk)
		if err != nil {
			return
		}
		end := `{"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`
		if _, err := io.WriteString(w, "data: "+string(b)+"\n\ndata: "+end+"\n\ndata: [DONE]\n\n"); err != nil {
			log.Printf("write: %v", err)
		}
		return
	}
	writeJSON(w, map[string]any{
		"id": "chatcmpl-stub", "object": "chat.completion", "created": time.Now().Unix(), "model": req.Model,
		"choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": msg}},
		"usage":   map[string]any{"prompt_tokens": 7, "completion_tokens": 3, "total_tokens": 10},
	})
}

// handleImages answers generations (JSON) and edits (multipart) alike.
func handleImages(w http.ResponseWriter, r *http.Request) {
	model := ""
	if ct, params, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err == nil && strings.HasPrefix(ct, "multipart/") {
		mr := multipart.NewReader(r.Body, params["boundary"])
		images := 0
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			b, err := io.ReadAll(p)
			if err != nil {
				break
			}
			if p.FormName() == "model" {
				model = string(b)
			}
			if p.FileName() != "" {
				images++
			}
		}
		log.Printf("%s model=%s images=%d", r.URL.Path, model, images)
	} else {
		var req struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			model = req.Model
		}
		log.Printf("%s model=%s", r.URL.Path, model)
	}
	writeJSON(w, map[string]any{"data": []any{map[string]any{"b64_json": pngB64}}})
}

func handleSpeech(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model  string `json:"model"`
		Voice  string `json:"voice"`
		Format string `json:"response_format"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	log.Printf("%s model=%s voice=%s format=%s", r.URL.Path, req.Model, req.Voice, req.Format)
	w.Header().Set("Content-Type", "audio/mpeg")
	if _, err := w.Write([]byte("ID3stub-mp3-bytes")); err != nil {
		log.Printf("write: %v", err)
	}
}

func handleTranscription(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	log.Printf("%s model=%s", r.URL.Path, r.FormValue("model"))
	writeJSON(w, map[string]any{"text": "TRANSCRIPT-TEXT"})
}

var geminiPath = regexp.MustCompile(`^/v1beta/models/([^/:]+):generateContent$`)

func handleGemini(w http.ResponseWriter, r *http.Request) {
	m := geminiPath.FindStringSubmatch(r.URL.Path)
	if m == nil {
		http.NotFound(w, r)
		return
	}
	if _, err := io.Copy(io.Discard, r.Body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	log.Printf("/v1beta/models:generateContent model=%s key=%v", m[1], r.Header.Get("X-Goog-Api-Key") != "")
	writeJSON(w, map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{
		map[string]any{"inlineData": map[string]any{"mimeType": "audio/L16;codec=pcm;rate=24000", "data": base64.StdEncoding.EncodeToString(pcm)}},
	}}}}})
}
