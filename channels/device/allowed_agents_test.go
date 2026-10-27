package device

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/PivotLLM/ClawEh/gatewayproto"
)

// allowedServer is a test server whose querier knows alice, agent3 and bob
// (default alice) and which records the session key of every turn it hands
// to the agent layer.
type allowedServer struct {
	srv   *Server
	wsURL string
	mu    sync.Mutex
	keys  []string
}

func newAllowedServer(t *testing.T) *allowedServer {
	t.Helper()
	srv, _, wsURL := newTestServer(t, ServerOptions{ServerVersion: "test-1", AutoApprove: true})
	srv.SetQuerier(&recordingQuerier{})
	a := &allowedServer{srv: srv, wsURL: wsURL}
	srv.SetInbound(func(_, _, _, _, sessionKey string, _ []InboundAttachment) {
		a.mu.Lock()
		a.keys = append(a.keys, sessionKey)
		a.mu.Unlock()
	})
	return a
}

// turnKeys waits until n turns reached the agent layer and returns their keys.
func (a *allowedServer) turnKeys(t *testing.T, n int) []string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		a.mu.Lock()
		got := append([]string(nil), a.keys...)
		a.mu.Unlock()
		if len(got) >= n || time.Now().After(deadline) {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// connect pairs a new device, limits it to allowed (nil = no restriction)
// and returns its connection.
func (a *allowedServer) connect(t *testing.T, allowed []string) (*emulator, *websocket.Conn) {
	t.Helper()
	em := newEmulator(t)
	conn := mustOpen(t, em, a.wsURL, "")
	if allowed != nil {
		if err := a.srv.store.SetDeviceAllowedAgents(context.Background(), em.deviceID(), allowed); err != nil {
			t.Fatalf("SetDeviceAllowedAgents: %v", err)
		}
	}
	return em, conn
}

// agentsList sends agents.list and returns the listed ids, defaultId and mainKey.
func agentsList(t *testing.T, em *emulator, conn *websocket.Conn) (ids []string, defaultID, mainKey string) {
	t.Helper()
	em.writeReq(t, conn, "al", "agents.list", map[string]any{})
	r := readRes(t, conn, "al")
	if !r.OK {
		t.Fatalf("agents.list refused: %+v", r.Error)
	}
	var p struct {
		DefaultID string `json:"defaultId"`
		MainKey   string `json:"mainKey"`
		Agents    []struct {
			ID string `json:"id"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(r.Payload, &p); err != nil {
		t.Fatalf("decode agents.list: %v", err)
	}
	for _, ag := range p.Agents {
		ids = append(ids, ag.ID)
	}
	return ids, p.DefaultID, p.MainKey
}

// commandReply sends message as a chat.send and returns the text of the
// chat final event that answers it.
func commandReply(t *testing.T, em *emulator, conn *websocket.Conn, id, message string) string {
	t.Helper()
	em.writeReq(t, conn, id, "chat.send", map[string]any{"message": message, "idempotencyKey": id})
	for _, f := range readFramesUntilFinal(t, conn) {
		if f.Type != gatewayproto.FrameEvent || f.Event != "chat" {
			continue
		}
		var ce struct {
			State   string `json:"state"`
			Message struct {
				Text string `json:"text"`
			} `json:"message"`
		}
		if err := json.Unmarshal(f.Payload, &ce); err != nil {
			t.Fatalf("decode chat event: %v", err)
		}
		if ce.State == "final" {
			return ce.Message.Text
		}
	}
	return ""
}

// A device limited to bob sees only bob, reaches bob with an unqualified
// key, and is refused alice by session key and by /agent.
func TestAllowedAgents_LimitedDevice(t *testing.T) {
	a := newAllowedServer(t)
	em, conn := a.connect(t, []string{"Bob"})

	ids, defaultID, mainKey := agentsList(t, em, conn)
	if !slices.Equal(ids, []string{"bob"}) || defaultID != "bob" || mainKey != "agent:bob:main" {
		t.Fatalf("agents.list = %v default %q mainKey %q, want [bob] bob agent:bob:main", ids, defaultID, mainKey)
	}

	for _, method := range []string{"chat.send", "chat.history"} {
		em.writeReq(t, conn, method, method, map[string]any{"sessionKey": "agent:alice:main", "message": "hi", "idempotencyKey": "r0"})
		r := readRes(t, conn, method)
		if r.OK || r.Error == nil || r.Error.Code != gatewayproto.CodeInvalidRequest || r.Error.Message != "agent not allowed for this device" {
			t.Fatalf("%s for alice: ok=%v err=%+v, want INVALID_REQUEST agent not allowed for this device", method, r.OK, r.Error)
		}
	}

	em.writeReq(t, conn, "s1", "chat.send", map[string]any{"sessionKey": "main", "message": "hi", "idempotencyKey": "r1"})
	if r := readRes(t, conn, "s1"); !r.OK {
		t.Fatalf("chat.send main refused: %+v", r.Error)
	}
	if keys := a.turnKeys(t, 1); !slices.Equal(keys, []string{"agent:bob:main"}) {
		t.Fatalf("turn keys = %v, want [agent:bob:main]", keys)
	}

	// A hidden agent is answered like one that does not exist.
	hidden := commandReply(t, em, conn, "c1", "/agent alice")
	unknown := commandReply(t, em, conn, "c1b", "/agent nobody")
	if !strings.HasPrefix(hidden, "No assistant matches \"alice\".") || strings.Contains(hidden, "alice (") ||
		strings.Replace(hidden, "alice", "nobody", 1) != unknown {
		t.Fatalf("/agent alice reply = %q, /agent nobody reply = %q, want the same no-match reply", hidden, unknown)
	}
	if dev, _, err := a.srv.store.GetPaired(context.Background(), em.deviceID()); err != nil || dev.AgentID != "" {
		t.Fatalf("after refused switch GetPaired = %+v err=%v, want no assignment", dev, err)
	}
	list := commandReply(t, em, conn, "c2", "/agent")
	if !strings.Contains(list, "bob (current)") || strings.Contains(list, "alice") || strings.Contains(list, "agent3") {
		t.Fatalf("/agent list = %q, want only bob (current)", list)
	}
}

// A device with no restriction behaves as before: every agent, the gateway
// default, and /agent may switch to any agent.
func TestAllowedAgents_UnrestrictedDevice(t *testing.T) {
	a := newAllowedServer(t)
	em, conn := a.connect(t, nil)

	ids, defaultID, mainKey := agentsList(t, em, conn)
	if !slices.Equal(ids, []string{"alice", "agent3", "bob"}) || defaultID != "alice" || mainKey != "agent:alice:main" {
		t.Fatalf("agents.list = %v default %q mainKey %q, want all agents, alice", ids, defaultID, mainKey)
	}
	em.writeReq(t, conn, "s1", "chat.send", map[string]any{"sessionKey": "agent:bob:main", "message": "hi", "idempotencyKey": "r1"})
	if r := readRes(t, conn, "s1"); !r.OK {
		t.Fatalf("chat.send bob refused: %+v", r.Error)
	}
	if got := commandReply(t, em, conn, "c1", "/agent alice"); !strings.HasPrefix(got, "Switched to alice") {
		t.Fatalf("/agent alice reply = %q, want a switch", got)
	}
}

// pairScopeDevice pairs dev1 in s's store with the given assignment and
// allowed set.
func pairScopeDevice(t *testing.T, s *Server, assigned string, allowed []string) {
	t.Helper()
	ctx := context.Background()
	pairStoreDevice(t, s.store, "dev1")
	if err := s.store.SetDeviceAgent(ctx, "dev1", assigned); err != nil {
		t.Fatalf("SetDeviceAgent: %v", err)
	}
	if err := s.store.SetDeviceAllowedAgents(ctx, "dev1", allowed); err != nil {
		t.Fatalf("SetDeviceAllowedAgents: %v", err)
	}
}

// An unqualified turn reaches the assignment when allowed, else the gateway
// default when allowed, else the first allowed agent that runs, else the
// gateway default.
func TestDeviceAgentFor_ResolutionOrder(t *testing.T) {
	tests := []struct {
		name     string
		assigned string
		allowed  []string
		want     string // "" = refused with errNoAllowedAgent
	}{
		{"no restriction, assignment", "bob", nil, "agent:bob:main"},
		{"assignment allowed", "bob", []string{"bob", "alice"}, "agent:bob:main"},
		{"assignment not allowed, default allowed", "bob", []string{"third", "alice"}, "agent:alice:main"},
		{"default not allowed", "", []string{"third", "bob"}, "agent:third:main"},
		{"first allowed agent not running", "", []string{"gone", "bob"}, "agent:bob:main"},
		{"assignment not allowed, default not allowed", "bob", []string{"third"}, "agent:third:main"},
		{"no allowed agent runs", "", []string{"gone"}, ""},
		{"only allowed agent is a human agent", "", []string{"person"}, ""},
		{"assignment allowed but none other runs", "third", []string{"third", "gone"}, "agent:third:main"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newScopeServer(t)
			pairScopeDevice(t, s, tc.assigned, tc.allowed)
			got, err := s.sessionScopeKeyFor(context.Background(), &liveConn{deviceID: "dev1"}, "main")
			if tc.want == "" {
				if !errors.Is(err, errNoAllowedAgent) || refusalCode(err) != gatewayproto.CodeUnavailable {
					t.Fatalf("session key = %q err = %v, want errNoAllowedAgent", got, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("session key = %q err = %v, want %q", got, err, tc.want)
			}
		})
	}
}

// A key naming an existing agent outside the allowed set is refused with
// errAgentNotAllowed; an unknown agent is still errUnknownAgent.
func TestSessionScopeKey_AgentNotAllowed(t *testing.T) {
	s := newScopeServer(t)
	pairScopeDevice(t, s, "", []string{"bob"})
	ctx := context.Background()
	lc := &liveConn{deviceID: "dev1"}

	if _, err := s.sessionScopeKeyFor(ctx, lc, "agent:alice:main"); !errors.Is(err, errAgentNotAllowed) {
		t.Fatalf("alice: err = %v, want errAgentNotAllowed", err)
	}
	if _, err := s.sessionScopeKeyFor(ctx, lc, "agent:nobody:main"); !errors.Is(err, errUnknownAgent) {
		t.Fatalf("nobody: err = %v, want errUnknownAgent", err)
	}
	if got, err := s.sessionScopeKeyFor(ctx, lc, "agent:BOB:main"); err != nil || got != "agent:bob:main" {
		t.Fatalf("BOB: %q err=%v, want agent:bob:main", got, err)
	}
}

// When the device record cannot be read, every resolution fails closed with
// errDeviceRecordUnavailable (sent as UNAVAILABLE), whether or not the key
// names an agent.
func TestSessionScopeKey_RecordUnavailableFailsClosed(t *testing.T) {
	s := newScopeServer(t)
	pairScopeDevice(t, s, "", []string{"bob"})
	ctx := context.Background()
	if _, err := s.store.db.ExecContext(ctx, `DROP TABLE paired_devices`); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	lc := &liveConn{deviceID: "dev1"}

	for _, key := range []string{"main", "agent:alice:main", "agent:bob:main"} {
		if got, err := s.sessionScopeKeyFor(ctx, lc, key); !errors.Is(err, errDeviceRecordUnavailable) {
			t.Fatalf("%s: got %q err = %v, want errDeviceRecordUnavailable", key, got, err)
		}
	}
	if code := refusalCode(errDeviceRecordUnavailable); code != gatewayproto.CodeUnavailable {
		t.Fatalf("refusal code = %s, want %s", code, gatewayproto.CodeUnavailable)
	}
	if code := refusalCode(errAgentNotAllowed); code != gatewayproto.CodeInvalidRequest {
		t.Fatalf("not-allowed refusal code = %s, want %s", code, gatewayproto.CodeInvalidRequest)
	}
}

// agents.list's defaultId (and mainKey) always names one of the listed agents,
// or is empty when the agent an unqualified turn reaches is not listed.
func TestAllowedAgents_ListDefaultIsListed(t *testing.T) {
	tests := []struct {
		name        string
		assigned    string
		allowed     []string
		wantIDs     []string
		wantDefault string
	}{
		{"default allowed", "", []string{"bob", "alice"}, []string{"alice", "bob"}, "alice"},
		{"assignment allowed", "agent3", []string{"bob", "agent3"}, []string{"agent3", "bob"}, "agent3"},
		{"default not allowed", "", []string{"bob"}, []string{"bob"}, "bob"},
		{"assignment not allowed", "alice", []string{"bob"}, []string{"bob"}, "bob"},
		{"no allowed agent runs", "", []string{"gone"}, nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := newAllowedServer(t)
			em, conn := a.connect(t, tc.allowed)
			if tc.assigned != "" {
				if err := a.srv.store.SetDeviceAgent(context.Background(), em.deviceID(), tc.assigned); err != nil {
					t.Fatal(err)
				}
			}
			ids, defaultID, mainKey := agentsList(t, em, conn)
			if !slices.Equal(ids, tc.wantIDs) || defaultID != tc.wantDefault {
				t.Fatalf("agents.list = %v default %q, want %v default %q", ids, defaultID, tc.wantIDs, tc.wantDefault)
			}
			if defaultID == "" {
				if mainKey != "" {
					t.Fatalf("mainKey = %q with no defaultId, want empty", mainKey)
				}
				return
			}
			if !slices.Contains(ids, defaultID) || mainKey != "agent:"+defaultID+":main" {
				t.Fatalf("defaultId %q mainKey %q not among listed %v", defaultID, mainKey, ids)
			}
		})
	}
}

// A device none of whose allowed agents runs is refused an unqualified turn
// with UNAVAILABLE, and /agent says no assistant is available.
func TestAllowedAgents_NoAllowedAgentRuns(t *testing.T) {
	a := newAllowedServer(t)
	em, conn := a.connect(t, []string{"gone"})

	em.writeReq(t, conn, "s1", "chat.send", map[string]any{"sessionKey": "main", "message": "hi", "idempotencyKey": "r1"})
	r := readRes(t, conn, "s1")
	if r.OK || r.Error == nil || r.Error.Code != gatewayproto.CodeUnavailable || r.Error.Message != "no allowed assistant is available on this device" {
		t.Fatalf("chat.send: ok=%v err=%+v, want UNAVAILABLE no allowed assistant is available on this device", r.OK, r.Error)
	}
	if keys := a.turnKeys(t, 0); len(keys) != 0 {
		t.Fatalf("turn keys = %v, want none", keys)
	}
	if got := commandReply(t, em, conn, "c1", "/agent"); got != "No assistants are available on this device." {
		t.Fatalf("/agent reply = %q", got)
	}
}

// A stored session key naming an agent the device may not reach does not
// block /agent, which still lists the allowed agents.
func TestAllowedAgents_CommandsIgnoreStaleKey(t *testing.T) {
	a := newAllowedServer(t)
	em, conn := a.connect(t, []string{"bob"})

	em.writeReq(t, conn, "n1", "node.event", map[string]any{"event": "chat.subscribe", "payload": map[string]any{"sessionKey": "agent:alice:main"}})
	if r := readRes(t, conn, "n1"); !r.OK {
		t.Fatalf("chat.subscribe refused: %+v", r.Error)
	}
	if list := commandReply(t, em, conn, "c1", "/agent"); !strings.Contains(list, "bob (current)") || strings.Contains(list, "alice") {
		t.Fatalf("/agent list = %q, want only bob (current)", list)
	}
	if help := commandReply(t, em, conn, "c2", "/help"); !strings.HasPrefix(help, "Commands:") {
		t.Fatalf("/help reply = %q", help)
	}
}

// "/agent default" names the agent the device then reaches: the gateway
// default when allowed, else the allowed agent that replaces it.
func TestAllowedAgents_ResetNamesResolvedAgent(t *testing.T) {
	tests := []struct {
		name    string
		allowed []string
		want    string
	}{
		{"unrestricted", nil, "Switched to alice. New messages will go to this assistant."},
		{"default not allowed", []string{"bob"}, "Switched to bob. New messages will go to this assistant."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := newAllowedServer(t)
			em, conn := a.connect(t, tc.allowed)
			if err := a.srv.store.SetDeviceAgent(context.Background(), em.deviceID(), "bob"); err != nil {
				t.Fatal(err)
			}
			if got := commandReply(t, em, conn, "c1", "/agent default"); got != tc.want {
				t.Fatalf("/agent default reply = %q, want %q", got, tc.want)
			}
		})
	}
}
