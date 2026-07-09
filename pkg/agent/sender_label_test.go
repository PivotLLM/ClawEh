package agent

import (
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/bus"
)

// prependSenderLabel attributes every message regardless of peer kind, so a
// direct chat under the default unified session scope stays attributable.
func TestPrependSenderLabel(t *testing.T) {
	tests := []struct {
		name   string
		sender bus.SenderInfo
		want   string
	}{
		{"display and username", bus.SenderInfo{DisplayName: "Alice", Username: "alice"}, "[From: Alice (@alice)]\nhi"},
		{"display only", bus.SenderInfo{DisplayName: "Bob"}, "[From: Bob]\nhi"},
		{"username only", bus.SenderInfo{Username: "carol"}, "[From: @carol]\nhi"},
		{"no identity", bus.SenderInfo{}, "hi"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := prependSenderLabel("hi", tt.sender); got != tt.want {
				t.Errorf("prependSenderLabel = %q, want %q", got, tt.want)
			}
		})
	}
}
