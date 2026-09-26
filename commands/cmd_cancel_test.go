package commands

import (
	"context"
	"testing"
)

func TestCancelCommand_Reply(t *testing.T) {
	for _, tc := range []struct {
		name    string
		running bool
		skipped int
		want    string
	}{
		{"running and queued", true, 2, "Cancelled the current request and 2 pending message(s)."},
		{"running only", true, 0, "Cancelled the current request."},
		{"queued only", false, 3, "Cancelled 3 pending message(s)."},
		{"nothing", false, 0, "No pending messages to cancel."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := &Runtime{CancelPending: func() (bool, int) { return tc.running, tc.skipped }}
			var got string
			err := cancelCommand().Handler(context.Background(), Request{Reply: func(s string) error { got = s; return nil }}, rt)
			if err != nil {
				t.Fatalf("handler: %v", err)
			}
			if got != tc.want {
				t.Fatalf("reply = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCancelCommand_Unavailable(t *testing.T) {
	var got string
	if err := cancelCommand().Handler(context.Background(), Request{Reply: func(s string) error { got = s; return nil }}, &Runtime{}); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if got != unavailableMsg {
		t.Fatalf("reply = %q, want %q", got, unavailableMsg)
	}
}
