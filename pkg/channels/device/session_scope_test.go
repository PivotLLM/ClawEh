package device

import (
	"context"
	"path/filepath"
	"testing"
)

// stubQuerier is an AgentQuerier that reports a fixed default agent and session
// mode; the read methods are unused by session-scope resolution.
type stubQuerier struct {
	defaultAgent string
	mode         string
}

func (q stubQuerier) Agents() ([]DeviceAgentInfo, string, string) {
	return nil, q.defaultAgent, "agent:" + q.defaultAgent + ":main"
}
func (q stubQuerier) DefaultAgentID() string                { return q.defaultAgent }
func (q stubQuerier) SessionMode() string                   { return q.mode }
func (q stubQuerier) History(string) []DeviceHistoryMessage { return nil }

func newScopeServer(t *testing.T, mode string) *Server {
	t.Helper()
	st, err := OpenStore(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &Server{store: st, querier: stubQuerier{defaultAgent: "amber", mode: mode}}
}

// Under unified, a node client (the R1 sends the bare "main" sentinel) joins the
// default agent's main conversation rather than a per-device one.
func TestSessionScopeKeyUnifiedNodeClient(t *testing.T) {
	s := newScopeServer(t, "unified")
	lc := &liveConn{deviceID: "dev1", sessionKey: "main"}

	if got := s.sessionScopeKey(lc); got != "agent:amber:main" {
		t.Fatalf("session key = %q, want agent:amber:main", got)
	}
}

// Two devices under unified share one conversation — same assistant, same
// history, same memory.
func TestSessionScopeKeyUnifiedDevicesShareSession(t *testing.T) {
	s := newScopeServer(t, "unified")
	a := s.sessionScopeKey(&liveConn{deviceID: "dev1", sessionKey: "main"})
	b := s.sessionScopeKey(&liveConn{deviceID: "dev2", sessionKey: "main"})

	if a != b {
		t.Fatalf("devices must share a session under unified: %q != %q", a, b)
	}
}

// An operator client still picks its agent, but its per-profile isolation is
// dropped under unified.
func TestSessionScopeKeyUnifiedOperatorClient(t *testing.T) {
	s := newScopeServer(t, "unified")
	lc := &liveConn{deviceID: "dev1", sessionKey: "agent:wendy:slack:work"}

	if got := s.sessionScopeKey(lc); got != "agent:wendy:main" {
		t.Fatalf("session key = %q, want agent:wendy:main", got)
	}
}

// A per-device agent assignment still selects the agent under unified — it picks
// WHICH assistant, not which conversation.
func TestSessionScopeKeyUnifiedHonorsDeviceAssignment(t *testing.T) {
	s := newScopeServer(t, "unified")
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

	if got := s.sessionScopeKey(lc); got != "agent:wendy:main" {
		t.Fatalf("session key = %q, want agent:wendy:main", got)
	}
}

// With an isolating mode the previous behavior stands: per-device for node
// clients, verbatim for operator clients.
func TestSessionScopeKeyIsolatingMode(t *testing.T) {
	s := newScopeServer(t, "per-user")

	node := s.sessionScopeKey(&liveConn{deviceID: "dev1", sessionKey: "main"})
	if node != "agent:amber:device:dev1" {
		t.Errorf("node key = %q, want agent:amber:device:dev1", node)
	}
	op := s.sessionScopeKey(&liveConn{deviceID: "dev1", sessionKey: "agent:wendy:slack:work"})
	if op != "agent:wendy:slack:work" {
		t.Errorf("operator key = %q, want it honored verbatim", op)
	}
}

// chat.history must resolve through the same rule as chat.send, so a client
// reads the transcript its turns are written to.
func TestHistoryKeyMatchesSendKey(t *testing.T) {
	s := newScopeServer(t, "unified")
	lc := &liveConn{deviceID: "dev1", sessionKey: "agent:wendy:slack:work"}

	send := s.sessionScopeKey(lc)
	// The operator client asks for its own key; resolution must land on the same
	// session the turn was written to.
	history := s.sessionScopeKeyFor(lc, "agent:wendy:slack:work")
	if send != history {
		t.Fatalf("history key %q != send key %q", history, send)
	}
}

// A server without a querier (no agent loop attached) must not panic and must
// fall back to the unified default.
func TestSessionScopeKeyWithoutQuerier(t *testing.T) {
	st, err := OpenStore(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer func() { _ = st.Close() }()
	s := &Server{store: st}

	if got := s.sessionScopeKey(&liveConn{deviceID: "dev1", sessionKey: "main"}); got != "agent:main:main" {
		t.Fatalf("session key = %q, want agent:main:main", got)
	}
}
