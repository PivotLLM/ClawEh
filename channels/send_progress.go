// ClawEh
// License: MIT

package channels

import "context"

// SendProgress records how many parts of one outbound message a channel has
// delivered, for a channel whose Send splits a message into several API
// calls. The manager gives every message its own SendProgress on the context
// it passes to Send and keeps it across that message's retries, so a retry
// resumes after the parts already delivered instead of repeating them.
// Retries of one message are sequential, so it needs no lock.
type SendProgress struct {
	delivered int
}

type sendProgressKey struct{}

// WithSendProgress returns ctx carrying a new, empty SendProgress: one per
// message, kept across its retries.
func WithSendProgress(ctx context.Context) context.Context {
	return context.WithValue(ctx, sendProgressKey{}, &SendProgress{})
}

// SendProgressFrom returns the SendProgress on ctx, or nil when the send did
// not come through the manager (a nil SendProgress delivers nothing yet and
// records nothing).
func SendProgressFrom(ctx context.Context) *SendProgress {
	if p, ok := ctx.Value(sendProgressKey{}).(*SendProgress); ok {
		return p
	}
	return nil
}

// Delivered returns the number of parts delivered by earlier attempts.
func (p *SendProgress) Delivered() int {
	if p == nil {
		return 0
	}
	return p.delivered
}

// MarkDelivered records one more delivered part.
func (p *SendProgress) MarkDelivered() {
	if p != nil {
		p.delivered++
	}
}
