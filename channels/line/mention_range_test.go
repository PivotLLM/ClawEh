package line

import (
	"math"
	"testing"
)

// TestMentionRange_RejectsOutOfRangeAndOverflow pins the guard against
// webhook index/length values that used to reach the slice: an index+length
// that overflows to a negative end, a negative length that puts end before
// start, and ranges past the text.
func TestMentionRange_RejectsOutOfRangeAndOverflow(t *testing.T) {
	const n = 5 // "hello"
	for _, tc := range []struct {
		name          string
		index, length int
		wantOK        bool
	}{
		{"whole text", 0, 5, true},
		{"inner", 1, 3, true},
		{"tail", 4, 1, true},
		{"zero length", 1, 0, false},
		{"negative length", 3, -2, false},
		{"negative index", -1, 2, false},
		{"past end", 3, 3, false},
		{"index past end", 6, 1, false},
		{"overflow to negative end", math.MaxInt, 1, false},
		{"overflow via length", 1, math.MaxInt, false},
		{"min int length", 0, math.MinInt, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start, end, ok := mentionRange(lineMentionee{Index: tc.index, Length: tc.length}, n)
			if ok != tc.wantOK {
				t.Fatalf("mentionRange(%d,%d) ok = %v, want %v", tc.index, tc.length, ok, tc.wantOK)
			}
			if ok && (start != tc.index || end != tc.index+tc.length) {
				t.Fatalf("mentionRange(%d,%d) = [%d,%d)", tc.index, tc.length, start, end)
			}
		})
	}
}

// mentionMsg builds a lineMessage carrying one mentionee; the mention field is
// an anonymous struct, so it is constructed here once.
func mentionMsg(text string, m lineMentionee) lineMessage {
	msg := lineMessage{Text: text}
	msg.Mention = &struct {
		Mentionees []lineMentionee `json:"mentionees"`
	}{Mentionees: []lineMentionee{m}}
	return msg
}

// TestMentionHandling_SurvivesHostileIndices drives the two callers with the
// inputs that panicked before mentionRange existed.
func TestMentionHandling_SurvivesHostileIndices(t *testing.T) {
	ch := &LINEChannel{botUserID: "U-bot", botDisplayName: "Claw"}
	for _, m := range []lineMentionee{
		{Index: math.MaxInt, Length: 1, UserID: "U-bot"},
		{Index: 0, Length: -1, UserID: "U-bot"},
		{Index: 3, Length: -2, UserID: "U-bot"},
		{Index: 2, Length: math.MaxInt},
	} {
		msg := mentionMsg("hello", m)
		_ = ch.isBotMentioned(msg)
		if got := ch.stripBotMention(msg.Text, msg); got != "hello" {
			t.Fatalf("stripBotMention with mention %+v = %q, want text unchanged", m, got)
		}
	}

	// A well-formed mention still strips.
	msg := mentionMsg("@Claw hello", lineMentionee{Index: 0, Length: 5, UserID: "U-bot"})
	if !ch.isBotMentioned(msg) {
		t.Fatal("well-formed bot mention not detected")
	}
	if got := ch.stripBotMention(msg.Text, msg); got != "hello" {
		t.Fatalf("stripBotMention = %q, want %q", got, "hello")
	}
}
