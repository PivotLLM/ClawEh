// ClawEh
// License: MIT

package cronmsg

import (
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	const ts = "2026-05-31 09:40 MST"
	// Parse returns the substring after the FIRST newline following the prefix
	// line, so use a single '\n' here to make the expected payload exact.
	marked := "[3f9a1c0d] " + prefix + ts + ":\nNo changes."
	legacy := prefix + ts + ":\nNo changes."

	tests := []struct {
		name    string
		content string
		wantFP  string
		wantPay string
		wantOK  bool
	}{
		{"marked", marked, "3f9a1c0d", "No changes.", true},
		{"legacy unmarked", legacy, "", "No changes.", true},
		{"non-cron", "just a normal message", "", "", false},
		{"non-cron with brackets", "[hello] world", "", "", false},
		{"malformed bracket non-hex", "[zzzz] " + prefix + ts + ":\n\nx", "", "", false},
		{"bracket but no space", "[3f9a]" + prefix + ts + ":\n\nx", "", "", false},
		{"marker then non-cron", "[3f9a1c0d] not a cron message", "", "", false},
		{"cron prefix no newline", prefix + ts, "", "", true},
		{"two newlines keeps trailing", "[abc] " + prefix + ts + ":\n\nhi", "abc", "\nhi", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fp, pay, ok := Parse(tc.content)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if fp != tc.wantFP {
				t.Errorf("fingerprint = %q, want %q", fp, tc.wantFP)
			}
			if pay != tc.wantPay {
				t.Errorf("payload = %q, want %q", pay, tc.wantPay)
			}
		})
	}
}

func TestCollapseKey(t *testing.T) {
	const ts = "2026-05-31 09:40 MST"
	// marked: key is the fingerprint
	if k, ok := CollapseKey("[abc123] " + prefix + ts + ":\npayload"); !ok || k != "abc123" {
		t.Errorf("marked key = %q ok=%v, want abc123 true", k, ok)
	}
	// legacy: key is the payload
	if k, ok := CollapseKey(prefix + ts + ":\npayload"); !ok || k != "payload" {
		t.Errorf("legacy key = %q ok=%v, want payload true", k, ok)
	}
	// non-cron
	if _, ok := CollapseKey("nope"); ok {
		t.Error("non-cron content reported as cron")
	}
}

// TestBuildParseRoundTrip verifies that Build output parses back to the same
// fingerprint and payload, for both the marked and legacy forms.
func TestBuildParseRoundTrip(t *testing.T) {
	fireTime := time.Date(2026, 5, 31, 9, 40, 0, 0, time.UTC)
	const message = "self-check complete"

	t.Run("marked", func(t *testing.T) {
		const fp = "3f9a1c0d"
		out := Build(fp, fireTime, message)
		gotFP, gotPay, ok := Parse(out)
		if !ok {
			t.Fatalf("Parse(%q) ok=false, want true", out)
		}
		if gotFP != fp {
			t.Errorf("fingerprint = %q, want %q", gotFP, fp)
		}
		// Build joins the prefix line and the message with ":\n\n", so the
		// payload after the first newline retains the leading blank line.
		if want := "\n" + message; gotPay != want {
			t.Errorf("payload = %q, want %q", gotPay, want)
		}
		if key, _ := CollapseKey(out); key != fp {
			t.Errorf("CollapseKey = %q, want %q", key, fp)
		}
	})

	t.Run("legacy", func(t *testing.T) {
		out := Build("", fireTime, message)
		gotFP, gotPay, ok := Parse(out)
		if !ok {
			t.Fatalf("Parse(%q) ok=false, want true", out)
		}
		if gotFP != "" {
			t.Errorf("fingerprint = %q, want empty", gotFP)
		}
		if want := "\n" + message; gotPay != want {
			t.Errorf("payload = %q, want %q", gotPay, want)
		}
		// Legacy collapse key is the payload.
		if key, ok := CollapseKey(out); !ok || key != "\n"+message {
			t.Errorf("CollapseKey = %q ok=%v, want %q true", key, ok, "\n"+message)
		}
	})
}

// TestBuildTimestampFormat pins the exact timestamp layout in the produced
// wrapper, guarding against accidental format drift.
func TestBuildTimestampFormat(t *testing.T) {
	fireTime := time.Date(2026, 5, 31, 9, 40, 0, 0, time.UTC)
	out := Build("", fireTime, "x")
	want := prefix + "2026-05-31 09:40 UTC:\n\nx"
	if out != want {
		t.Errorf("Build = %q, want %q", out, want)
	}
}
