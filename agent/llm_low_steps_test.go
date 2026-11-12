// ClawEh
// License: MIT

package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/providers"
)

func TestLowStepsNote(t *testing.T) {
	const lastStep = "[System notice] This is your last step in this turn. Do not call tools; reply now with your findings and next steps."
	tests := []struct {
		name          string
		iteration     int
		maxIterations int
		want          string
	}{
		{name: "five remaining", iteration: 6, maxIterations: 10, want: ""},
		{name: "four remaining", iteration: 7, maxIterations: 10, want: "[System notice] You have 3 tool steps left in this turn. Save any work in progress and give your reply with your findings and next steps before they run out."},
		{name: "three remaining", iteration: 8, maxIterations: 10, want: "[System notice] You have 2 tool steps left in this turn. Save any work in progress and give your reply with your findings and next steps before they run out."},
		{name: "two remaining", iteration: 9, maxIterations: 10, want: "[System notice] You have 1 tool step left in this turn. Save any work in progress and give your reply with your findings and next steps before it runs out."},
		{name: "one remaining", iteration: 10, maxIterations: 10, want: lastStep},
		{name: "small cap warns on the first iteration", iteration: 1, maxIterations: 1, want: lastStep},
		{name: "past the cap", iteration: 11, maxIterations: 10, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := lowStepsNote(tt.iteration, tt.maxIterations); got != tt.want {
				t.Errorf("lowStepsNote(%d, %d) = %q, want %q", tt.iteration, tt.maxIterations, got, tt.want)
			}
		})
	}
}

// cliChatFunc is a scripted model that reports itself as a CLI provider.
type cliChatFunc struct{ chatFunc }

func (cliChatFunc) IsCLI() bool { return true }

// lowStepsTurn runs a six-step turn in which the model calls a tool on every
// step, and returns each request the model was sent and the stored history.
// fallback routes the turn through the fallback chain (chatCandidate) rather
// than straight to the provider; cli makes the model a CLI provider; noTools
// offers the model no tools.
func lowStepsTurn(t *testing.T, fallback, cli, noTools bool) ([][]providers.Message, []providers.Message) {
	t.Helper()
	al := newTestAgentLoop(t).al
	agentInstance := al.registry.Default()
	if agentInstance == nil {
		t.Fatal("no default agent")
	}
	agentInstance.Tools.Register(&mockEchoTool{})
	if agentInstance.Config != nil {
		agentInstance.Config.Tools = []string{"*"}
	}
	agentInstance.MaxIterations = 6
	agentInstance.NoTools = noTools
	// No dispatcher: both paths call agentInstance.Provider.
	al.dispatcher = nil
	if fallback {
		agentInstance.Candidates = []providers.FallbackCandidate{{Provider: "mock", Model: "test-model"}}
	} else {
		agentInstance.Candidates = nil
	}

	var (
		mu       sync.Mutex
		requests [][]providers.Message
	)
	model := chatFunc(func(_ context.Context, messages []providers.Message) (*providers.LLMResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		requests = append(requests, append([]providers.Message(nil), messages...))
		// Distinct arguments per call, so the repeated-batch guard never stops
		// the turn early.
		args := fmt.Sprintf(`{"text":"step %d"}`, len(requests))
		return &providers.LLMResponse{ToolCalls: []providers.ToolCall{{
			ID:       fmt.Sprintf("tc-%d", len(requests)),
			Type:     "function",
			Name:     "echo_tool",
			Function: &providers.FunctionCall{Name: "echo_tool", Arguments: args},
		}}}, nil
	})
	if cli {
		agentInstance.Provider = cliChatFunc{model}
	} else {
		agentInstance.Provider = model
	}

	opts := processOptions{
		SessionKey:  "low-steps",
		Channel:     "cli",
		ChatID:      "direct",
		UserMessage: "go",
	}
	cm, releaseCM := al.getContextManager(agentInstance, opts.SessionKey)
	defer releaseCM()

	messages := []providers.Message{{Role: "user", Content: "go"}}
	if _, err := al.runLLMIteration(context.Background(), agentInstance, messages, opts, cm, nil); err != nil {
		t.Fatalf("runLLMIteration: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 6 {
		t.Fatalf("model called %d times, want 6", len(requests))
	}
	return requests, agentInstance.Sessions.GetHistory(opts.SessionKey)
}

// lowStepsNotes are the low-steps notes a request carries.
func lowStepsNotes(req []providers.Message) []string {
	var notes []string
	for _, m := range req {
		if strings.HasPrefix(m.Content, "[System notice]") {
			notes = append(notes, m.Content)
		}
	}
	return notes
}

// TestRunLLMIteration_LowStepsNotePerRequest checks, on the direct path and
// through the fallback chain, that each request carries the note for its own
// step count only, as its trailing message, and that the note never reaches
// the stored history.
func TestRunLLMIteration_LowStepsNotePerRequest(t *testing.T) {
	for _, tc := range []struct {
		name     string
		fallback bool
	}{
		{name: "direct", fallback: false},
		{name: "fallback chain", fallback: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests, history := lowStepsTurn(t, tc.fallback, false, false)
			for i, req := range requests {
				want := lowStepsNote(i+1, 6)
				notes := lowStepsNotes(req)
				switch {
				case want == "" && len(notes) != 0:
					t.Errorf("request %d carries notes %q, want none", i+1, notes)
				case want != "":
					if len(notes) != 1 || notes[0] != want {
						t.Errorf("request %d carries notes %q, want exactly %q", i+1, notes, want)
					}
					if last := req[len(req)-1]; last.Role != "user" || last.Content != want {
						t.Errorf("request %d ends with %s %q, want the note as a trailing user message", i+1, last.Role, last.Content)
					}
				}
			}
			// Requests 3 to 6 are the last four steps of the turn.
			for i, want := range []string{"3 tool steps left", "2 tool steps left", "1 tool step left", "last step"} {
				if got := requests[i+2][len(requests[i+2])-1].Content; !strings.Contains(got, want) {
					t.Errorf("request %d note = %q, want it to mention %q", i+3, got, want)
				}
			}
			for _, m := range history {
				if strings.Contains(m.Content, "[System notice]") {
					t.Errorf("stored history contains the note: %q", m.Content)
				}
			}
		})
	}
}

