// ClawEh
// License: MIT

package agents

import "testing"

func TestParseMediaArg(t *testing.T) {
	// Absent → nil, no error.
	if refs, errStr := parseMediaArg(nil); refs != nil || errStr != "" {
		t.Errorf("nil arg: got %v / %q", refs, errStr)
	}
	// Valid refs pass through.
	refs, errStr := parseMediaArg([]any{"media://aaa", "media://bbb"})
	if errStr != "" || len(refs) != 2 {
		t.Errorf("valid refs: got %v / %q", refs, errStr)
	}
	// Non-array is rejected.
	if _, errStr := parseMediaArg("media://aaa"); errStr == "" {
		t.Error("non-array should be rejected")
	}
	// Non-ref entry is rejected.
	if _, errStr := parseMediaArg([]any{"/tmp/x.png"}); errStr == "" {
		t.Error("non-media:// entry should be rejected")
	}
	// Non-string entry is rejected.
	if _, errStr := parseMediaArg([]any{42}); errStr == "" {
		t.Error("non-string entry should be rejected")
	}
}
