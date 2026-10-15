package matrix

import (
	"bytes"
	"context"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
)

// resetEncryptedRooms empties the process-wide record for one test.
func resetEncryptedRooms(t *testing.T) {
	t.Helper()
	reset := func() {
		encryptedRooms.mu.Lock()
		encryptedRooms.names = map[id.RoomID]string{}
		encryptedRooms.mu.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

// syncBuffer is a log sink safe for the sync goroutine and the test.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// encryptedWarnings counts the encrypted-room WARN lines naming room.
func (b *syncBuffer) encryptedWarnings(room string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for line := range strings.SplitSeq(b.buf.String(), "\n") {
		if strings.Contains(line, `"level":"warn"`) && strings.Contains(line, "is encrypted") && strings.Contains(line, room) {
			n++
		}
	}
	return n
}

func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	t.Cleanup(logger.RedirectForTest(buf))
	return buf
}

func TestHandleEncryptedEvent_WarnsOncePerRoom(t *testing.T) {
	resetEncryptedRooms(t)
	logs := captureLogs(t)

	c, err := NewMatrixChannel(config.MatrixConfig{
		Homeserver:  "http://127.0.0.1:1",
		UserID:      "@claw:example.org",
		AccessToken: "token",
	}, bus.NewMessageBus())
	if err != nil {
		t.Fatal(err)
	}
	names := map[id.RoomID]string{"!alice:example.org": "Alice's room"}
	c.roomName = func(_ context.Context, roomID id.RoomID) string { return names[roomID] }

	ctx := context.Background()
	c.handleEncryptedEvent(ctx, &event.Event{RoomID: "!alice:example.org", Type: event.EventEncrypted})
	c.handleEncryptedEvent(ctx, &event.Event{RoomID: "!alice:example.org", Type: event.EventEncrypted})
	c.handleEncryptedEvent(ctx, &event.Event{RoomID: "!bob:example.org", Type: event.StateEncryption})

	if n := logs.encryptedWarnings("!alice:example.org"); n != 1 {
		t.Errorf("warnings for Alice's room = %d, want 1", n)
	}
	if n := logs.encryptedWarnings("!bob:example.org"); n != 1 {
		t.Errorf("warnings for Bob's room = %d, want 1", n)
	}
	want := []string{"!bob:example.org", `"Alice's room" (!alice:example.org)`}
	if got := EncryptedRooms(); !slices.Equal(got, want) {
		t.Errorf("EncryptedRooms() = %q, want %q", got, want)
	}
}

// Through a real sync: the encryption state and encrypted messages of one
// room record it once, with its name; an unencrypted room is not recorded.
func TestSync_RecordsEncryptedRoomOnly(t *testing.T) {
	resetEncryptedRooms(t)
	logs := captureLogs(t)

	const firstSync = `{"next_batch":"s1","rooms":{"join":{
"!enc:example.org":{
 "state":{"events":[{"type":"m.room.encryption","state_key":"","sender":"@alice:example.org","event_id":"$1","origin_server_ts":1,"content":{"algorithm":"m.megolm.v1.aes-sha2"}}]},
 "timeline":{"events":[
  {"type":"m.room.encrypted","sender":"@alice:example.org","event_id":"$2","origin_server_ts":1,"content":{"algorithm":"m.megolm.v1.aes-sha2","ciphertext":"abc","sender_key":"k","session_id":"s","device_id":"D"}},
  {"type":"m.room.encrypted","sender":"@alice:example.org","event_id":"$3","origin_server_ts":1,"content":{"algorithm":"m.megolm.v1.aes-sha2","ciphertext":"def","sender_key":"k","session_id":"s","device_id":"D"}}]}},
"!plain:example.org":{
 "state":{"events":[{"type":"m.room.name","state_key":"","sender":"@bob:example.org","event_id":"$4","origin_server_ts":1,"content":{"name":"Bob's room"}}]},
 "timeline":{"events":[{"type":"m.room.message","sender":"@bob:example.org","event_id":"$5","origin_server_ts":1,"content":{"msgtype":"m.text","body":"hi"}}]}}
}}}`

	var syncs atomic.Int32
	startSyncAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/filter"):
			reply(t, w, `{"filter_id":"1"}`)
		case strings.HasSuffix(r.URL.Path, "/sync"):
			if syncs.Add(1) == 1 {
				reply(t, w, firstSync)
				return
			}
			time.Sleep(20 * time.Millisecond) // stand in for the long poll
			reply(t, w, `{"next_batch":"s2"}`)
		case strings.Contains(r.URL.Path, "/rooms/!enc:example.org/state/m.room.name"):
			reply(t, w, `{"name":"Alice's room"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			reply(t, w, `{"errcode":"M_NOT_FOUND","error":"not found"}`)
		}
	})

	// The first sync is fully processed once the second one is requested.
	waitFor(t, "second sync", 5*time.Second, func() bool { return syncs.Load() >= 2 })

	want := []string{`"Alice's room" (!enc:example.org)`}
	if got := EncryptedRooms(); !slices.Equal(got, want) {
		t.Fatalf("EncryptedRooms() = %q, want %q", got, want)
	}
	if n := logs.encryptedWarnings("!enc:example.org"); n != 1 {
		t.Errorf("warnings for the encrypted room = %d, want 1", n)
	}
	if n := logs.encryptedWarnings("!plain:example.org"); n != 0 {
		t.Errorf("warnings for the unencrypted room = %d, want 0", n)
	}
}
