// ClawEh
// License: MIT

package agent

import "testing"

func intPtr(v int) *int { return &v }

// TestResolveAgentIntOpt covers how per-agent retention/compress config knobs
// flow into llmcontext options: a per-agent pointer always wins (even 0, an
// explicit "disabled"); otherwise a non-zero default applies; otherwise the knob
// is unset and the llmcontext package default is used.
func TestResolveAgentIntOpt(t *testing.T) {
	tests := []struct {
		name     string
		agentPtr *int
		defaults int
		wantVal  int
		wantOK   bool
	}{
		{"agent override wins", intPtr(7), 365, 7, true},
		{"agent override of 0 (explicit disable) wins", intPtr(0), 365, 0, true},
		{"falls back to non-zero default", nil, 3650, 3650, true},
		{"unset: nil ptr + zero default -> use package default", nil, 0, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v, ok := resolveAgentIntOpt(tc.agentPtr, tc.defaults)
			if v != tc.wantVal || ok != tc.wantOK {
				t.Errorf("resolveAgentIntOpt(%v, %d) = (%d, %v), want (%d, %v)",
					tc.agentPtr, tc.defaults, v, ok, tc.wantVal, tc.wantOK)
			}
		})
	}
}
