// ClawEh
// License: MIT

package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"time"
)

// turnIDKey is the context key for the turn id; a private type so nothing
// outside this package can collide with it.
type turnIDKey struct{}

// newTurnID returns a short random id (8 hex characters) that names one turn in
// the logs and the audit log, so every line a turn produces can be pulled
// together. Falls back to a time-derived value if the random source fails,
// which it does not on any supported platform.
func newTurnID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano()&0xffffffff, 16)
	}
	return hex.EncodeToString(b[:])
}

// withTurnID returns ctx carrying id for turnIDFrom.
func withTurnID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, turnIDKey{}, id)
}

// turnIDFrom returns the turn id stamped on ctx, or "" outside a turn.
func turnIDFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if id, ok := ctx.Value(turnIDKey{}).(string); ok {
		return id
	}
	return ""
}

// turnFields adds the turn id from ctx to a log field map (when there is one)
// and returns the map, so a log call can wrap its fields in place.
func turnFields(ctx context.Context, fields map[string]any) map[string]any {
	if id := turnIDFrom(ctx); id != "" {
		fields["turn_id"] = id
	}
	return fields
}
