package device

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// stubQuerier is an AgentQuerier with the agents amber, wendy and bob and a
// fixed default agent; History is unused by session-scope resolution.
type stubQuerier struct {
	defaultAgent string
}

func (q stubQuerier) Agents() ([]DeviceAgentInfo, string, string) {
	agents := []DeviceAgentInfo{{ID: "amber"}, {ID: "wendy"}, {ID: "bob"}}
	return agents, q.defaultAgent, "agent:" + q.defaultAgent + ":main"
}
func (q stubQuerier) DefaultAgentID() string                { return q.defaultAgent }
func (q stubQuerier) History(string) []DeviceHistoryMessage { return nil }

func newScopeServer(t *testing.T) *Server {
	t.Helper()
	st, err := OpenStore(context.Background(), filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return &Server{store: st, querier: stubQuerier{defaultAgent: "amber"}}
}

// scopeKey resolves lc's declared session key and fails the test on a refusal.
func scopeKey(t *testing.T, s *Server, lc *liveConn) string {
	t.Helper()
	got, err := s.sessionScopeKeyFor(context.Background(), lc, lc.sessionKey)
	if err != nil {
		t.Fatalf("sessionScopeKeyFor(%q): %v", lc.sessionKey, err)
	}
	return got
}

// A node client (the R1 sends the bare "main" sentinel) joins the default
// agent's main conversation rather than a per-device one.
func TestSessionScopeKeyNodeClient(t *testing.T) {
	s := newScopeServer(t)
	lc := &liveConn{deviceID: "dev1", sessionKey: "main"}

	if got := scopeKey(t, s, lc); got != "agent:amber:main" {
		t.Fatalf("session key = %q, want agent:amber:main", got)
	}
}

// Two devices share one conversation — same assistant, same history, same
// memory.
func TestSessionScopeKeyDevicesShareSession(t *testing.T) {
	s := newScopeServer(t)
	a := scopeKey(t, s, &liveConn{deviceID: "dev1", sessionKey: "main"})
	b := scopeKey(t, s, &liveConn{deviceID: "dev2", sessionKey: "main"})

	if a != b {
		t.Fatalf("devices must share a session: %q != %q", a, b)
	}
}

// An operator client picks its agent; whatever else its key names, it joins
// that agent's main conversation.
func TestSessionScopeKeyOperatorClient(t *testing.T) {
	s := newScopeServer(t)
	lc := &liveConn{deviceID: "dev1", sessionKey: "agent:wendy:slack:work"}

	if got := scopeKey(t, s, lc); got != "agent:wendy:main" {
		t.Fatalf("session key = %q, want agent:wendy:main", got)
	}
}

// A per-device agent assignment selects the agent — it picks WHICH assistant,
// not which conversation.
func TestSessionScopeKeyHonorsDeviceAssignment(t *testing.T) {
	s := newScopeServer(t)
	ctx := context.Background()
	reqID, err := s.store.CreatePending(ctx, PendingPairing{
		DeviceID: "dev1", PublicKey: "pk1", DisplayName: "Rabbit R1", Role: "node",
	})
	if err != nil {
		t.Fatalf("CreatePending: %v", err)
	}
	if _, _, err := s.store.Approve(ctx, reqID, []string{"node"}, nil); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if err := s.store.SetDeviceAgent(ctx, "dev1", "wendy"); err != nil {
		t.Fatalf("SetDeviceAgent: %v", err)
	}
	lc := &liveConn{deviceID: "dev1", sessionKey: "main"}

	if got := scopeKey(t, s, lc); got != "agent:wendy:main" {
		t.Fatalf("session key = %q, want agent:wendy:main", got)
	}
}

// A key naming an agent that does not exist is refused before anything reads
// a session.
func TestSessionScopeKeyUnknownAgent(t *testing.T) {
	s := newScopeServer(t)
	if _, err := s.sessionScopeKeyFor(context.Background(), &liveConn{deviceID: "dev1"}, "agent:nobody:main"); !errors.Is(err, errUnknownAgent) {
		t.Errorf("err = %v, want errUnknownAgent", err)
	}
}

// Keys an older release gave a device (agent:<id>:device:<dev>) or another
// surface resolve to the agent's main conversation.
func TestSessionScopeKeyOtherKeysResolveToMain(t *testing.T) {
	s := newScopeServer(t)
	for _, key := range []string{"agent:wendy:device:dev1", "agent:wendy:service", "agent:wendy:telegram:direct:1", "agent:wendy:subagent:u1"} {
		if got := scopeKey(t, s, &liveConn{deviceID: "dev1", sessionKey: key}); got != "agent:wendy:main" {
			t.Errorf("%s: session key = %q, want agent:wendy:main", key, got)
		}
	}
}

// chat.history must resolve through the same rule as chat.send, so a client
// reads the transcript its turns are written to.
func TestHistoryKeyMatchesSendKey(t *testing.T) {
	s := newScopeServer(t)
	lc := &liveConn{deviceID: "dev1", sessionKey: "agent:wendy:slack:work"}

	send := scopeKey(t, s, lc)
	// The operator client asks for its own key; resolution must land on the same
	// session the turn was written to.
	history, err := s.sessionScopeKeyFor(context.Background(), lc, "agent:wendy:slack:work")
	if err != nil {
		t.Fatalf("sessionScopeKeyFor: %v", err)
	}
	if send != history {
		t.Fatalf("history key %q != send key %q", history, send)
	}
}

// A server without a querier (no agent loop attached) must not panic and must
// fall back to the default agent's main conversation.
func TestSessionScopeKeyWithoutQuerier(t *testing.T) {
	st, err := OpenStore(context.Background(), filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()
	s := &Server{store: st}

	if got := scopeKey(t, s, &liveConn{deviceID: "dev1", sessionKey: "main"}); got != "agent:main:main" {
		t.Fatalf("session key = %q, want agent:main:main", got)
	}
}
