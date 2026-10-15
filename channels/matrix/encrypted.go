package matrix

import (
	"context"
	"sort"
	"strconv"
	"sync"
	"time"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/PivotLLM/ClawEh/logger"
)

// roomNameTimeout bounds the one room-name lookup made when a room is first
// seen to be encrypted; the lookup runs on the sync goroutine.
const roomNameTimeout = 5 * time.Second

// encryptedRooms is the process-wide record of the Matrix rooms seen to be
// end-to-end encrypted, which ClawEh cannot read. It outlives the channel,
// which every config reload rebuilds, so each room is logged once per
// process and Check Up lists it until ClawEh restarts.
var encryptedRooms = encryptedRecord{names: map[id.RoomID]string{}}

// encryptedRecord maps each encrypted room to its name ("" when unknown).
// Safe for concurrent use.
type encryptedRecord struct {
	mu    sync.Mutex
	names map[id.RoomID]string
}

// claim records roomID and reports whether it was not recorded before.
func (r *encryptedRecord) claim(roomID id.RoomID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.names[roomID]; ok {
		return false
	}
	r.names[roomID] = ""
	return true
}

func (r *encryptedRecord) setName(roomID id.RoomID, name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.names[roomID] = name
}

// labels returns each room as roomLabel renders it, sorted.
func (r *encryptedRecord) labels() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.names))
	for roomID, name := range r.names {
		out = append(out, roomLabel(roomID, name))
	}
	sort.Strings(out)
	return out
}

// EncryptedRooms names the Matrix rooms seen since ClawEh started to be
// end-to-end encrypted, sorted: `"Name" (!room:server)`, or the room id when
// its name is unknown. Empty when there were none.
func EncryptedRooms() []string {
	return encryptedRooms.labels()
}

// roomLabel names a room for people: its quoted name and id, or the id alone.
func roomLabel(roomID id.RoomID, name string) string {
	if name == "" {
		return roomID.String()
	}
	return strconv.Quote(name) + " (" + roomID.String() + ")"
}

// handleEncryptedEvent notes the room of an encrypted message
// (m.room.encrypted) or of the state that turns encryption on
// (m.room.encryption). ClawEh cannot read such a room and never answers in it.
func (c *MatrixChannel) handleEncryptedEvent(ctx context.Context, evt *event.Event) {
	if evt == nil || evt.RoomID == "" {
		return
	}
	if !encryptedRooms.claim(evt.RoomID) {
		return
	}
	name := c.roomName(ctx, evt.RoomID)
	if name != "" {
		encryptedRooms.setName(evt.RoomID, name)
	}
	logger.WarnCF("matrix", "Matrix room "+roomLabel(evt.RoomID, name)+" is encrypted; ClawEh can't read its messages",
		map[string]any{"channel": c.Name(), "room_id": evt.RoomID.String()})
}

// lookupRoomName asks the homeserver for the room's name; "" when it has none
// or the lookup fails.
func (c *MatrixChannel) lookupRoomName(ctx context.Context, roomID id.RoomID) string {
	ctx, cancel := context.WithTimeout(ctx, roomNameTimeout)
	defer cancel()
	var content event.RoomNameEventContent
	if err := c.client.StateEvent(ctx, roomID, event.StateRoomName, "", &content); err != nil {
		logger.DebugCF("matrix", "Room name lookup failed", map[string]any{
			"room_id": roomID.String(),
			"error":   err.Error(),
		})
		return ""
	}
	return content.Name
}
