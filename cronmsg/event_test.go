// ClawEh
// License: MIT

package cronmsg

import (
	"strings"
	"testing"
	"time"
)

// A monitor event must not look like a cron fire: the noise dedup in storage
// and the run collapse in compaction key on Parse, and either would fold
// distinct events from one listener into one.
func TestBuildEvent_IsNotACronWrapper(t *testing.T) {
	at := time.Date(2026, 9, 13, 17, 30, 0, 0, time.UTC)
	got := BuildEvent(at, "A document event arrived.", "documents_event_wait", `{"event":{"id":"e1"}}`)
	for _, want := range []string{
		"continuous monitor at 2026-09-13 17:30 UTC:",
		"A document event arrived.",
		"documents_event_wait returned the following:\n{\"event\":{\"id\":\"e1\"}}",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "cron job that fired") {
		t.Fatalf("event envelope reads as a cron fire:\n%s", got)
	}
	if _, _, ok := Parse(got); ok {
		t.Fatal("Parse accepted a monitor event as a cron-wrapper message")
	}
	if _, ok := CollapseKey(got); ok {
		t.Fatal("CollapseKey produced a key for a monitor event; events must never collapse")
	}
}

func TestBuildEvent_NoticeHasNoResultSection(t *testing.T) {
	got := BuildEvent(time.Now(), "Listener has failed 5 times in a row.", "documents_event_wait", "")
	if strings.Contains(got, "returned the following") {
		t.Fatalf("notice carries a result section:\n%s", got)
	}
}