// TestRunLLMIteration_LowStepsNoteSkipped checks that no request carries the
// note when the model is offered no tools or runs on a CLI provider, on the
// direct path and through the fallback chain.
func TestRunLLMIteration_LowStepsNoteSkipped(t *testing.T) {
	for _, tc := range []struct {
		name     string
		fallback bool
		cli      bool
		noTools  bool
	}{
		{name: "no tools, direct", noTools: true},
		{name: "no tools, fallback chain", fallback: true, noTools: true},
		{name: "CLI provider, direct", cli: true},
		{name: "CLI provider, fallback chain", fallback: true, cli: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests, _ := lowStepsTurn(t, tc.fallback, tc.cli, tc.noTools)
			for i, req := range requests {
				if notes := lowStepsNotes(req); len(notes) != 0 {
					t.Errorf("request %d carries notes %q, want none", i+1, notes)
				}
			}
		})
	}
}

func TestResolveRunLimits_MaxToolIterations(t *testing.T) {
	intPtr := func(v int) *int { return &v }
	tests := []struct {
		name     string
		defaults int
		agent    *config.AgentConfig
		want     int
	}{
		{name: "no agent config uses the default", defaults: 50, agent: nil, want: 50},
		{name: "unset uses the default", defaults: 50, agent: &config.AgentConfig{ID: "alice"}, want: 50},
		{name: "set overrides the default", defaults: 50, agent: &config.AgentConfig{ID: "alice", MaxToolIterations: intPtr(120)}, want: 120},
		{name: "set below the default overrides it", defaults: 50, agent: &config.AgentConfig{ID: "alice", MaxToolIterations: intPtr(5)}, want: 5},
		{name: "zero uses the default", defaults: 50, agent: &config.AgentConfig{ID: "alice", MaxToolIterations: intPtr(0)}, want: 50},
		{name: "negative uses the default", defaults: 50, agent: &config.AgentConfig{ID: "alice", MaxToolIterations: intPtr(-3)}, want: 50},
		{name: "set with no default", defaults: 0, agent: &config.AgentConfig{ID: "alice", MaxToolIterations: intPtr(7)}, want: 7},
		{name: "neither set uses the built-in fallback", defaults: 0, agent: &config.AgentConfig{ID: "alice"}, want: 20},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defaults := &config.AgentDefaults{MaxToolIterations: tt.defaults}
			got := resolveRunLimits(defaults, tt.agent, &config.Config{}, "test-model")
			if got.maxIterations != tt.want {
				t.Errorf("maxIterations = %d, want %d", got.maxIterations, tt.want)
			}
		})
	}
}
