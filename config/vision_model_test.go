// ClawEh
// License: MIT

package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// The vision-describe side-model fields parse from JSON and round-trip back out.
func TestVisionModelConfig_RoundTrip(t *testing.T) {
	in := `{"agents":{"defaults":{"vision_model":"gpt4o","vision_model_fallbacks":["claude-vision","gemini-vision"]}}}`
	var c Config
	if err := json.Unmarshal([]byte(in), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if c.Agents.Defaults.VisionModel != "gpt4o" {
		t.Fatalf("VisionModel = %q, want gpt4o", c.Agents.Defaults.VisionModel)
	}
	if got := c.Agents.Defaults.VisionModelFallbacks; len(got) != 2 || got[0] != "claude-vision" || got[1] != "gemini-vision" {
		t.Fatalf("VisionModelFallbacks = %v", got)
	}

	out, err := json.Marshal(c.Agents.Defaults)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(out), `"vision_model":"gpt4o"`) {
		t.Fatalf("marshalled defaults missing vision_model: %s", out)
	}
	if !strings.Contains(string(out), `"vision_model_fallbacks":["claude-vision","gemini-vision"]`) {
		t.Fatalf("marshalled defaults missing vision_model_fallbacks: %s", out)
	}
}

// Unset vision fields are omitted (feature off).
func TestVisionModelConfig_OmittedWhenEmpty(t *testing.T) {
	out, err := json.Marshal(AgentDefaults{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(out), "vision_model") {
		t.Fatalf("empty defaults should omit vision_model: %s", out)
	}
}

// A model_name change repoints both the vision model and its fallbacks.
func TestRenameModelReferences_Vision(t *testing.T) {
	c := &Config{}
	c.Agents.Defaults.VisionModel = "old"
	c.Agents.Defaults.VisionModelFallbacks = []string{"old", "keep"}

	c.RenameModelReferences("old", "new")

	if c.Agents.Defaults.VisionModel != "new" {
		t.Fatalf("VisionModel = %q, want new", c.Agents.Defaults.VisionModel)
	}
	if got := c.Agents.Defaults.VisionModelFallbacks; got[0] != "new" || got[1] != "keep" {
		t.Fatalf("VisionModelFallbacks = %v, want [new keep]", got)
	}
}
