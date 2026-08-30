package gatewayproto

import (
	"encoding/json"
	"fmt"
)

// Frame type discriminator values (the "type" field).
const (
	FrameReq   = "req"
	FrameRes   = "res"
	FrameEvent = "event"
)

// RequestFrame is a client->server request. Params stays raw for per-method decode.
type RequestFrame struct {
	Type   string          `json:"type"`
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// ResponseFrame is the server->client reply to a request, correlated by ID.
// Success carries Payload; failure carries Error with OK=false.
type ResponseFrame struct {
	Type    string      `json:"type"`
	ID      string      `json:"id"`
	OK      bool        `json:"ok"`
	Payload any         `json:"payload,omitempty"`
	Error   *ErrorShape `json:"error,omitempty"`
}

// EventFrame is an unsolicited server->client event. Seq is per-connection
// monotonic on broadcast frames; StateVersion accompanies presence/health.
type EventFrame struct {
	Type         string        `json:"type"`
	Event        string        `json:"event"`
	Payload      any           `json:"payload,omitempty"`
	Seq          *uint64       `json:"seq,omitempty"`
	StateVersion *StateVersion `json:"stateVersion,omitempty"`
}

// StateVersion holds monotonic freshness counters for snapshot subtrees.
type StateVersion struct {
	Presence uint64 `json:"presence"`
	Health   uint64 `json:"health"`
}

// FrameKind peeks at the "type" discriminator of a raw text frame.
func FrameKind(raw []byte) (string, error) {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return "", fmt.Errorf("gatewayproto: parse frame type: %w", err)
	}
	if head.Type == "" {
		return "", fmt.Errorf("gatewayproto: frame missing type")
	}
	return head.Type, nil
}

// NewOKResponse builds a successful response frame for a request id.
func NewOKResponse(id string, payload any) ResponseFrame {
	return ResponseFrame{Type: FrameRes, ID: id, OK: true, Payload: payload}
}

// NewErrorResponse builds a failed response frame for a request id.
func NewErrorResponse(id string, err *ErrorShape) ResponseFrame {
	return ResponseFrame{Type: FrameRes, ID: id, OK: false, Error: err}
}

// NewEvent builds an event frame. Pass seq=nil for targeted (non-broadcast) events.
func NewEvent(event string, payload any, seq *uint64) EventFrame {
	return EventFrame{Type: FrameEvent, Event: event, Payload: payload, Seq: seq}
}
