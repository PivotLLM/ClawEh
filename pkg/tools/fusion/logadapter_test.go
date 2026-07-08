// ClawEh
// License: MIT

package fusion

import (
	"reflect"
	"testing"
)

// fieldsToMap mirrors mlogger's alternating key/value convention, including the
// trailing-key "MISSING" rule.
func TestFieldsToMap(t *testing.T) {
	tests := []struct {
		name string
		args []any
		want map[string]any
	}{
		{"empty", nil, nil},
		{"pairs", []any{"a", 1, "b", "two"}, map[string]any{"a": 1, "b": "two"}},
		{"odd trailing key", []any{"a", 1, "orphan"}, map[string]any{"a": 1, "orphan": "MISSING"}},
		{"non-string key", []any{42, "v"}, map[string]any{"42": "v"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fieldsToMap(tt.args); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("fieldsToMap(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

// The embedded engine emits Fatal for ordinary request failures, so Fatal-level
// calls MUST log without terminating the ClawEh process. If any of these exited
// or panicked, the test binary would never reach the end of the function.
func TestFusionLogAdapter_FatalNeverExits(t *testing.T) {
	a := newFusionLogAdapter()
	a.Fatal("boom")
	a.Fatalf("boom %d", 1)
	a.FatalFields("k", "v")
	a.FatalExit()
	// Reaching here proves none of the above tore down the process.
}

// Every forwarded method must run without panicking (nil fields, odd args, etc.).
func TestFusionLogAdapter_AllLevelsSafe(t *testing.T) {
	a := newFusionLogAdapter()
	a.Debug("d")
	a.Info("i")
	a.Notice("n")
	a.Warning("w")
	a.Error("e")
	a.Debugf("%d", 1)
	a.Infof("%d", 1)
	a.Noticef("%d", 1)
	a.Warningf("%d", 1)
	a.Errorf("%d", 1)
	a.DebugFields("k", 1)
	a.InfoFields("k", 1)
	a.NoticeFields("k", 1)
	a.WarningFields("k", 1)
	a.ErrorFields("k")
	a.Close()
}
