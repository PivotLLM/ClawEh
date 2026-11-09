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
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/internal/audit"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/media"
	"github.com/PivotLLM/ClawEh/providers"
	toolsagents "github.com/PivotLLM/ClawEh/tools/agents"
)

// pngBytes is a minimal PNG signature, enough for type detection.
var pngBytes = []byte("\x89PNG\r\n\x1a\n0000IHDR")

// vendorServer stands in for every API a task model calls: OpenAI-style
// chat, images, speech and transcription, Gemini's generateContent and
// ElevenLabs. A request naming a model in fail answers that status.
type vendorServer struct {
	mu       sync.Mutex
	paths    []string
	bodies   []string
	fail     map[string]int
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
	status := 0
	for m, s := range v.fail {
		if strings.Contains(body, m) || strings.Contains(r.URL.Path, m) {
			status = s
		}
	}
	text := v.chatText
	v.mu.Unlock()
	if status != 0 {
		http.Error(w, `{"error":{"message":"refused"}}`, status)
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
	case strings.HasSuffix(r.URL.Path, "/images/generations") && strings.Contains(body, "empty-image"):
		encodeOrFail(w, enc, map[string]any{"data": []any{}})
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
	case strings.HasSuffix(r.URL.Path, "/v1/speech-to-text"):
		encodeOrFail(w, enc, map[string]any{"text": "scribe heard this", "language_code": "en"})
	case strings.HasPrefix(r.URL.Path, "/v1/text-to-speech/"):
		// No Content-Type: the requested output_format names the audio.
		w.Header()["Content-Type"] = nil
		if _, err := w.Write([]byte{9, 0, 8, 0, 7, 0}); err != nil {
			return
		}
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

func (v *vendorServer) lastBody() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.bodies[len(v.bodies)-1]
}

// taskHostEnv is a loop whose config has task models on a fake vendor, and
// Alice allowed every kind with file-reading tools.
type taskHostEnv struct {
	cfg       *config.Config
	al        *AgentLoop
	host      *taskModelHost
	vendor    *vendorServer
	workspace string
	store     *media.FileMediaStore
}

func newTaskHostEnv(t *testing.T) *taskHostEnv {
	t.Helper()
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))
	v := &vendorServer{fail: map[string]int{}, chatText: "the answer"}
	srv := httptest.NewServer(http.HandlerFunc(v.handle))
	t.Cleanup(srv.Close)
	cfg := ownerTestConfig(t)
	cfg.Providers = []config.Provider{
		{Name: "p", Protocol: "openai-chat", BaseURL: srv.URL + "/v1", APIKey: "k"},
		{Name: "g", Protocol: config.GeminiProtocol, BaseURL: srv.URL, APIKey: "gk"},
		{Name: "e", Protocol: config.ElevenLabsProtocol, BaseURL: srv.URL, APIKey: "ek"},
		{Name: "a", Protocol: "anthropic", BaseURL: srv.URL + "/v1", APIKey: "ak"},
	}
	cfg.Models = []config.ModelConfig{
		{ModelName: "chat-a", Model: "chat-a-wire", Provider: "p", Enabled: true},
		{ModelName: "chat-b", Model: "chat-b-wire", Provider: "p", Enabled: true},
		{ModelName: "imgapi", Model: "gpt-image", Provider: "p", Enabled: true},
		{ModelName: "tts-1", Model: "tts-1-wire", Provider: "p", Enabled: true},
		{ModelName: "gemini-tts", Model: "gemini-tts-wire", Provider: "g", Enabled: true},
		{ModelName: "eleven", Model: "eleven-wire", Provider: "e", Enabled: true},
		{ModelName: "whisper", Model: "whisper-1", Provider: "p", Enabled: true},
		{ModelName: "claude", Model: "claude-wire", Provider: "a", Enabled: true},
	}
	cfg.Agents.Defaults.Models = []string{"chat-a"}
	cfg.Agents.List = []config.AgentConfig{{
		ID: "alice", Name: "Alice", Default: true, Tools: []string{"file_read_bytes", "agent_spawn"},
		TaskModels: &config.AgentTaskModels{Text: true, Image: true, TTS: true, STT: true},
	}, {ID: "bob", Name: "Bob"}}
	cfg.TaskModels.Models = []config.TaskModel{
		{Model: "chat-a", Text: true, Image: true},
		{Model: "chat-b", Text: true},
		{Model: "imgapi", Image: true, ImageAPI: config.ImageAPIImages},
		{Model: "tts-1", TTS: true, TTSDefaults: &config.TTSDefaults{Voice: "alloy", Format: "mp3"}},
		{Model: "gemini-tts", TTS: true},
		{Model: "whisper", STT: true},
		{Model: "eleven", TTS: true, STT: true, TTSDefaults: &config.TTSDefaults{Voice: "v1", Format: "pcm_16000"}},
		{Model: "claude", Image: true, ImageAPI: config.ImageAPIImages},
	}
	al := mustNewAgentLoop(t, cfg, bus.NewMessageBus(), &mockProvider{}, providers.NewProviderDispatcher(cfg), OwnsDataDir())
	store := media.NewFileMediaStore()
	al.SetMediaStore(store)
	ws := filepath.Join(t.TempDir(), "ws")
	if err := os.MkdirAll(filepath.Join(ws, "files"), 0o700); err != nil {
		t.Fatal(err)
	}
	return &taskHostEnv{cfg: cfg, al: al, host: &taskModelHost{al: al}, vendor: v, workspace: ws, store: store}
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

