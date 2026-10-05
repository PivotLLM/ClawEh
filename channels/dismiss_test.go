// ClawEh
// License: MIT

package channels

import (
	"context"
	"testing"

	"github.com/PivotLLM/ClawEh/bus"
)

// deletingChannel can delete the placeholder it sent.
type deletingChannel struct {
	mockMessageEditor
	deleted []string
}

func (d *deletingChannel) DeleteMessage(_ context.Context, _ string, messageID string) error {
	d.deleted = append(d.deleted, messageID)
	return nil
}

// DismissInbound clears typing, the reaction and the placeholder for a
// message that gets no reply: deleting the placeholder where the channel can,
// editing it to a check mark otherwise. Nothing is sent.
func TestDismissInbound(t *testing.T) {
	noSend := func(context.Context, bus.OutboundMessage) error {
		t.Fatal("DismissInbound sent a message")
		return nil
	}
	for _, tc := range []struct {
		name   string
		delete bool
	}{{"deletes the placeholder", true}, {"edits it where it cannot delete", false}} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestManager()
			var edits []string
			editFn := func(_ context.Context, _, _, content string) error {
				edits = append(edits, content)
				return nil
			}
			dc := &deletingChannel{}
			dc.sendFn, dc.editFn = noSend, editFn
			if tc.delete {
				m.RegisterChannel("test", dc)
			} else {
				m.RegisterChannel("test", &dc.mockMessageEditor)
			}
			typing, undone := false, false
			m.RecordTypingStop("test", "123", func() { typing = true })
			m.RecordReactionUndo("test", "123", "m1", func() { undone = true })
			m.RecordPlaceholder("test", "123", "456")

			m.DismissInbound(context.Background(), "test", "123", "m1")
			if !typing || !undone {
				t.Errorf("typing stopped %v, reaction undone %v", typing, undone)
			}
			if tc.delete && (len(dc.deleted) != 1 || dc.deleted[0] != "456" || len(edits) != 0) {
				t.Errorf("deleted %v, edits %v", dc.deleted, edits)
			}
			if !tc.delete && (len(edits) != 1 || edits[0] != "✓") {
				t.Errorf("edits %v, want the placeholder edited to a check mark", edits)
			}
			if _, ok := m.placeholders.Load("test:123"); ok {
				t.Error("the placeholder is still recorded")
			}
		})
	}
}
