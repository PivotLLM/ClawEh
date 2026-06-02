// ClawEh
// License: MIT

package llmcontext

import (
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/memory"
	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// testCronPrefix mirrors the cron-wrapper header for building test fixtures.
// The shared marker-parsing implementation and its unit tests live in
// pkg/cronmsg; these tests exercise the collapse policies layered on top of it.
const testCronPrefix = "The following message is from a cron job that fired at "

// --- collapseRepetitiveRuns (cron-aware) tests ---

func storedMsg(seq int64, role, content string) memory.StoredMessage {
	return memory.NewStoredMessage(seq, providers.Message{Role: role, Content: content})
}

func cronFire(seq int64, fp, ts, payload string) memory.StoredMessage {
	content := testCronPrefix + ts + ":\n\n" + payload
	if fp != "" {
		content = "[" + fp + "] " + content
	}
	return storedMsg(seq, "user", content)
}

// TestCollapseRepetitiveRuns_CronNoOpCollapses verifies a run of N marked cron
// fires with identical short replies collapses to a single counted anchor that
// carries the seq of the first message.
func TestCollapseRepetitiveRuns_CronNoOpCollapses(t *testing.T) {
	stored := []memory.StoredMessage{
		cronFire(10, "3f9a1c0d", "09:00", "self-check"),
		storedMsg(11, "assistant", "No changes."),
		cronFire(12, "3f9a1c0d", "10:00", "self-check"),
		storedMsg(13, "assistant", "No changes."),
		cronFire(14, "3f9a1c0d", "11:00", "self-check"),
		storedMsg(15, "assistant", "No changes."),
		cronFire(16, "3f9a1c0d", "12:00", "self-check"),
		storedMsg(17, "assistant", " No changes. "), // trimmed-equal
	}
	got := collapseRepetitiveRuns(stored)
	if len(got) != 1 {
		t.Fatalf("expected 1 collapsed message, got %d: %+v", len(got), got)
	}
	if got[0].Seq != 10 {
		t.Errorf("anchor seq = %d, want 10 (first message of run)", got[0].Seq)
	}
	if got[0].Role != "user" {
		t.Errorf("anchor role = %q, want user", got[0].Role)
	}
	want := `[scheduled job 3f9a1c0d fired ×4 (#10-#17); routine, replies identical: "No changes."]`
	if got[0].Content != want {
		t.Errorf("anchor content = %q, want %q", got[0].Content, want)
	}
}

// TestCollapseRepetitiveRuns_LegacyNoFingerprint verifies legacy fires (no marker)
// collapse using the payload key and omit the id in the anchor.
func TestCollapseRepetitiveRuns_LegacyNoFingerprint(t *testing.T) {
	stored := []memory.StoredMessage{
		cronFire(1, "", "09:00", "self-check"),
		storedMsg(2, "assistant", "No changes."),
		cronFire(3, "", "10:00", "self-check"),
		storedMsg(4, "assistant", "No changes."),
		cronFire(5, "", "11:00", "self-check"),
		storedMsg(6, "assistant", "No changes."),
	}
	got := collapseRepetitiveRuns(stored)
	if len(got) != 1 {
		t.Fatalf("expected 1 collapsed message, got %d", len(got))
	}
	want := `[scheduled job fired ×3 (#1-#6); routine, replies identical: "No changes."]`
	if got[0].Content != want {
		t.Errorf("anchor content = %q, want %q", got[0].Content, want)
	}
	if strings.Contains(got[0].Content, "scheduled job  fired") {
		t.Error("legacy anchor should not contain an empty id slot")
	}
}

// TestCollapseRepetitiveRuns_DifferingReplyBreaksRun verifies a substantive
// differing reply ends the uniform prefix; that pair is preserved verbatim.
func TestCollapseRepetitiveRuns_DifferingReplyBreaksRun(t *testing.T) {
	stored := []memory.StoredMessage{
		cronFire(1, "aa11bb22", "09:00", "self-check"),
		storedMsg(2, "assistant", "No changes."),
		cronFire(3, "aa11bb22", "10:00", "self-check"),
		storedMsg(4, "assistant", "No changes."),
		cronFire(5, "aa11bb22", "11:00", "self-check"),
		storedMsg(6, "assistant", "No changes."),
		cronFire(7, "aa11bb22", "12:00", "self-check"),
		storedMsg(8, "assistant", "Disk is full! Took action."), // differs → breaks
	}
	got := collapseRepetitiveRuns(stored)
	// Expect: 1 anchor (3 uniform fires) + the differing pair preserved (2 msgs).
	if len(got) != 3 {
		t.Fatalf("expected 3 messages (anchor + differing pair), got %d: %+v", len(got), got)
	}
	want := `[scheduled job aa11bb22 fired ×3 (#1-#6); routine, replies identical: "No changes."]`
	if got[0].Content != want {
		t.Errorf("anchor content = %q, want %q", got[0].Content, want)
	}
	if got[1].Seq != 7 || got[1].Role != "user" {
		t.Errorf("preserved fire = seq %d role %q, want seq 7 user", got[1].Seq, got[1].Role)
	}
	if got[2].Content != "Disk is full! Took action." {
		t.Errorf("preserved reply = %q", got[2].Content)
	}
}

// TestCollapseRepetitiveRuns_SubThresholdLeftAlone verifies a run shorter than
// the threshold is not collapsed.
func TestCollapseRepetitiveRuns_SubThresholdLeftAlone(t *testing.T) {
	stored := []memory.StoredMessage{
		cronFire(1, "cc33dd44", "09:00", "self-check"),
		storedMsg(2, "assistant", "No changes."),
		cronFire(3, "cc33dd44", "10:00", "self-check"),
		storedMsg(4, "assistant", "No changes."),
	}
	got := collapseRepetitiveRuns(stored)
	if len(got) != 4 {
		t.Fatalf("sub-threshold run should be left alone, got %d messages", len(got))
	}
	for i := range got {
		if got[i].Seq != stored[i].Seq || got[i].Content != stored[i].Content {
			t.Errorf("message %d altered: %+v", i, got[i])
		}
	}
}

// TestCollapseRepetitiveRuns_LongReplyNotCollapsed verifies that substantive
// (long) replies are not treated as routine no-ops.
func TestCollapseRepetitiveRuns_LongReplyNotCollapsed(t *testing.T) {
	long := strings.Repeat("x", cronNoOpReplyMaxLen+1)
	stored := []memory.StoredMessage{
		cronFire(1, "ee55ff66", "09:00", "self-check"),
		storedMsg(2, "assistant", long),
		cronFire(3, "ee55ff66", "10:00", "self-check"),
		storedMsg(4, "assistant", long),
		cronFire(5, "ee55ff66", "11:00", "self-check"),
		storedMsg(6, "assistant", long),
	}
	got := collapseRepetitiveRuns(stored)
	if len(got) != 6 {
		t.Fatalf("long replies should not collapse, got %d messages", len(got))
	}
}

// TestCollapseRepetitiveRuns_NonCronByteIdentical verifies the original
// byte-identical collapse still works for non-cron same-role runs.
func TestCollapseRepetitiveRuns_NonCronByteIdentical(t *testing.T) {
	stored := []memory.StoredMessage{
		storedMsg(1, "user", "ping"),
		storedMsg(2, "user", "ping"),
		storedMsg(3, "user", "ping"),
	}
	got := collapseRepetitiveRuns(stored)
	if len(got) != 1 {
		t.Fatalf("expected 1 collapsed entry, got %d", len(got))
	}
	if !strings.HasPrefix(got[0].Content, "[REPEATED 3 TIMES") {
		t.Errorf("expected REPEATED annotation, got %q", got[0].Content)
	}
}

// TestCronRunAnchor_TruncatesReply verifies the short-reply hint is truncated.
func TestCronRunAnchor_TruncatesReply(t *testing.T) {
	reply := strings.Repeat("a", 100)
	out := cronRunAnchor("deadbeef", 5, 10, 19, reply)
	if !strings.Contains(out, "deadbeef fired ×5 (#10-#19)") {
		t.Errorf("anchor missing id/count/range: %q", out)
	}
	if !strings.Contains(out, "…") {
		t.Errorf("expected truncation ellipsis in %q", out)
	}
}

// assistantWithTools builds an assistant StoredMessage that issued a tool call,
// i.e. the LLM took a meaningful action in response to a fire.
func assistantWithTools(seq int64, content, toolName string) memory.StoredMessage {
	return memory.NewStoredMessage(seq, providers.Message{
		Role:      "assistant",
		Content:   content,
		ToolCalls: []providers.ToolCall{{Name: toolName}},
	})
}

// TestCollapseRepetitiveRuns_ToolCallReplyBreaksRun verifies that a fire whose
// reply issued a tool call is treated as an action — it ends the run and is
// preserved verbatim — EVEN WHEN its reply text matches the no-op text. This is
// the key guard: same prompt 30× where the LLM acts on some of them must not be
// silently collapsed.
func TestCollapseRepetitiveRuns_ToolCallReplyBreaksRun(t *testing.T) {
	stored := []memory.StoredMessage{
		cronFire(1, "ab12cd34", "09:00", "self-check"),
		storedMsg(2, "assistant", "No changes."),
		cronFire(3, "ab12cd34", "10:00", "self-check"),
		storedMsg(4, "assistant", "No changes."),
		cronFire(5, "ab12cd34", "11:00", "self-check"),
		storedMsg(6, "assistant", "No changes."),
		cronFire(7, "ab12cd34", "12:00", "self-check"),
		// Same reply text, but the LLM ACTED (tool call) — must not collapse.
		assistantWithTools(8, "No changes.", "restart_service"),
		storedMsg(9, "tool", "service restarted"),
		storedMsg(10, "assistant", "Restarted nginx."),
	}
	got := collapseRepetitiveRuns(stored)
	// Expect: anchor for the 3 no-op fires (#1-#6) + the acting fire's 4 messages
	// preserved verbatim (cron7, assistant+toolcall 8, tool 9, assistant 10).
	if len(got) != 5 {
		t.Fatalf("expected 5 messages (anchor + 4 preserved), got %d: %+v", len(got), got)
	}
	wantAnchor := `[scheduled job ab12cd34 fired ×3 (#1-#6); routine, replies identical: "No changes."]`
	if got[0].Content != wantAnchor {
		t.Errorf("anchor = %q, want %q", got[0].Content, wantAnchor)
	}
	if got[1].Seq != 7 || got[2].Seq != 8 || len(got[2].ToolCalls) != 1 {
		t.Errorf("acting fire/tool-call not preserved: %+v", got[1:])
	}
	if got[3].Seq != 9 || got[4].Seq != 10 {
		t.Errorf("tool exchange after the acting fire not preserved: %+v", got[3:])
	}
}

// TestCollapseRetainedCronRuns collapses cron no-op runs in the retained tail
// while preserving surrounding non-cron messages and a trailing unreplied cron
// fire, and does NOT apply the byte-identical non-cron collapse.
func TestCollapseRetainedCronRuns(t *testing.T) {
	stored := []memory.StoredMessage{
		storedMsg(1, "user", "real work"),
		storedMsg(2, "assistant", "done"),
		cronFire(3, "ab12cd34", "09:00", "self-check"),
		storedMsg(4, "assistant", "No changes."),
		cronFire(5, "ab12cd34", "10:00", "self-check"),
		storedMsg(6, "assistant", "No changes."),
		cronFire(7, "ab12cd34", "11:00", "self-check"),
		storedMsg(8, "assistant", "No changes."),
		cronFire(9, "ab12cd34", "12:00", "self-check"), // trailing, unreplied → preserved
	}
	got := collapseRetainedCronRuns(stored)
	// Expect: msg1, msg2, anchor(#3-#8), trailing cron fire #9 = 4 messages.
	if len(got) != 4 {
		t.Fatalf("expected 4 messages, got %d: %+v", len(got), got)
	}
	if got[0].Seq != 1 || got[1].Seq != 2 {
		t.Errorf("leading messages altered: %+v", got[:2])
	}
	wantAnchor := `[scheduled job ab12cd34 fired ×3 (#3-#8); routine, replies identical: "No changes."]`
	if got[2].Seq != 3 || got[2].Content != wantAnchor {
		t.Errorf("anchor = seq %d %q, want seq 3 %q", got[2].Seq, got[2].Content, wantAnchor)
	}
	if got[3].Seq != 9 || got[3].Role != "user" {
		t.Errorf("trailing unreplied cron fire not preserved: %+v", got[3])
	}
}

// TestCollapseRetainedCronRuns_NoByteIdenticalCollapse verifies the retained-tail
// collapse leaves non-cron repeated content alone (unlike the summarizer-input
// path, which also does byte-identical collapse).
func TestCollapseRetainedCronRuns_NoByteIdenticalCollapse(t *testing.T) {
	stored := []memory.StoredMessage{
		storedMsg(1, "user", "ping"),
		storedMsg(2, "user", "ping"),
		storedMsg(3, "user", "ping"),
	}
	got := collapseRetainedCronRuns(stored)
	if len(got) != 3 {
		t.Fatalf("non-cron repeats must be preserved in the retained tail, got %d", len(got))
	}
}
