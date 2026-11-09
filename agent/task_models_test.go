// ClawEh
// License: MIT

package agent

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
	toolsagents "github.com/PivotLLM/ClawEh/tools/agents"
)

// pngBytes is a minimal PNG signature, enough for type detection.
var pngBytes = []byte("\x89PNG\r\n\x1a\n0000IHDR")

// vendorServer stands in for every API a task model calls: OpenAI-style
// chat, images, speech and transcription, and Gemini's generateContent. A
// model whose name is in fail answers 500.
type vendorServer struct {
	mu       sync.Mutex
	paths    []string
	bodies   []string
	fail     map[string]bool
	chatText string
}

func (v *vendorServer) handle(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	body := string(raw)
	v.mu.Lock()
	v.paths = append(v.paths, r.URL.Path)
	v.bodies = append(v.bodies, body)
	fail := false
	for m := range v.fail {
		if strings.Contains(body, m) || strings.Contains(r.URL.Path, m) {
			fail = true
		}
	}
	text := v.chatText
	v.mu.Unlock()
	if fail {
		http.Error(w, `{"error":{"message":"down"}}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	switch {
	case strings.HasSuffix(r.URL.Path, "/chat/completions"):
		msg := map[string]any{"role": "assistant", "content": text}
		if strings.Contains(body, "draw") {
			msg["images"] = []any{map[string]any{"type": "image_url", "image_url": map[string]any{
				"url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes),
			}}}
		}
		encodeOrFail(w, enc, map[string]any{
			"choices": []any{map[string]any{"message": msg, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 3, "completion_tokens": 5, "total_tokens": 8},
		})
	case strings.HasSuffix(r.URL.Path, "/images/generations"), strings.HasSuffix(r.URL.Path, "/images/edits"):
		encodeOrFail(w, enc, map[string]any{"data": []any{map[string]any{"b64_json": base64.StdEncoding.EncodeToString(pngBytes)}}})
	case strings.HasSuffix(r.URL.Path, "/audio/speech"):
		w.Header().Set("Content-Type", "audio/mpeg")
		if _, err := w.Write([]byte("ID3fake-mp3")); err != nil {
			return
		}
	case strings.HasSuffix(r.URL.Path, "/audio/transcriptions"):
		encodeOrFail(w, enc, map[string]any{"text": "hello from the audio"})
	case strings.Contains(r.URL.Path, ":generateContent"):
		encodeOrFail(w, enc, map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{
			map[string]any{"inlineData": map[string]any{"mimeType": "audio/L16;codec=pcm;rate=16000", "data": base64.StdEncoding.EncodeToString([]byte{1, 0, 2, 0})}},
		}}}}})
	default:
		http.NotFound(w, r)
	}
}

// encodeOrFail writes v as JSON, or a 500 when it can't.
func encodeOrFail(w http.ResponseWriter, enc *json.Encoder, v any) {
	if err := enc.Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (v *vendorServer) calls() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]string(nil), v.paths...)
}

// taskHostEnv is a loop whose config has task models on a fake vendor, and
// Alice allowed every kind with file-reading tools.
type taskHostEnv struct {
	cfg       *config.Config
	al        *AgentLoop
	host      *taskModelHost
	vendor    *vendorServer
	workspace string
}

func newTaskHostEnv(t *testing.T) *taskHostEnv {
	t.Helper()
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))
	v := &vendorServer{fail: map[string]bool{}, chatText: "the answer"}
	srv := httptest.NewServer(http.HandlerFunc(v.handle))
	t.Cleanup(srv.Close)
	cfg := ownerTestConfig(t)
	cfg.Providers = []config.Provider{
		{Name: "p", Protocol: "openai-chat", BaseURL: srv.URL + "/v1", APIKey: "k"},
		{Name: "g", Protocol: config.GeminiProtocol, BaseURL: srv.URL, APIKey: "gk"},
		{Name: "a", Protocol: "anthropic", BaseURL: srv.URL + "/v1", APIKey: "ak"},
	}
	cfg.Models = []config.ModelConfig{
		{ModelName: "chat-a", Model: "chat-a-wire", Provider: "p", Enabled: true},
		{ModelName: "chat-b", Model: "chat-b-wire", Provider: "p", Enabled: true},
		{ModelName: "imgapi", Model: "gpt-image", Provider: "p", Enabled: true},
		{ModelName: "tts-1", Model: "tts-1-wire", Provider: "p", Enabled: true},
		{ModelName: "gemini-tts", Model: "gemini-tts-wire", Provider: "g", Enabled: true},
		{ModelName: "whisper", Model: "whisper-1", Provider: "p", Enabled: true},
		{ModelName: "claude", Model: "claude-wire", Provider: "a", Enabled: true},
	}
	cfg.Agents.Defaults.Models = []string{"chat-a"}
	cfg.Agents.List = []config.AgentConfig{{
		ID: "alice", Name: "Alice", Default: true, Tools: []string{"file_read_bytes"},
		TaskModels: &config.AgentTaskModels{Text: true, Image: true, TTS: true, STT: true},
	}, {ID: "bob", Name: "Bob"}}
	cfg.TaskModels.Models = []config.TaskModel{
		{Model: "chat-a", Text: true, Image: true},
		{Model: "chat-b", Text: true},
		{Model: "imgapi", Image: true, ImageAPI: config.ImageAPIImages},
		{Model: "tts-1", TTS: true, TTSDefaults: &config.TTSDefaults{Voice: "alloy", Format: "mp3"}},
		{Model: "gemini-tts", TTS: true},
		{Model: "whisper", STT: true},
	}
	al := mustNewAgentLoop(t, cfg, bus.NewMessageBus(), &mockProvider{}, providers.NewProviderDispatcher(cfg))
	ws := filepath.Join(t.TempDir(), "ws")
	if err := os.MkdirAll(filepath.Join(ws, "files"), 0o700); err != nil {
		t.Fatal(err)
	}
	return &taskHostEnv{cfg: cfg, al: al, host: &taskModelHost{al: al}, vendor: v, workspace: ws}
}

func (e *taskHostEnv) write(t *testing.T, rel string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.workspace, filepath.FromSlash(rel)), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (e *taskHostEnv) read(t *testing.T, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(e.workspace, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (e *taskHostEnv) run(t *testing.T, agentID string, req toolsagents.TaskModelRequest) (*toolsagents.TaskModelResult, error) {
	t.Helper()
	return e.host.Run(context.Background(), agentID, e.workspace, req)
}

// Text: the first Text model answers; the text is saved as .md under
// files/generated/<date>/ with the agents-area modes and comes back inline;
// a text input file is added to the prompt.
func TestTaskModelHost_Text(t *testing.T) {
	e := newTaskHostEnv(t)
	e.write(t, "files/notes.txt", []byte("some notes"))
	res, err := e.run(t, "alice", toolsagents.TaskModelRequest{Kind: "text", Prompt: "summarise", Files: []string{"files/notes.txt"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Model != "chat-a" || res.Text != "the answer" || len(res.Paths) != 1 {
		t.Fatalf("result = %+v", res)
	}
	p := res.Paths[0]
	if !strings.HasPrefix(p, "files/generated/") || !strings.Contains(p, "/text-") || !strings.HasSuffix(p, ".md") {
		t.Errorf("path = %q", p)
	}
	if string(e.read(t, p)) != "the answer" {
		t.Errorf("file = %q", e.read(t, p))
	}
	info, err := os.Stat(filepath.Join(e.workspace, filepath.FromSlash(p)))
	if err != nil || info.Mode().Perm() != e.cfg.AgentModes(e.workspace).File {
		t.Errorf("mode = %v, want %v", info.Mode().Perm(), e.cfg.AgentModes(e.workspace).File)
	}
	if res.Usage != "3 input and 5 output tokens" {
		t.Errorf("usage = %q", res.Usage)
	}
	e.vendor.mu.Lock()
	body := e.vendor.bodies[len(e.vendor.bodies)-1]
	e.vendor.mu.Unlock()
	if !strings.Contains(body, "summarise") || !strings.Contains(body, "some notes") || strings.Contains(body, `"tools"`) {
		t.Errorf("chat body = %s, want the prompt and the file, no tools", body)
	}
}

// A failing model falls back to the next in page order; a named model is
// the only one tried; a model in cooldown is skipped.
func TestTaskModelHost_FallbackNamedAndCooldown(t *testing.T) {
	e := newTaskHostEnv(t)
	e.vendor.fail["chat-a-wire"] = true
	res, err := e.run(t, "alice", toolsagents.TaskModelRequest{Kind: "text", Prompt: "hi"})
	if err != nil || res.Model != "chat-b" || len(res.Attempts) != 1 || res.Attempts[0].Model != "chat-a" {
		t.Fatalf("fallback: %+v, %v", res, err)
	}

	res, err = e.run(t, "alice", toolsagents.TaskModelRequest{Kind: "text", Model: "chat-a", Prompt: "hi"})
	if err == nil || res.Model != "" {
		t.Fatalf("named failing model: %+v, %v; want an error and no other model tried", res, err)
	}

	delete(e.vendor.fail, "chat-a-wire")
	e.al.cooldownTracker().MarkFailure("p", "chat-a-wire", providers.FailoverRateLimit, http.StatusTooManyRequests, time.Hour)
	before := len(e.vendor.calls())
	res, err = e.run(t, "alice", toolsagents.TaskModelRequest{Kind: "text", Prompt: "hi"})
	if err != nil || res.Model != "chat-b" || !strings.HasPrefix(res.Attempts[0].Error, "in cooldown") {
		t.Fatalf("cooldown: %+v, %v", res, err)
	}
	if len(e.vendor.calls()) != before+1 {
		t.Error("a model in cooldown was called")
	}
}

// Images: chat image output (the default image_api) and the images API.
func TestTaskModelHost_Images(t *testing.T) {
	e := newTaskHostEnv(t)
	res, err := e.run(t, "alice", toolsagents.TaskModelRequest{Kind: "image", Prompt: "draw a cat"})
	if err != nil || res.Model != "chat-a" || len(res.Paths) != 1 || !strings.HasSuffix(res.Paths[0], ".png") {
		t.Fatalf("chat image: %+v, %v", res, err)
	}
	if string(e.read(t, res.Paths[0])) != string(pngBytes) {
		t.Error("image data not saved as returned")
	}

	res, err = e.run(t, "alice", toolsagents.TaskModelRequest{Kind: "image", Model: "imgapi", Prompt: "a dog"})
	if err != nil || len(res.Paths) != 1 || !strings.Contains(strings.Join(e.vendor.calls(), " "), "/v1/images/generations") {
		t.Fatalf("images API: %+v, %v, calls %v", res, err, e.vendor.calls())
	}
	e.write(t, "files/in.png", pngBytes)
	if _, err = e.run(t, "alice", toolsagents.TaskModelRequest{Kind: "image", Model: "imgapi", Prompt: "redder", Files: []string{"files/in.png"}}); err != nil ||
		!strings.Contains(strings.Join(e.vendor.calls(), " "), "/v1/images/edits") {
		t.Fatalf("images edit: %v, calls %v", err, e.vendor.calls())
	}
}

// Speech: voice and format from tts_defaults unless the call gives them;
// Gemini's raw PCM is saved as a WAV file.
func TestTaskModelHost_Speech(t *testing.T) {
	e := newTaskHostEnv(t)
	res, err := e.run(t, "alice", toolsagents.TaskModelRequest{Kind: "tts", Prompt: "Hello Bob"})
	if err != nil || res.Model != "tts-1" || len(res.Paths) != 1 || !strings.HasSuffix(res.Paths[0], ".mp3") {
		t.Fatalf("tts: %+v, %v", res, err)
	}
	e.vendor.mu.Lock()
	body := e.vendor.bodies[len(e.vendor.bodies)-1]
	e.vendor.mu.Unlock()
	if !strings.Contains(body, `"voice":"alloy"`) || !strings.Contains(body, `"response_format":"mp3"`) {
		t.Errorf("speech body = %s, want the tts_defaults", body)
	}
	if _, err = e.run(t, "alice", toolsagents.TaskModelRequest{Kind: "tts", Model: "tts-1", Prompt: "x", Voice: "nova"}); err != nil {
		t.Fatal(err)
	}
	e.vendor.mu.Lock()
	body = e.vendor.bodies[len(e.vendor.bodies)-1]
	e.vendor.mu.Unlock()
	if !strings.Contains(body, `"voice":"nova"`) {
		t.Errorf("speech body = %s, want the call's voice", body)
	}

	res, err = e.run(t, "alice", toolsagents.TaskModelRequest{Kind: "tts", Model: "gemini-tts", Prompt: "Hello"})
	if err != nil || !strings.HasSuffix(res.Paths[0], ".wav") {
		t.Fatalf("gemini tts: %+v, %v", res, err)
	}
	wav := e.read(t, res.Paths[0])
	if string(wav[:4]) != "RIFF" || string(wav[8:12]) != "WAVE" || binary.LittleEndian.Uint32(wav[24:]) != 16000 ||
		binary.LittleEndian.Uint32(wav[40:]) != 4 || len(wav) != 48 {
		t.Errorf("wav header wrong: % x", wav[:44])
	}
}

// Transcription takes exactly one audio file; the transcript is saved as .md.
func TestTaskModelHost_Transcription(t *testing.T) {
	e := newTaskHostEnv(t)
	e.write(t, "files/clip.ogg", []byte("OggS fake"))
	res, err := e.run(t, "alice", toolsagents.TaskModelRequest{Kind: "stt", Files: []string{"files/clip.ogg"}})
	if err != nil || res.Text != "hello from the audio" || !strings.Contains(res.Paths[0], "/transcript-") {
		t.Fatalf("stt: %+v, %v", res, err)
	}
	if err := e.host.Check(context.Background(), "alice", e.workspace, toolsagents.TaskModelRequest{Kind: "stt"}); err == nil ||
		err.Error() != "Transcription needs exactly one audio file in files or media." {
		t.Errorf("no file = %v", err)
	}
}

// The refusals: no grant (naming the agent and the kind), a model not
// ticked for the kind, no model ticked, a file the agent can't read.
func TestTaskModelHost_Refusals(t *testing.T) {
	e := newTaskHostEnv(t)
	check := func(agent string, req toolsagents.TaskModelRequest) string {
		t.Helper()
		err := e.host.Check(context.Background(), agent, e.workspace, req)
		if err == nil {
			return ""
		}
		return err.Error()
	}
	if got := check("bob", toolsagents.TaskModelRequest{Kind: "image", Prompt: "x"}); got != "Bob is not allowed to use task models for images." {
		t.Errorf("no grant = %q", got)
	}
	e.cfg.Agents.List[1].TaskModels = &config.AgentTaskModels{Image: true}
	if got := check("bob", toolsagents.TaskModelRequest{Kind: "image", Prompt: "x"}); got != "" {
		t.Errorf("granted = %q", got)
	}
	if got := check("alice", toolsagents.TaskModelRequest{Kind: "text", Model: "tts-1", Prompt: "x"}); got != "Model tts-1 is not a task model for text; tick it on the Task models page." {
		t.Errorf("wrong model = %q", got)
	}
	e.cfg.TaskModels.Models = e.cfg.TaskModels.Models[2:]
	if got := check("alice", toolsagents.TaskModelRequest{Kind: "text", Prompt: "x"}); got != "No model is ticked for text on the Task models page." {
		t.Errorf("none ticked = %q", got)
	}
	if got := check("alice", toolsagents.TaskModelRequest{Kind: "stt", Files: []string{"../outside.ogg"}}); !strings.HasPrefix(got, `Can't use "../outside.ogg": you can't read it`) {
		t.Errorf("unreadable file = %q", got)
	}
	e.cfg.Agents.List[0].Tools = nil
	e.write(t, "files/clip.ogg", []byte("x"))
	if got := check("alice", toolsagents.TaskModelRequest{Kind: "stt", Files: []string{"files/clip.ogg"}}); got != "You can only use files you're allowed to read." {
		t.Errorf("no read tool = %q", got)
	}
}

func TestCutText(t *testing.T) {
	if got := cutText("short", 10, "x.md"); got != "short" {
		t.Errorf("short = %q", got)
	}
	if got := cutText("abcdefghij", 4, "files/generated/t.md"); got != "abcd\n[cut at 4 characters; full text in files/generated/t.md]" {
		t.Errorf("cut = %q", got)
	}
}

func TestPCMRate(t *testing.T) {
	for _, tc := range []struct {
		ct   string
		rate int
		ok   bool
	}{{"audio/L16;codec=pcm;rate=24000", 24000, true}, {"audio/l16", 24000, true}, {"audio/L16;rate=16000", 16000, true}, {"audio/mpeg", 0, false}} {
		if rate, ok := pcmRate(tc.ct); rate != tc.rate || ok != tc.ok {
			t.Errorf("pcmRate(%q) = %d, %v", tc.ct, rate, ok)
		}
	}
}
