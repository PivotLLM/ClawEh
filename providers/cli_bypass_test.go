// ClawEh
// License: MIT

package providers

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/testalerts"
	"github.com/PivotLLM/ClawEh/logger"
)

// unwrapCLI returns the spawnllm provider behind the declined-tools guard, or
// p itself when it is not wrapped (bypass on).
func unwrapCLI(p LLMProvider) LLMProvider {
	if g, ok := p.(*cliDeclinedGuard); ok {
		return g.LLMProvider
	}
	return p
}

// scriptedProvider stands in for a CLI provider and answers with whatever the
// test planted.
type scriptedProvider struct {
	resp *LLMResponse
	err  error
}

func (s *scriptedProvider) Chat(context.Context, []Message, []ToolDefinition, string, map[string]any) (*LLMResponse, error) {
	return s.resp, s.err
}
func (s *scriptedProvider) GetDefaultModel() string { return "scripted" }

const declinedText = "The Claude CLI declined to use tools. Tick *Bypass CLI restrictions* for this CLI in the WebUI, or allow the tools in the CLI's own settings."

// With bypass off, the CLI runs without its permission-bypass flag and is
// wrapped so an empty answer — what a CLI returns when it refuses a tool call —
// reaches the user as an error naming the setting. With bypass on, the flag is
// passed and the provider is returned as is.
func TestNewCLIProvider_BypassSettingDecidesFlagAndGuard(t *testing.T) {
	model := &config.ModelConfig{ModelName: "c", Model: "claude-cli", Provider: "Claude CLI"}

	off, _, err := CreateProviderFromConfig(model, &config.Provider{Name: "Claude CLI", Protocol: "claude-cli"})
	if err != nil {
		t.Fatal(err)
	}
	guard, ok := off.(*cliDeclinedGuard)
	if !ok {
		t.Fatalf("bypass off produced %T, want the declined-tools guard", off)
	}
	if _, isCLI := off.(CLIProvider); !isCLI {
		t.Error("the guard must still count as a CLI provider for the agent loop")
	}
	if _, ok := guard.LLMProvider.(*ClaudeCliProvider); !ok {
		t.Errorf("guard wraps %T, want *ClaudeCliProvider", guard.LLMProvider)
	}

	on, _, err := CreateProviderFromConfig(model, &config.Provider{Name: "Claude CLI", Protocol: "claude-cli", BypassRestrictions: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := on.(*ClaudeCliProvider); !ok {
		t.Errorf("bypass on produced %T, want the bare *ClaudeCliProvider", on)
	}
}

// A refusal is reported to the user through the error, never as an alert:
// the CLI did what its settings say.
func TestCLIDeclinedGuard_Chat(t *testing.T) {
	deniedText := "The Claude CLI declined to use tools (maestro_file_get, microsoft365_mail_read_inbox, Bash). Tick *Bypass CLI restrictions* for this CLI in the WebUI, or allow the tools in the CLI's own settings."
	tests := []struct {
		name    string
		resp    *LLMResponse
		err     error
		wantErr string // "" means the inner result passes through unchanged
	}{
		{
			name:    "an empty answer becomes the declined-tools error",
			resp:    &LLMResponse{Content: "  "},
			wantErr: declinedText,
		},
		{
			// The Claude CLI answers in prose and lists the refused calls in
			// permission_denials; the message and the alert name them.
			name: "refused calls in the status become the error, naming the tools",
			resp: &LLMResponse{
				Content: "I couldn't run this morning's email brief.",
				Status:  &DispatchStatus{Success: true, DeniedTools: []string{"mcp__claw__maestro_file_get", "mcp__claw__microsoft365_mail_read_inbox", "Bash"}},
			},
			wantErr: deniedText,
		},
		{
			// agy names the denied action; keep that detail behind the message.
			name:    "a denied-action error is prefixed with the message",
			resp:    &LLMResponse{},
			err:     errors.New("antigravity cli denied RunCommand and produced no answer"),
			wantErr: declinedText + ": antigravity cli denied RunCommand and produced no answer",
		},
		{name: "a real answer passes through", resp: &LLMResponse{Content: "hello"}},
		{name: "an unrelated error passes through", resp: nil, err: errors.New("exit status 1")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := testalerts.Install(t)
			g := &cliDeclinedGuard{LLMProvider: &scriptedProvider{resp: tc.resp, err: tc.err}, label: "Claude CLI", protocol: "claude-cli"}
			resp, err := g.Chat(context.Background(), nil, nil, "m", nil)
			if resp != tc.resp {
				t.Errorf("response replaced: got %v, want the inner %v", resp, tc.resp)
			}
			if tc.wantErr == "" {
				if !errors.Is(err, tc.err) {
					t.Errorf("err = %v, want the inner %v", err, tc.err)
				}
				return
			}
			if err == nil || err.Error() != tc.wantErr {
				t.Errorf("err = %v\nwant %q", err, tc.wantErr)
			}
			// The failover renderer finds the message by this type.
			declined, ok := errors.AsType[*CLIDeclinedError](err)
			if !ok {
				t.Fatalf("err is %T, want *CLIDeclinedError", err)
			}
			wantMsg := declinedText
			if tc.resp != nil && tc.resp.Status != nil && len(tc.resp.Status.DeniedTools) > 0 {
				wantMsg = deniedText
			}
			if declined.Message != wantMsg {
				t.Errorf("declined.Message = %q, want %q", declined.Message, wantMsg)
			}
			if n := len(rec.Alerts()); n != 0 {
				t.Errorf("a refusal raised %d alert(s), want none: %+v", n, rec.Alerts())
			}
			if tc.err != nil && !errors.Is(err, tc.err) {
				t.Error("the inner error is no longer in the chain")
			}
		})
	}
}

// One INFO line per CLI provider with bypass on, so the startup log states
// which CLIs may act without asking. Nothing for HTTP providers or CLIs with it
// off, and no alert either way: a CLI without the flag is running as directed.
func TestNewProviderDispatcher_LogsBypassEnabled(t *testing.T) {
	var buf bytes.Buffer
	restore := logger.RedirectForTest(&buf)
	defer restore()

	cfg := config.DefaultConfig()
	cfg.Providers = []config.Provider{
		{Name: "Codex CLI", Protocol: "codex-cli", BypassRestrictions: true},
		{Name: "Cursor CLI", Protocol: "cursor-cli"},
		{Name: "OpenAI", Protocol: "openai-chat", BaseURL: "https://api.openai.com/v1", BypassRestrictions: true},
	}
	rec := testalerts.Install(t)
	NewProviderDispatcher(cfg)

	out := buf.String()
	if strings.Count(out, "Bypass CLI restrictions is on") != 1 || !strings.Contains(out, "Codex CLI") {
		t.Errorf("want exactly one line, for Codex CLI:\n%s", out)
	}
	if strings.Contains(out, "Cursor CLI") || strings.Contains(out, "OpenAI") {
		t.Errorf("logged a provider without bypass, or a non-CLI:\n%s", out)
	}
	if n := len(rec.Alerts()); n != 0 {
		t.Errorf("building the dispatcher raised %d alert(s), want none: %+v", n, rec.Alerts())
	}
}
