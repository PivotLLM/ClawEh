package memory

import (
	"time"

	"github.com/PivotLLM/ClawEh/pkg/providers"
)

// StoredMessage is a providers.Message decorated with a monotonically increasing
// sequence number assigned at write time and the UTC timestamp of when it was stored.
// Seq and CreatedAt are storage-layer-only and are never part of providers.Message.
type StoredMessage struct {
	Seq       int       `json:"seq"`
	CreatedAt time.Time `json:"created_at"`
	providers.Message
}
