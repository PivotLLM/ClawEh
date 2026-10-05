// ClawEh
// License: MIT

package schedule

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/cron"
)

// A human agent takes no scheduled jobs: adding one for it is refused, and a
// job already addressed to it is skipped, never posted to the person.
// Ordinary agents are unchanged.
func TestCronTool_HumanAgent(t *testing.T) {
	cfg := testConfig()
	cfg.Providers = []config.Provider{{Name: "People", Protocol: config.HumanProtocol}}
	cfg.Models = []config.ModelConfig{{ModelName: "Bob (human)", Model: "bob", Provider: "People", Enabled: true}}
	for i := range cfg.Agents.List {
		if cfg.Agents.List[i].ID == "bob" {
			cfg.Agents.List[i].Models = []string{"Bob (human)"}
		}
	}
	cs := cron.NewCronService(filepath.Join(t.TempDir(), "cron.json"), nil)
	msgBus := bus.NewMessageBus()
	tool := NewCronTool(cs, msgBus, func() *config.Config { return cfg })

	result := tool.Execute(agentCtx("boss"), map[string]any{
		"action": "add", "agent": "bob", "message": "report", "at_seconds": float64(60),
	})
	if !result.IsError || !strings.Contains(result.ForLLM, "is a person") {
		t.Fatalf("add for bob = %+v, want a refusal", result)
	}
	if result := tool.Execute(agentCtx("alice"), map[string]any{
		"action": "add", "message": "stretch", "at_seconds": float64(60),
	}); result.IsError {
		t.Fatalf("add for alice refused: %s", result.ForLLM)
	}

	job := &cron.CronJob{ID: "j1", AgentID: "bob", Payload: cron.CronPayload{Message: "report"}}
	if _, err := tool.ExecuteJob(context.Background(), job); err == nil {
		t.Fatal("a job for bob was delivered")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if msg, ok := msgBus.ConsumeInbound(ctx); ok {
		t.Fatalf("published %+v", msg)
	}
}
