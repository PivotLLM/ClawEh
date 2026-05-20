package memory

import "github.com/PivotLLM/ClawEh/pkg/providers"

// StoredMessage is a providers.Message decorated with a monotonically increasing
// sequence number assigned at write time. Seq is storage-layer-only and is never
// part of providers.Message.
type StoredMessage struct {
	Seq int `json:"seq"`
	providers.Message
}
