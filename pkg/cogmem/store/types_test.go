// ClawEh - Cognitive Memory
// License: MIT

package store

import (
	"testing"
	"time"
)

// TestDomainLastActive covers the unit-tolerant accessor: unset, a legacy
// unix-seconds value, and a current unix-nanoseconds value.
func TestDomainLastActive(t *testing.T) {
	if _, ok := (Domain{LastActiveAt: 0}).LastActive(); ok {
		t.Error("unset last_active_at should report ok=false")
	}

	secs := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC).Unix()
	got, ok := (Domain{LastActiveAt: secs}).LastActive()
	if !ok || !got.Equal(time.Unix(secs, 0)) {
		t.Errorf("legacy seconds value: got %v ok=%v", got, ok)
	}

	nanos := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC).UnixNano()
	got, ok = (Domain{LastActiveAt: nanos}).LastActive()
	if !ok || !got.Equal(time.Unix(0, nanos)) {
		t.Errorf("nanoseconds value: got %v ok=%v", got, ok)
	}
}
