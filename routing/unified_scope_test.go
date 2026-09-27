package routing

import (
	"errors"
	"testing"
)

// Unified is the default: an unset mode must never silently isolate anything.
func TestIsUnified(t *testing.T) {
	for _, mode := range []SessionScope{"", SessionScopeUnified} {
		if !IsUnified(mode) {
			t.Errorf("IsUnified(%q) = false, want true", mode)
		}
	}
	for _, mode := range []SessionScope{SessionScopePerUser, SessionScopePerPlatform, SessionScopePerAccount} {
		if IsUnified(mode) {
			t.Errorf("IsUnified(%q) = true, want false", mode)
		}
	}
}

func TestResolveServiceSessionKey(t *testing.T) {
	if got := ResolveServiceSessionKey(SessionScopeUnified, "alice"); got != "agent:alice:main" {
		t.Errorf("unified service key = %q, want agent:alice:main", got)
	}
	if got := ResolveServiceSessionKey("", "alice"); got != "agent:alice:main" {
		t.Errorf("default service key = %q, want agent:alice:main", got)
	}
	if got := ResolveServiceSessionKey(SessionScopePerUser, "alice"); got != "agent:alice:service" {
		t.Errorf("isolating service key = %q, want agent:alice:service", got)
	}
}

func TestAgentIDFromSessionKey(t *testing.T) {
	cases := map[string]string{
		"agent:alice:main":          "alice",
		"agent:bob:device:abc123":   "bob",
		"agent:carol:slack:profile": "carol",
		"main":                      "", // node-client sentinel
		"agent:main:device:x":       "", // the sentinel in agent position
		"":                          "",
		"not-a-key":                 "",
		"agent:":                    "",
	}
	for in, want := range cases {
		if got := AgentIDFromSessionKey(in); got != want {
			t.Errorf("AgentIDFromSessionKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// resolveDeviceKey calls ResolveDeviceSessionKey and fails the test on an error.
func resolveDeviceKey(t *testing.T, mode SessionScope, requested, fallback, deviceID string) string {
	t.Helper()
	got, err := ResolveDeviceSessionKey(mode, requested, fallback, deviceID)
	if err != nil {
		t.Fatalf("ResolveDeviceSessionKey(%q, %q): %v", mode, requested, err)
	}
	return got
}

// Under unified every device joins the selected agent's main conversation —
// whether the client picked an agent (operator) or not (node).
func TestResolveDeviceSessionKeyUnified(t *testing.T) {
	// Node client: sends the "main" sentinel, falls back to its assigned agent.
	if got := resolveDeviceKey(t, SessionScopeUnified, "main", "amber", "dev1"); got != "agent:amber:main" {
		t.Errorf("node key = %q, want agent:amber:main", got)
	}
	// Operator client: picks its own agent; the profile segment is dropped.
	if got := resolveDeviceKey(t, SessionScopeUnified, "agent:wendy:slack:work", "amber", "dev1"); got != "agent:wendy:main" {
		t.Errorf("operator key = %q, want agent:wendy:main", got)
	}
	// Two devices on the same agent share one conversation.
	a := resolveDeviceKey(t, SessionScopeUnified, "main", "amber", "dev1")
	b := resolveDeviceKey(t, SessionScopeUnified, "main", "amber", "dev2")
	if a != b {
		t.Errorf("devices must share a session under unified: %q != %q", a, b)
	}
	// The empty mode behaves as unified.
	if got := resolveDeviceKey(t, "", "main", "amber", "dev1"); got != "agent:amber:main" {
		t.Errorf("default-mode key = %q, want agent:amber:main", got)
	}
}

// Under an isolating mode each device gets its own session for the selected
// agent, and a key naming any other session is refused.
func TestResolveDeviceSessionKeyIsolating(t *testing.T) {
	if got := resolveDeviceKey(t, SessionScopePerUser, "main", "amber", "dev1"); got != "agent:amber:device:dev1" {
		t.Errorf("node key = %q, want agent:amber:device:dev1", got)
	}
	// The agent's main key selects the agent; the session is the device's own.
	if got := resolveDeviceKey(t, SessionScopePerUser, "agent:wendy:main", "amber", "dev1"); got != "agent:wendy:device:dev1" {
		t.Errorf("selector key = %q, want agent:wendy:device:dev1", got)
	}
	// The device's own key is accepted as is.
	if got := resolveDeviceKey(t, SessionScopePerPlatform, "agent:wendy:device:dev1", "amber", "dev1"); got != "agent:wendy:device:dev1" {
		t.Errorf("own key = %q, want agent:wendy:device:dev1", got)
	}
	a := resolveDeviceKey(t, SessionScopePerUser, "main", "amber", "dev1")
	b := resolveDeviceKey(t, SessionScopePerUser, "main", "amber", "dev2")
	if a == b {
		t.Errorf("devices must not share a session when isolating: both %q", a)
	}
	for _, key := range []string{
		"agent:bob:telegram:direct:123456", // someone else's chat
		"agent:wendy:device:dev2",          // another device's session
		"agent:wendy:slack:work",           // a profile key
	} {
		if got, err := ResolveDeviceSessionKey(SessionScopePerPlatform, key, "amber", "dev1"); !errors.Is(err, ErrDeviceSessionKeyNotAllowed) {
			t.Errorf("key %q: got (%q, %v), want ErrDeviceSessionKeyNotAllowed", key, got, err)
		}
	}
}
