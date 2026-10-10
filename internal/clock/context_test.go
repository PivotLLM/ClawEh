// ClawEh
// License: MIT

package clock

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestFakeContextDeadline(t *testing.T) {
	type key struct{}
	cases := []struct {
		name    string
		advance time.Duration
		parent  bool // cancel the parent
		cancel  bool // call the CancelFunc
		want    error
	}{
		{name: "before the deadline", advance: time.Second - time.Nanosecond},
		{name: "at the deadline", advance: time.Second, want: context.DeadlineExceeded},
		{name: "parent cancelled", parent: true, want: context.Canceled},
		{name: "cancelled", cancel: true, want: context.Canceled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := NewFake(epoch)
			parent, cancelParent := context.WithCancel(context.WithValue(context.Background(), key{}, "v"))
			defer cancelParent()
			ctx, cancel := f.WithTimeout(parent, time.Second)
			defer cancel()
			child, cancelChild := context.WithCancel(ctx)
			defer cancelChild()
			if d, ok := ctx.Deadline(); !ok || !d.Equal(epoch.Add(time.Second)) {
				t.Fatalf("deadline = %v, %v", d, ok)
			}
			if ctx.Value(key{}) != "v" {
				t.Fatal("value not inherited from the parent")
			}
			f.Advance(tc.advance)
			if tc.parent {
				cancelParent()
			}
			if tc.cancel {
				cancel()
			}
			if tc.want == nil {
				if err := ctx.Err(); err != nil {
					t.Fatalf("ended early: %v", err)
				}
				return
			}
			<-ctx.Done()
			if err := ctx.Err(); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			<-child.Done()
			if err := child.Err(); !errors.Is(err, tc.want) {
				t.Fatalf("child err = %v, want %v", err, tc.want)
			}
			if f.Waiters() != 0 {
				t.Fatalf("waiters = %d after the context ended", f.Waiters())
			}
		})
	}
}

func TestFakeContextEndedParentOrPastDeadline(t *testing.T) {
	f := NewFake(epoch)
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	ctx, stop := f.WithTimeout(ended, time.Hour)
	defer stop()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", ctx.Err())
	}
	past, stopPast := f.WithDeadline(context.Background(), epoch.Add(-time.Second))
	defer stopPast()
	<-past.Done()
	if !errors.Is(past.Err(), context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", past.Err())
	}
}