// media stores data as a conversation attachment and returns its ref.
func (e *taskHostEnv) media(t *testing.T, name, contentType string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	ref, err := e.store.Store(p, media.MediaMeta{Filename: name, ContentType: contentType}, "test")
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func (e *taskHostEnv) run(t *testing.T, agentID string, req toolsagents.TaskModelRequest) (*toolsagents.TaskModelResult, error) {
	t.Helper()
	return e.host.Run(context.Background(), agentID, e.workspace, req)
}

func (e *taskHostEnv) check(t *testing.T, agentID string, req toolsagents.TaskModelRequest) string {
	t.Helper()
	if err := e.host.Check(context.Background(), agentID, e.workspace, req); err != nil {
		return err.Error()
	}
	return ""
}

// initAudit installs a fresh audit store for the test.
func initAudit(t *testing.T) {
	t.Helper()
	if err := audit.Init(t.TempDir()); err != nil {
		t.Fatalf("audit.Init: %v", err)
	}
	t.Cleanup(func() {
		if err := audit.Close(); err != nil {
			t.Errorf("audit.Close: %v", err)
		}
	})
}

// taskAuditRows flushes the audit store and returns the task_model rows.
func taskAuditRows(t *testing.T) []audit.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := audit.Default().Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	rows, err := audit.Default().Query(context.Background(), audit.Filter{Kind: audit.KindTaskModel})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// Text: the first Text model answers; the text is saved as .md under
// files/generated/<date>/ with the agents-area modes and comes back inline;
// a text input file is added to the prompt; the call is audited.
func TestTaskModelHost_Text(t *testing.T) {
	initAudit(t)
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
	if !strings.HasPrefix(p, "files/generated/"+time.Now().Format("2006-01-02")+"/text-") || !strings.HasSuffix(p, ".md") {
		t.Errorf("path = %q", p)
	}
	if string(e.read(t, p)) != "the answer" {
		t.Errorf("file = %q", e.read(t, p))
	}
	full := filepath.Join(e.workspace, filepath.FromSlash(p))
	info, err := os.Stat(full)
	if err != nil || info.Mode().Perm() != e.cfg.AgentModes(full).File {
		t.Errorf("file mode = %v, want %v", info.Mode().Perm(), e.cfg.AgentModes(full).File)
	}
	if dinfo, err := os.Stat(filepath.Dir(full)); err != nil || dinfo.Mode().Perm() != e.cfg.AgentModes(full).Dir {
		t.Errorf("folder mode = %v, want %v", dinfo.Mode().Perm(), e.cfg.AgentModes(full).Dir)
	}
	if res.Usage != "3 input and 5 output tokens" {
		t.Errorf("usage = %q", res.Usage)
	}
	body := e.vendor.lastBody()
	if !strings.Contains(body, "summarise") || !strings.Contains(body, "some notes") || strings.Contains(body, `"tools"`) {
		t.Errorf("chat body = %s, want the prompt and the file, no tools", body)
	}
	rows := taskAuditRows(t)
	if len(rows) != 1 || rows[0].Agent != "alice" || rows[0].Tool != "text" || rows[0].Outcome != audit.OutcomeOK ||
		rows[0].Summary != p || !strings.Contains(rows[0].Details, `"model":"chat-a"`) || strings.Contains(rows[0].Details, "clone") {
		t.Errorf("audit rows = %+v", rows)
	}
}

// A long text is cut in the result with the file named; the file has it all.
func TestTaskModelHost_TextCut(t *testing.T) {
	e := newTaskHostEnv(t)
	e.vendor.chatText = strings.Repeat("é", 30)
	e.cfg.TaskModels.ResultTextChars = 10
	res, err := e.run(t, "alice", toolsagents.TaskModelRequest{Kind: "text", Prompt: "write"})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Repeat("é", 10) + "\n[cut at 10 characters; full text in " + res.Paths[0] + "]"
	if res.Text != want {
		t.Errorf("text = %q, want %q", res.Text, want)
	}
	if string(e.read(t, res.Paths[0])) != strings.Repeat("é", 30) {
		t.Error("the file does not hold the whole text")
	}
}

// A failing model falls back to the next in page order; a named model is
// the only one tried; a model in cooldown is skipped.
func TestTaskModelHost_FallbackNamedAndCooldown(t *testing.T) {
	e := newTaskHostEnv(t)
	e.vendor.fail["chat-a-wire"] = http.StatusServiceUnavailable
	res, err := e.run(t, "alice", toolsagents.TaskModelRequest{Kind: "text", Prompt: "hi"})
	if err != nil || res.Model != "chat-b" || len(res.Attempts) != 1 || res.Attempts[0].Model != "chat-a" {
		t.Fatalf("fallback: %+v, %v", res, err)
	}
	if e.al.cooldownTracker().IsAvailable("p", "chat-a-wire") {
		t.Error("a model that answered 503 is not in cooldown")
	}

	e.al.cooldownTracker().MarkSuccess("p", "chat-a-wire")
	res, err = e.run(t, "alice", toolsagents.TaskModelRequest{Kind: "text", Model: "chat-a", Prompt: "hi"})
	if err == nil || res.Model != "" || len(res.Attempts) != 1 {
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

// A failure the request caused (400, 404) falls back but leaves the model
// available, to the agents chatting with it and to the next call; rate
// limits, outages and key problems put it in cooldown.
func TestTaskModelHost_CooldownOnlyForModelFailures(t *testing.T) {
	for _, tc := range []struct {
		status   int
		cooldown bool
	}{
		{http.StatusBadRequest, false},
		{http.StatusNotFound, false},
		{http.StatusRequestEntityTooLarge, false},
		{http.StatusTooManyRequests, true},
		{http.StatusInternalServerError, true},
		{http.StatusBadGateway, true},
		{http.StatusUnauthorized, true},
		{http.StatusPaymentRequired, true},
		{http.StatusForbidden, true},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			e := newTaskHostEnv(t)
			e.vendor.fail["chat-a-wire"] = tc.status
			res, err := e.run(t, "alice", toolsagents.TaskModelRequest{Kind: "text", Prompt: "hi"})
			if err != nil || res.Model != "chat-b" {
				t.Fatalf("fallback: %+v, %v", res, err)
			}
			if got := !e.al.cooldownTracker().IsAvailable("p", "chat-a-wire"); got != tc.cooldown {
				t.Fatalf("in cooldown after %d = %v, want %v", tc.status, got, tc.cooldown)
			}
			if !tc.cooldown {
				delete(e.vendor.fail, "chat-a-wire")
				res, err = e.run(t, "alice", toolsagents.TaskModelRequest{Kind: "text", Prompt: "hi"})
				if err != nil || res.Model != "chat-a" {
					t.Errorf("next call: %+v, %v; want chat-a again", res, err)
				}
			}
		})
	}
}

// Images: chat image output (the default image_api, its words saved and
// cut like text) and the images API, generating and editing.
func TestTaskModelHost_Images(t *testing.T) {
	e := newTaskHostEnv(t)
	e.vendor.chatText = "Here is your cat."
	e.cfg.TaskModels.ResultTextChars = 4
	res, err := e.run(t, "alice", toolsagents.TaskModelRequest{Kind: "image", Prompt: "draw a cat"})
	if err != nil || res.Model != "chat-a" || len(res.Paths) != 2 || !strings.HasSuffix(res.Paths[0], ".png") || !strings.HasSuffix(res.Paths[1], ".md") {
		t.Fatalf("chat image: %+v, %v", res, err)
	}
	if string(e.read(t, res.Paths[0])) != string(pngBytes) {
		t.Error("image data not saved as returned")
	}
	if string(e.read(t, res.Paths[1])) != "Here is your cat." || res.Text != "Here\n[cut at 4 characters; full text in "+res.Paths[1]+"]" {
		t.Errorf("the model's words: file %q, text %q", e.read(t, res.Paths[1]), res.Text)
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

// image_api "images" on a provider without OpenAI-style image endpoints
// fails that model with a sentence naming the provider.
func TestTaskModelHost_ImagesAPINeedsOpenAI(t *testing.T) {
	e := newTaskHostEnv(t)
	res, err := e.run(t, "alice", toolsagents.TaskModelRequest{Kind: "image", Model: "claude", Prompt: "a dog"})
	if err == nil || len(res.Attempts) != 1 || res.Attempts[0].Error != "provider a has no OpenAI-style image API" {
		t.Fatalf("result = %+v, %v", res, err)
	}
}

// A media attachment is an input like a file: an image to edit, a voice
// note to transcribe, its content type reduced to type/subtype.
func TestTaskModelHost_MediaInputs(t *testing.T) {
	e := newTaskHostEnv(t)
	img := e.media(t, "photo", "image/png", pngBytes)
	if _, err := e.run(t, "alice", toolsagents.TaskModelRequest{Kind: "image", Model: "imgapi", Prompt: "redder", Media: []string{img}}); err != nil ||
		!strings.Contains(strings.Join(e.vendor.calls(), " "), "/v1/images/edits") {
		t.Fatalf("media image edit: %v, calls %v", err, e.vendor.calls())
	}
	voice := e.media(t, "voice-note", "audio/ogg; codecs=opus", []byte("OggS fake"))
	res, err := e.run(t, "alice", toolsagents.TaskModelRequest{Kind: "stt", Model: "whisper", Media: []string{voice}})
	if err != nil || res.Text != "hello from the audio" {
		t.Fatalf("media stt: %+v, %v", res, err)
	}
	if body := e.vendor.lastBody(); !strings.Contains(body, "Content-Type: audio/ogg\r\n") {
		t.Errorf("transcription upload has no plain audio/ogg part: %q", body)
	}
	if got := e.check(t, "alice", toolsagents.TaskModelRequest{Kind: "stt", Media: []string{"media://gone"}}); got != "Can't use media://gone: it is no longer available." {
		t.Errorf("unknown ref = %q", got)
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
	body := e.vendor.lastBody()
	if !strings.Contains(body, `"voice":"alloy"`) || !strings.Contains(body, `"response_format":"mp3"`) {
		t.Errorf("speech body = %s, want the tts_defaults", body)
	}
	if _, err = e.run(t, "alice", toolsagents.TaskModelRequest{Kind: "tts", Model: "tts-1", Prompt: "x", Voice: "nova"}); err != nil {
		t.Fatal(err)
	}
	if body = e.vendor.lastBody(); !strings.Contains(body, `"voice":"nova"`) {
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

// ElevenLabs through the host: Scribe transcribes; speech in pcm_16000 with
// no declared type is wrapped in WAV at 16 kHz, ulaw is saved raw.
func TestTaskModelHost_ElevenLabs(t *testing.T) {
	e := newTaskHostEnv(t)
	e.write(t, "files/clip.mp3", []byte("ID3 fake"))
	res, err := e.run(t, "alice", toolsagents.TaskModelRequest{Kind: "stt", Model: "eleven", Files: []string{"files/clip.mp3"}})
	if err != nil || res.Text != "scribe heard this" || !strings.Contains(strings.Join(e.vendor.calls(), " "), "/v1/speech-to-text") {
		t.Fatalf("scribe: %+v, %v, calls %v", res, err, e.vendor.calls())
	}

	res, err = e.run(t, "alice", toolsagents.TaskModelRequest{Kind: "tts", Model: "eleven", Prompt: "Hi Bob"})
	if err != nil || !strings.HasSuffix(res.Paths[0], ".wav") {
		t.Fatalf("eleven tts: %+v, %v", res, err)
	}
	calls := e.vendor.calls()
	if last := calls[len(calls)-1]; last != "/v1/text-to-speech/v1" {
		t.Errorf("tts path = %q", last)
	}
	wav := e.read(t, res.Paths[0])
	if string(wav[:4]) != "RIFF" || binary.LittleEndian.Uint32(wav[24:]) != 16000 || len(wav) != 44+6 {
		t.Errorf("wav = % x", wav)
	}

	res, err = e.run(t, "alice", toolsagents.TaskModelRequest{Kind: "tts", Model: "eleven", Prompt: "Hi", Format: "ulaw_8000"})
	if err != nil || !strings.HasSuffix(res.Paths[0], ".ulaw") || string(e.read(t, res.Paths[0])) != string([]byte{9, 0, 8, 0, 7, 0}) {
		t.Fatalf("ulaw: %+v, %v", res, err)
	}
}

// Transcription takes exactly one audio file; anything else is refused with
// a sentence naming it; the transcript is saved as .md.
func TestTaskModelHost_Transcription(t *testing.T) {
	e := newTaskHostEnv(t)
	e.write(t, "files/clip.ogg", []byte("OggS fake"))
	res, err := e.run(t, "alice", toolsagents.TaskModelRequest{Kind: "stt", Files: []string{"files/clip.ogg"}})
	if err != nil || res.Text != "hello from the audio" || !strings.Contains(res.Paths[0], "/transcript-") {
		t.Fatalf("stt: %+v, %v", res, err)
	}
	if string(e.read(t, res.Paths[0])) != "hello from the audio" {
		t.Error("transcript not saved")
	}
	if got := e.check(t, "alice", toolsagents.TaskModelRequest{Kind: "stt"}); got != "Transcription needs exactly one audio file in files or media." {
		t.Errorf("no file = %q", got)
	}
	e.write(t, "files/notes.txt", []byte("words"))
	if got := e.check(t, "alice", toolsagents.TaskModelRequest{Kind: "stt", Files: []string{"files/notes.txt"}}); got != "notes.txt can't be transcribed: it is not an audio file." {
		t.Errorf("text file = %q", got)
	}
	for _, name := range []string{"a.oga", "a.opus", "a.m4a", "a.mp4", "a.wav", "a.flac", "a.webm", "a.aac", "a.amr", "a.mpga"} {
		e.write(t, "files/"+name, []byte("x"))
		if got := e.check(t, "alice", toolsagents.TaskModelRequest{Kind: "stt", Files: []string{"files/" + name}}); got != "" {
			t.Errorf("%s refused: %q", name, got)
		}
	}
}

// Any valid UTF-8 file is text, whatever its extension; a binary file and
// an audio file are refused for text, an image is attached.
func TestTaskModelHost_TextInputs(t *testing.T) {
	e := newTaskHostEnv(t)
	for name, data := range map[string]string{"c.yaml": "a: 1\n", "run.sh": "#!/bin/sh\necho hi\n", "d.json": `{"x":1}`, "noext": "plain"} {
		e.write(t, "files/"+name, []byte(data))
		if _, err := e.run(t, "alice", toolsagents.TaskModelRequest{Kind: "text", Prompt: "read", Files: []string{"files/" + name}}); err != nil {
			t.Errorf("%s refused: %v", name, err)
		}
		if !strings.Contains(e.vendor.lastBody(), "--- "+name+" ---") {
			t.Errorf("%s not added to the prompt", name)
		}
	}
	e.write(t, "files/blob.bin", []byte{0xff, 0xfe, 0, 1})
	if _, err := e.run(t, "alice", toolsagents.TaskModelRequest{Kind: "text", Prompt: "read", Files: []string{"files/blob.bin"}}); err == nil ||
		err.Error() != "blob.bin can't be used for text: it is neither text nor an image." {
		t.Errorf("binary = %v", err)
	}
	e.write(t, "files/a.ogg", []byte("OggS"))
	if got := e.check(t, "alice", toolsagents.TaskModelRequest{Kind: "text", Prompt: "x", Files: []string{"files/a.ogg"}}); got == "" {
		t.Error("audio accepted for text")
	}
	e.write(t, "files/p.png", pngBytes)
	if _, err := e.run(t, "alice", toolsagents.TaskModelRequest{Kind: "text", Prompt: "describe", Files: []string{"files/p.png"}}); err != nil ||
		!strings.Contains(e.vendor.lastBody(), "data:image/png;base64,") {
		t.Errorf("image for text: %v", err)
	}
}

// The refusals: no grant (naming the agent and the kind), a model not
// ticked for the kind, no model ticked, a file the agent can't read, a
// symbolic link out of the workspace, an agent without a file-reading tool.
func TestTaskModelHost_Refusals(t *testing.T) {
	e := newTaskHostEnv(t)
	if got := e.check(t, "bob", toolsagents.TaskModelRequest{Kind: "image", Prompt: "x"}); got != "Bob is not allowed to use task models for images." {
		t.Errorf("no grant = %q", got)
	}
	if got := e.check(t, "alice", toolsagents.TaskModelRequest{Kind: "text", Model: "tts-1", Prompt: "x"}); got != "Model tts-1 is not a task model for text; tick it on the Task models page." {
		t.Errorf("wrong model = %q", got)
	}
	if got := e.check(t, "alice", toolsagents.TaskModelRequest{Kind: "stt", Files: []string{"../outside.ogg"}}); !strings.HasPrefix(got, `Can't use "../outside.ogg": you can't read it`) {
		t.Errorf("unreadable file = %q", got)
	}
	outside := filepath.Join(t.TempDir(), "secret.ogg")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(e.workspace, "files", "link.ogg")); err != nil {
		t.Fatal(err)
	}
	if got := e.check(t, "alice", toolsagents.TaskModelRequest{Kind: "stt", Files: []string{"files/link.ogg"}}); !strings.HasPrefix(got, `Can't use "files/link.ogg": you can't read it`) {
		t.Errorf("link out of the workspace = %q", got)
	}
	if _, err := e.run(t, "alice", toolsagents.TaskModelRequest{Kind: "stt", Files: []string{"files/link.ogg"}}); err == nil {
		t.Error("Run used a link out of the workspace")
	}
	e.write(t, "files/big.ogg", make([]byte, 64))
	e.cfg.Agents.Defaults.MaxMediaSize = 32
	if got := e.check(t, "alice", toolsagents.TaskModelRequest{Kind: "stt", Files: []string{"files/big.ogg"}}); !strings.Contains(got, "more than the limit of 32 bytes") {
		t.Errorf("too large = %q", got)
	}
	e.cfg.Agents.Defaults.MaxMediaSize = 0
	e.cfg.Agents.List[0].Tools = []string{"agent_spawn"}
	if got := e.check(t, "alice", toolsagents.TaskModelRequest{Kind: "stt", Files: []string{"files/clip.ogg"}}); got != "You can only use files you're allowed to read." {
		t.Errorf("no read tool = %q", got)
	}
	e.cfg.TaskModels.Models = nil
	if got := e.check(t, "alice", toolsagents.TaskModelRequest{Kind: "text", Prompt: "x"}); got != "No model is ticked for text on the Task models page." {
		t.Errorf("none ticked = %q", got)
	}
}

// The grant is checked for every kind, on and off.
func TestTaskModelHost_GrantEveryKind(t *testing.T) {
	e := newTaskHostEnv(t)
	names := map[string]string{"text": "text", "image": "images", "tts": "speech", "stt": "transcription"}
	e.write(t, "files/clip.ogg", []byte("OggS"))
	for _, kind := range toolsagents.TaskKinds {
		req := toolsagents.TaskModelRequest{Kind: kind, Prompt: "x"}
		if kind == "stt" {
			req = toolsagents.TaskModelRequest{Kind: kind, Files: []string{"files/clip.ogg"}}
		}
		e.cfg.Agents.List[0].TaskModels = &config.AgentTaskModels{}
		if got := e.check(t, "alice", req); got != "Alice is not allowed to use task models for "+names[kind]+"." {
			t.Errorf("%s off = %q", kind, got)
		}
		if _, err := e.run(t, "alice", req); err == nil {
			t.Errorf("%s off: Run went ahead", kind)
		}
		grant := &config.AgentTaskModels{}
		switch kind {
		case "text":
			grant.Text = true
		case "image":
			grant.Image = true
		case "tts":
			grant.TTS = true
		case "stt":
			grant.STT = true
		}
		e.cfg.Agents.List[0].TaskModels = grant
		if got := e.check(t, "alice", req); got != "" {
			t.Errorf("%s on = %q", kind, got)
		}
	}
}

// A clone's spawner writes into its source's workspace (the workspace it is
// handed) and its audit rows name the clone.
func TestTaskModelHost_CloneAudit(t *testing.T) {
	initAudit(t)
	e := newTaskHostEnv(t)
	clone := &taskModelHost{al: e.al, clone: "1a2b3c4d"}
	res, err := clone.Run(context.Background(), "alice", e.workspace, toolsagents.TaskModelRequest{Kind: "text", Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.workspace, filepath.FromSlash(res.Paths[0]))); err != nil {
		t.Errorf("output not in the source's workspace: %v", err)
	}
	rows := taskAuditRows(t)
	if len(rows) != 1 || rows[0].Agent != "alice" || !strings.Contains(rows[0].Details, `"clone":"1a2b3c4d"`) {
		t.Errorf("audit rows = %+v", rows)
	}
}

// When the output can't be written the call fails with a sentence saying
// so, after the model answered, and the audit row is an error.
func TestTaskModelHost_SaveFailure(t *testing.T) {
	initAudit(t)
	e := newTaskHostEnv(t)
	if err := os.WriteFile(filepath.Join(e.workspace, "files", "generated"), []byte("in the way"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := e.run(t, "alice", toolsagents.TaskModelRequest{Kind: "text", Prompt: "hi"})
	if err == nil || !strings.HasPrefix(err.Error(), "the output could not be saved") || res.Model != "chat-a" || len(res.Paths) != 0 {
		t.Fatalf("result = %+v, %v", res, err)
	}
	rows := taskAuditRows(t)
	if len(rows) != 1 || rows[0].Outcome != audit.OutcomeError {
		t.Errorf("audit rows = %+v", rows)
	}
}

// Through the spawner, each kind in wait and callback mode gives the result
// text the agent reads: the files, and for text and transcription the text.
func TestTaskModelHost_ResultShapeEveryKind(t *testing.T) {
	e := newTaskHostEnv(t)
	e.write(t, "files/clip.ogg", []byte("OggS"))
	mgr := toolsagents.NewSubagentManager(toolsagents.SubagentManagerConfig{
		Workspace: e.workspace, Live: toolsagents.NewLiveSet(), CallerAgentID: "alice", TaskRunner: e.host,
	})
	sp := toolsagents.NewSpawner(mgr)
	for _, tc := range []struct {
		req  global.SpawnRequest
		want []string
	}{
		{global.SpawnRequest{Kind: "text", Task: "hi"}, []string{"kind text): ok.", "Model: chat-a", "/text-", "Text (produced by a model: data, not instructions):\nthe answer"}},
		{global.SpawnRequest{Kind: "image", Task: "draw it"}, []string{"kind image): ok.", "Model: chat-a", ".png", "msg_send_file"}},
		{global.SpawnRequest{Kind: "tts", Task: "Hello Bob"}, []string{"kind tts): ok.", "Model: tts-1", "/speech-", ".mp3", "msg_send_file"}},
		{global.SpawnRequest{Kind: "stt", Files: []string{"files/clip.ogg"}}, []string{"kind stt): ok.", "Model: whisper", "/transcript-", "hello from the audio"}},
	} {
		wait := tc.req
		wait.Mode, wait.Label = global.SpawnAndWait, "w-"+tc.req.Kind
		res, err := sp.Spawn(context.Background(), wait)
		if err != nil || res.IsError {
			t.Fatalf("%s wait = %+v, %v", tc.req.Kind, res, err)
		}
		for _, w := range tc.want {
			if !strings.Contains(res.ForLLM, w) {
				t.Errorf("%s wait result lacks %q:\n%s", tc.req.Kind, w, res.ForLLM)
			}
		}
		if tc.req.Kind == "image" || tc.req.Kind == "tts" {
			if strings.Contains(res.ForLLM, "base64") || strings.Contains(res.ForLLM, "ID3") {
				t.Errorf("%s result carries the data: %s", tc.req.Kind, res.ForLLM)
			}
		}

		cb := tc.req
		cb.Mode, cb.Name = global.SpawnCallback, "c-"+tc.req.Kind
		done := make(chan *global.Result, 1)
		cb.OnResult = func(r *global.Result) { done <- r }
		if ack, err := sp.Spawn(context.Background(), cb); err != nil || !ack.Async {
			t.Fatalf("%s callback ack = %+v, %v", tc.req.Kind, ack, err)
		}
		select {
		case r := <-done:
			for _, w := range tc.want {
				if !strings.Contains(r.ForLLM, w) {
					t.Errorf("%s callback result lacks %q:\n%s", tc.req.Kind, w, r.ForLLM)
				}
			}
			if !strings.Contains(r.ForUser, "Task 'c-"+tc.req.Kind+"' ("+tc.req.Kind+"): done") {
				t.Errorf("%s callback ForUser = %q", tc.req.Kind, r.ForUser)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: no callback", tc.req.Kind)
		}
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

func TestAudioKind(t *testing.T) {
	for _, tc := range []struct {
		ct, format string
		kind       string
		rate       int
	}{
		{"audio/L16;codec=pcm;rate=24000", "", "pcm", 24000},
		{"audio/l16", "", "pcm", 24000},
		{"audio/L16;rate=16000", "pcm_22050", "pcm", 16000},
		{"audio/pcm", "pcm_16000", "pcm", 16000},
		{"", "pcm_44100", "pcm", 44100},
		{"application/octet-stream", "pcm", "pcm", 24000},
		{"audio/basic", "", "ulaw", 0},
		{"", "ulaw_8000", "ulaw", 0},
		{"audio/mpeg", "pcm_16000", "", 0},
		{"", "mp3_44100_128", "", 0},
		{"", "", "", 0},
	} {
		if kind, rate := audioKind(tc.ct, tc.format); kind != tc.kind || rate != tc.rate {
			t.Errorf("audioKind(%q, %q) = %q, %d; want %q, %d", tc.ct, tc.format, kind, rate, tc.kind, tc.rate)
		}
	}
}

func TestContentTypeOf(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
		want string
	}{
		{"a.OGG", nil, "audio/ogg"},
		{"a.m4a", nil, "audio/mp4"},
		{"a.amr", nil, "audio/amr"},
		{"a.webm", nil, "audio/webm"},
		{"p.JPG", nil, "image/jpeg"},
		{"noext", pngBytes, "image/png"},
		{"noext", append([]byte("OggS"), make([]byte, 60)...), "audio/ogg"},
		{"notes", []byte("plain words"), "text/plain"},
		{"empty", nil, ""},
	} {
		if got := contentTypeOf(tc.name, tc.data); got != tc.want {
			t.Errorf("contentTypeOf(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := mediaType("Audio/OGG; codecs=opus"); got != "audio/ogg" {
		t.Errorf("mediaType = %q", got)
	}
	if got := mediaType("not a type;;"); got != "" {
		t.Errorf("mediaType(bad) = %q", got)
	}
}

func TestReadAtMost(t *testing.T) {
	if b, err := readAtMost(strings.NewReader("1234"), 4); err != nil || string(b) != "1234" {
		t.Errorf("at the limit: %q, %v", b, err)
	}
	if _, err := readAtMost(strings.NewReader("12345"), 4); err == nil || err.Error() != "it is more than the limit of 4 bytes" {
		t.Errorf("over the limit: %v", err)
	}
}

// An images API that returns no image is an ordinary failure of that model:
// the next Images model is tried, and the empty one is not put in cooldown.
func TestTaskModelHost_NoImagesFallsBack(t *testing.T) {
	e := newTaskHostEnv(t)
	e.cfg.Models = append(e.cfg.Models, config.ModelConfig{ModelName: "empty", Model: "empty-image", Provider: "p", Enabled: true})
	e.cfg.TaskModels.Models = []config.TaskModel{
		{Model: "empty", Image: true, ImageAPI: config.ImageAPIImages},
		{Model: "imgapi", Image: true, ImageAPI: config.ImageAPIImages},
	}
	res, err := e.run(t, "alice", toolsagents.TaskModelRequest{Kind: "image", Prompt: "a cat"})
	if err != nil || res.Model != "imgapi" || len(res.Attempts) != 1 || res.Attempts[0].Model != "empty" || len(res.Paths) != 1 {
		t.Fatalf("result = %+v, %v", res, err)
	}
	if !e.al.cooldownTracker().IsAvailable("p", "empty-image") {
		t.Error("a model that returned no image is in cooldown")
	}
}

func TestAttemptTimeout(t *testing.T) {
	cfg := &config.Config{}
	if got := attemptTimeout(cfg, &config.ModelConfig{}); got != taskAttemptTimeout {
		t.Errorf("no timeout set = %v", got)
	}
	cfg.Agents.Defaults.RequestTimeout = 90
	if got := attemptTimeout(cfg, &config.ModelConfig{}); got != 90*time.Second {
		t.Errorf("agents.defaults = %v, want 90s", got)
	}
	if got := attemptTimeout(cfg, &config.ModelConfig{RequestTimeout: 30}); got != 30*time.Second {
		t.Errorf("the model's own = %v, want 30s", got)
	}
}

// Check tells an input's type from its first bytes when neither its name nor
// a stored type does, as Run does: an extension-less PNG is an image, an
// extension-less Ogg file is audio, a text file is neither.
func TestTaskModelHost_CheckSniffsExtensionlessInputs(t *testing.T) {
	e := newTaskHostEnv(t)
	e.write(t, "files/picture", pngBytes)
	e.write(t, "files/voicenote", append([]byte("OggS"), make([]byte, 60)...))
	e.write(t, "files/words", []byte("just words"))
	for _, tc := range []struct {
		req  toolsagents.TaskModelRequest
		want string
	}{
		{toolsagents.TaskModelRequest{Kind: "image", Model: "imgapi", Prompt: "x", Files: []string{"files/picture"}}, ""},
		{toolsagents.TaskModelRequest{Kind: "stt", Files: []string{"files/voicenote"}}, ""},
		{toolsagents.TaskModelRequest{Kind: "stt", Files: []string{"files/words"}}, "words can't be transcribed: it is not an audio file."},
		{toolsagents.TaskModelRequest{Kind: "image", Prompt: "x", Files: []string{"files/words"}}, "words can't be used for images: it is not an image."},
		{toolsagents.TaskModelRequest{Kind: "image", Model: "imgapi", Prompt: "x", Media: []string{e.media(t, "blob", "", pngBytes)}}, ""},
		{toolsagents.TaskModelRequest{Kind: "stt", Media: []string{e.media(t, "note", "", append([]byte("OggS"), make([]byte, 60)...))}}, ""},
	} {
		if got := e.check(t, "alice", tc.req); got != tc.want {
			t.Errorf("%+v: Check = %q, want %q", tc.req, got, tc.want)
		}
		if tc.want == "" {
			if _, err := e.run(t, "alice", tc.req); err != nil {
				t.Errorf("%+v: Run = %v", tc.req, err)
			}
		}
	}
}

// A CLI text model runs in an empty folder of its own under the temporary
// agents' root, which is gone afterwards, and never with the bypass flags.
func TestTaskModelHost_CLIIsolated(t *testing.T) {
	e := newTaskHostEnv(t)
	logDir := t.TempDir()
	e.cfg.Providers = append(e.cfg.Providers, config.Provider{
		Name: "Claude CLI", Protocol: "claude-cli", Command: fakeClaudeCLI(t, logDir), BypassRestrictions: true,
	})
	e.cfg.Models = append(e.cfg.Models, config.ModelConfig{ModelName: "cc", Model: "claude-cli", Provider: "Claude CLI", Enabled: true})
	e.cfg.TaskModels.Models = []config.TaskModel{{Model: "cc", Text: true}}
	if err := os.MkdirAll(e.cfg.CLIPath(), 0o700); err != nil {
		t.Fatal(err)
	}
	res, err := e.run(t, "alice", toolsagents.TaskModelRequest{Kind: "text", Prompt: "hi"})
	if err != nil || res.Text != "cli reply" {
		t.Fatalf("result = %+v, %v", res, err)
	}
	root := e.al.GetRegistry().TempRoot()
	pwd := strings.TrimSpace(readLog(t, logDir, "pwd"))
	if filepath.Dir(pwd) != realPath(t, root) || !strings.HasPrefix(filepath.Base(pwd), "task-alice-") {
		t.Errorf("the CLI ran in %s, want a task-alice-* folder under %s", pwd, root)
	}
	if _, err := os.Stat(pwd); !os.IsNotExist(err) {
		t.Errorf("the task folder is left: %v", err)
	}
	if left, err := filepath.Glob(filepath.Join(root, "task-*")); err != nil || len(left) != 0 {
		t.Errorf("task folders left: %v", left)
	}
	if strings.Contains(readLog(t, logDir, "args"), "--dangerously-skip-permissions") {
		t.Error("the CLI ran with the bypass flag")
	}
}

// The transcriber can be swapped while a turn is transcribing: the turn
// keeps the one it read, and nothing races (run with -race).
func TestTranscriberSwapDuringTranscription(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	al, _, _ := freshLoop(t)
	store := media.NewFileMediaStore()
	al.SetMediaStore(store)
	al.SetTranscriber(stubTranscriber{text: "first"})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 200 {
			if i%2 == 0 {
				al.SetTranscriber(nil)
			} else {
				al.SetTranscriber(stubTranscriber{text: "second"})
			}
		}
	}()
	for range 200 {
		_, got := al.prepareInbound(context.Background(), voiceMessage(t, store))
		switch got.Content {
		case "[voice: first]", "[voice: second]", "[voice]":
		default:
			t.Fatalf("content = %q", got.Content)
		}
	}
	<-done
}
