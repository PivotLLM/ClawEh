// ClawEh
// License: MIT

package agenttoken

import "testing"

func TestSubagentSentinel_FormatAndDetection(t *testing.T) {
	if len(SubagentSentinel) != TokenLen {
		t.Errorf("sentinel length = %d, want %d", len(SubagentSentinel), TokenLen)
	}
	if !IsSubagentSentinel(SubagentSentinel) {
		t.Error("IsSubagentSentinel(SubagentSentinel) = false, want true")
	}
	if IsSubagentSentinel("") || IsSubagentSentinel(Prefix+"deadbeef") {
		t.Error("IsSubagentSentinel matched a non-sentinel value")
	}
}
