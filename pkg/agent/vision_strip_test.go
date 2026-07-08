package agent

import (
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// visionStripLoop builds a minimal AgentLoop whose config knows a vision model,
// a Responses vision model, an explicitly-off model, and a model with no vision
// flag (which must be treated as off).
func visionStripLoop() *AgentLoop {
	return &AgentLoop{cfg: &config.Config{
		Models: []config.ModelConfig{
			{ModelName: "gpt5", Enabled: true, Vision: config.VisionUserMessage},
			{ModelName: "gpt5-resp", Enabled: true, Vision: config.VisionToolResponse},
			{ModelName: "deepseek", Enabled: true, Vision: config.VisionOff},
			{ModelName: "noflag", Enabled: true},
		},
	}}
}

func msgsWithImage() []providers.Message {
	return []providers.Message{
		{Role: "user", Content: "check tracking"},
		{Role: "tool", Content: "[Image: image/png]", Media: []string{"data:image/png;base64,AAA"}},
	}
}

// A vision-capable model must receive the image untouched.
func TestMessagesForModel_VisionModelKeepsImages(t *testing.T) {
	al := visionStripLoop()
	for _, model := range []string{"gpt5", "gpt5-resp"} {
		out := al.messagesForModel(msgsWithImage(), model)
		if len(out[1].Media) != 1 {
			t.Errorf("%s: expected image kept, got Media=%v", model, out[1].Media)
		}
	}
}

// A non-vision model (off, unset, or unknown) must not receive image parts —
// this is the fix for OpenRouter 404ing the whole request on a non-vision
// default like deepseek. The persisted history must be left intact.
func TestMessagesForModel_NonVisionStripsImagesWithoutMutating(t *testing.T) {
	al := visionStripLoop()
	for _, model := range []string{"deepseek", "noflag", "unknown-model"} {
		msgs := msgsWithImage()
		out := al.messagesForModel(msgs, model)

		if out[1].Media != nil {
			t.Errorf("%s: expected images stripped, got Media=%v", model, out[1].Media)
		}
		// Text (incl. the [Image: …] marker) is preserved; only bytes are dropped.
		if out[1].Content != "[Image: image/png]" {
			t.Errorf("%s: text should be preserved, got %q", model, out[1].Content)
		}
		// The caller's history must not be mutated: a later vision turn still sees it.
		if len(msgs[1].Media) != 1 {
			t.Errorf("%s: original history mutated, Media=%v", model, msgs[1].Media)
		}
	}
}

// When there are no images, the input slice is returned as-is (no allocation),
// even for a non-vision model.
func TestMessagesForModel_NoImagesReturnsSameSlice(t *testing.T) {
	al := visionStripLoop()
	msgs := []providers.Message{{Role: "user", Content: "no images here"}}
	out := al.messagesForModel(msgs, "deepseek")
	if &out[0] != &msgs[0] {
		t.Error("expected the same backing slice when there are no images to strip")
	}
}
