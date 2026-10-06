package bus

import "strconv"

// Peer identifies the routing peer for a message (direct, group, channel, etc.)
type Peer struct {
	Kind string `json:"kind"` // "direct" | "group" | "channel" | ""
	ID   string `json:"id"`
}

// SenderInfo provides structured sender identity information.
type SenderInfo struct {
	Platform    string `json:"platform,omitempty"`     // "telegram", "discord", "slack", ...
	PlatformID  string `json:"platform_id,omitempty"`  // raw platform ID, e.g. "123456"
	CanonicalID string `json:"canonical_id,omitempty"` // "platform:id" format
	Username    string `json:"username,omitempty"`     // username (e.g. @alice)
	DisplayName string `json:"display_name,omitempty"` // display name
}

type InboundMessage struct {
	Channel    string            `json:"channel"`
	SenderID   string            `json:"sender_id"`
	Sender     SenderInfo        `json:"sender"`
	ChatID     string            `json:"chat_id"`
	Content    string            `json:"content"`
	Media      []string          `json:"media,omitempty"`
	Peer       Peer              `json:"peer"`                  // routing peer
	MessageID  string            `json:"message_id,omitempty"`  // platform message ID
	MediaScope string            `json:"media_scope,omitempty"` // media lifecycle scope
	SessionKey string            `json:"session_key"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	IsRetry    bool              `json:"is_retry,omitempty"`
}

type OutboundMessage struct {
	Channel           string `json:"channel"`
	ChatID            string `json:"chat_id"`
	Content           string `json:"content"`
	ReplyToMessageID  string `json:"reply_to_message_id,omitempty"`
	OriginalMessageID string `json:"original_message_id,omitempty"` // inbound MessageID, for per-message reaction tracking
	// Outcome is set only on the final reply of a turn (one of the Outcome
	// constants); interim messages (notices, placeholders, narration, msg_send)
	// leave it empty. Channels that do not need it ignore it. A channel that
	// implements channels.MessageLengthProvider has a long reply split by the
	// manager, and then receives the Outcome on every chunk.
	Outcome string `json:"outcome,omitempty"`
}

// Outcomes of a turn, carried on its final reply (OutboundMessage.Outcome).
const (
	OutcomeOK        = "ok"        // the agent replied
	OutcomeError     = "error"     // the turn failed; Content is the rendered error
	OutcomeCancelled = "cancelled" // the turn was cancelled (/cancel) before it finished
	OutcomeEmpty     = "empty"     // the agent produced no reply
)

// Inbound metadata keys a sender may set on InboundMessage.Metadata.
const (
	// MetaReplyRequired ("1") makes the turn publish exactly one final reply,
	// with its Outcome, to the inbound Channel/ChatID: even when the reply is
	// empty, the turn failed or was cancelled, or msg_send already replied.
	// Nothing is sent when the turn is interrupted by shutdown.
	MetaReplyRequired = "reply_required"
	// MetaSpawnDepth (a non-negative integer) is the sub-agent depth the turn
	// runs at. It only ever raises the depth: a sender sets it to the
	// configured maximum to stop the receiving agent from spawning.
	MetaSpawnDepth = "spawn_depth"
)

// SetSpawnDepth records depth under MetaSpawnDepth in meta (allocated when
// nil) and returns it. A depth of zero or less adds nothing.
func SetSpawnDepth(meta map[string]string, depth int) map[string]string {
	if depth <= 0 {
		return meta
	}
	if meta == nil {
		meta = map[string]string{}
	}
	meta[MetaSpawnDepth] = strconv.Itoa(depth)
	return meta
}

// ReplyRequired reports whether the inbound message asks for exactly one final
// reply (MetaReplyRequired).
func (m InboundMessage) ReplyRequired() bool {
	return m.Metadata[MetaReplyRequired] == "1"
}

// MediaPart describes a single media attachment to send.
type MediaPart struct {
	Type        string `json:"type"`                   // "image" | "audio" | "video" | "file"
	Ref         string `json:"ref"`                    // media store ref, e.g. "media://abc123"
	Caption     string `json:"caption,omitempty"`      // optional caption text
	Filename    string `json:"filename,omitempty"`     // original filename hint
	ContentType string `json:"content_type,omitempty"` // MIME type hint
}

// OutboundMediaMessage carries media attachments from Agent to channels via the bus.
type OutboundMediaMessage struct {
	Channel string      `json:"channel"`
	ChatID  string      `json:"chat_id"`
	Parts   []MediaPart `json:"parts"`
}
