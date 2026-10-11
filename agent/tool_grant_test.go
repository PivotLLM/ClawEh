// ClawEh
// License: MIT

package agent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
)

// TestAgentTools_OnlyWhatTheListNames: an agent gets only the native tools its
// tools list names. No tools key and an empty list both give none, so its
// registry, which the loop offers the model and the MCP host serves to its
// session and service tokens, is empty. Suites are off here: they have their
// own switches.
func TestAgentTools_OnlyWhatTheListNames(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))
	off := false
	cfg := messagingConfig(t)
	for i := range cfg.Agents.List {
		cfg.Agents.List[i].Cogmem = &off
	}
	cfg.Agents.List[0].Tools = nil        // alice: no tools key
	cfg.Agents.List[1].Tools = []string{} // bob: empty list
	cfg.Agents.List[2].Tools = []string{"agent_message"}
	al, _ := messagingLoop(t, cfg, &countingProvider{})

	for _, id := range []string{"alice", "bob"} {
		inst, _ := al.GetRegistry().Get(id)
		if n := inst.Tools.Count(); n != 0 {
			t.Errorf("%s has %d tools, want none: %v", id, n, inst.Tools.List())
		}
		if _, ok := inst.Tools.GetForHost("agent_message"); ok {
			t.Errorf("%s reaches agent_message through the MCP host", id)
		}
		res := inst.Tools.ExecuteForHost(context.Background(), "agent_message",
			map[string]any{"agent": "bob", "message": "hi"}, "cli", "direct", nil)
		if !res.IsError {
			t.Errorf("%s ran agent_message through the MCP host: %+v", id, res)
		}
	}
	helper, _ := al.GetRegistry().Get("helper")
	if got := helper.Tools.List(); !slices.Equal(got, []string{"agent_message"}) {
		t.Errorf("helper tools = %v, want exactly [agent_message]", got)
	}
}

// noToolsConfig is messagingConfig with every suite off, Alice with no tools
// and Helper with one.
func noToolsConfig(t *testing.T) *config.Config {
	t.Helper()
	off := false
	cfg := messagingConfig(t)
	for i := range cfg.Agents.List {
		cfg.Agents.List[i].Cogmem = &off
	}
	cfg.Agents.List[0].Tools = nil
	cfg.Agents.List[2].Tools = []string{"agent_message"}
	// A configured model, so a fresh temporary agent can be created.
	cfg.Providers = []config.Provider{{Name: "p", Protocol: "openai-chat", BaseURL: "http://127.0.0.1:0/v1", APIKey: "k"}}
	cfg.Models = []config.ModelConfig{{ModelName: "alpha", Model: "alpha-wire", Provider: "p", Enabled: true}}
	cfg.Agents.Defaults.Models = []string{"alpha"}
	return cfg
}

// promptText is the agent's whole system prompt for one turn.
func promptText(t *testing.T, inst *AgentInstance) string {
	t.Helper()
	var sb strings.Builder
	for _, l := range inst.ContextBuilder.PromptLayers("telegram", "chat-1") {
		sb.WriteString(l.Text)
	}
	return sb.String()
}

// TestAgentTools_NoToolsNotice: an agent with no tools at all is told so in
// its prompt; one with a tool, and a fresh temporary agent (whose prompt is
// its creator's), are not. A reload that grants a tool removes the notice.
func TestAgentTools_NoToolsNotice(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))
	cfg := noToolsConfig(t)
	al, _ := messagingLoop(t, cfg, &countingProvider{})

	alice, _ := al.GetRegistry().Get("alice")
	if !strings.Contains(promptText(t, alice), noToolsNotice) {
		t.Error("alice has no tools but her prompt lacks the notice")
	}
	helper, _ := al.GetRegistry().Get("helper")
	if strings.Contains(promptText(t, helper), noToolsNotice) {
		t.Error("helper has a tool but his prompt has the notice")
	}

	id, err := al.GetRegistry().CreateFresh(config.AgentConfig{Models: []string{"alpha"}})
	if err != nil {
		t.Fatalf("CreateFresh: %v", err)
	}
	fresh, _ := al.GetRegistry().Get(id)
	if fresh.Tools.Count() != 0 {
		t.Fatalf("the fresh agent has tools: %v", fresh.Tools.List())
	}
	if strings.Contains(promptText(t, fresh), noToolsNotice) {
		t.Error("a fresh temporary agent's prompt has the notice")
	}

	next := noToolsConfig(t)
	next.Agents.BaseDir = cfg.Agents.BaseDir
	next.Agents.List[0].Tools = []string{"agent_message"}
	if err := al.ReloadProviderAndConfig(context.Background(), &countingProvider{}, next); err != nil {
		t.Fatalf("reload: %v", err)
	}
	alice, _ = al.GetRegistry().Get("alice")
	if strings.Contains(promptText(t, alice), noToolsNotice) {
		t.Error("the notice stayed after a reload granted alice a tool")
	}
}

// TestAgentSkills_OnlyWhatTheListNames: an agent sees only the skills its
// skills list names; no skills key and an empty list both give none.
func TestAgentSkills_OnlyWhatTheListNames(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))
	cfg := messagingConfig(t)
	cfg.Agents.List[0].Skills = nil        // alice: no skills key
	cfg.Agents.List[1].Skills = []string{} // bob: empty list
	cfg.Agents.List[2].Skills = []string{"weather"}
	al, _ := messagingLoop(t, cfg, &countingProvider{})

	for _, id := range []string{"alice", "bob", "helper"} {
		inst, _ := al.GetRegistry().Get(id)
		for _, name := range []string{"weather", "news"} {
			dir := filepath.Join(inst.Workspace, "skills", name)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			body := "---\nname: " + name + "\ndescription: the " + name + " skill\n---\n# " + name + "\n"
			if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	want := map[string][]string{"alice": {}, "bob": {}, "helper": {"weather"}}
	for id, names := range want {
		inst, _ := al.GetRegistry().Get(id)
		got, ok := inst.ContextBuilder.GetSkillsInfo()["names"].([]string)
		if !ok {
			t.Fatalf("%s: skill names missing", id)
		}
		if !slices.Equal(got, names) {
			t.Errorf("%s skills = %v, want %v", id, got, names)
		}
	}
}
