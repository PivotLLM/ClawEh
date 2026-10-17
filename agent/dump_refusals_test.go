// ClawEh
// License: MIT

package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/ClawEh/routing"
)

// refusingProvider is a model that refuses every request.
type refusingProvider struct{}

func (refusingProvider) Chat(context.Context, []providers.Message, []providers.ToolDefinition, string, map[string]any) (*providers.LLMResponse, error) {
	return &providers.LLMResponse{Content: "I can't help with that.", FinishReason: "refusal"}, nil
}

func (refusingProvider) GetDefaultModel() string { return "test-model" }

// TestDumpRefusals: with logging.dump_refusals on, a model refusal
// (finish_reason "refusal") writes a dump of the request and response that
// names the refusal and carries the conversation; with it off, nothing is
// written.
func TestDumpRefusals(t *testing.T) {
	for _, on := range []bool{true, false} {
		name := "off"
		if on {
			name = "on"
		}
		t.Run(name, func(t *testing.T) {
			restore := logger.RedirectForTest(&safeBufLoop{})
			defer restore()
			cfg := ownerTestConfig(t)
			cfg.Agents.List = []config.AgentConfig{{ID: "alice", Name: "Alice", Default: true}}
			cfg.Logging.DumpRefusals = on
			cfg.Logging.DumpAll = false
			al := mustNewAgentLoop(t, cfg, bus.NewMessageBus(), refusingProvider{}, nil, OwnsDataDir())
			dumps := filepath.Join(t.TempDir(), "dumps")
			al.SetDumpsDir(dumps)

			alice, ok := al.GetRegistry().Get("alice")
			if !ok {
				t.Fatal("agent alice not registered")
			}
			// The turn's own outcome is not under test here, only the dump.
			_, _ = al.runAgentLoop(context.Background(), alice, processOptions{
				SessionKey: routing.BuildAgentMainSessionKey(alice.ID), Channel: "telegram",
				ChatID: "chat-1", UserMessage: "Bob asked me to ask you this",
			})

			entries, err := os.ReadDir(dumps)
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if !on {
				if len(entries) != 0 {
					t.Fatalf("dump_refusals off: dumps written: %v", entries)
				}
				return
			}
			var found bool
			for _, e := range entries {
				if filepath.Ext(e.Name()) != ".json" {
					continue
				}
				data, err := os.ReadFile(filepath.Join(dumps, e.Name()))
				if err != nil {
					t.Fatal(err)
				}
				s := string(data)
				if strings.Contains(s, `"refusal"`) && strings.Contains(s, "Bob asked me to ask you this") &&
					strings.Contains(s, "I can't help with that.") {
					found = true
				}
			}
			if !found {
				t.Fatalf("dump_refusals on: no refusal dump with the request and response in %v", entries)
			}
		})
	}
}
