// ClawEh
// License: MIT

package msgtoken

import (
	"os"
	"testing"
	"time"
)

func TestNamedStore_CreateListValidateDelete_RoundTrip(t *testing.T) {
	path := NamedTokenPath(t.TempDir())
	s, err := NewNamedStore(path)
	if err != nil {
		t.Fatalf("NewNamedStore: %v", err)
	}

	tok, err := s.Create("amber", "gps")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if tok.ID == "" || tok.Token == "" || tok.Name != "gps" || tok.CreatedAtMS == 0 {
		t.Fatalf("Create returned incomplete token: %+v", tok)
	}

	list := s.List("amber")
	if len(list) != 1 || list[0].ID != tok.ID {
		t.Fatalf("List = %+v, want the one created token", list)
	}

	// The token validates back to its agent.
	if agentID, ok := s.Validate(tok.Token); !ok || agentID != "amber" {
		t.Fatalf("Validate(created) = (%q,%v), want (amber,true)", agentID, ok)
	}

	// Unknown token does not validate.
	if agentID, ok := s.Validate("nope"); ok || agentID != "" {
		t.Fatalf("Validate(unknown) = (%q,%v), want (\"\",false)", agentID, ok)
	}

	// Delete removes it; a second delete is a no-op.
	if !s.Delete("amber", tok.ID) {
		t.Fatal("Delete(existing) = false, want true")
	}
	if s.Delete("amber", tok.ID) {
		t.Fatal("Delete(already gone) = true, want false")
	}
	if _, ok := s.Validate(tok.Token); ok {
		t.Fatal("Validate after delete still ok")
	}

	// State file is 0600.
	if fi, err := os.Stat(path); err == nil {
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Errorf("state file perm = %o, want 600", perm)
		}
	} else {
		t.Fatalf("stat state file: %v", err)
	}
}

func TestNamedStore_Persist(t *testing.T) {
	path := NamedTokenPath(t.TempDir())
	s1, err := NewNamedStore(path)
	if err != nil {
		t.Fatalf("NewNamedStore: %v", err)
	}
	created, err := s1.Create("dawn", "alarm")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// A fresh store loaded from the same path sees the persisted token.
	s2, err := NewNamedStore(path)
	if err != nil {
		t.Fatalf("NewNamedStore(reload): %v", err)
	}
	if agentID, ok := s2.Validate(created.Token); !ok || agentID != "dawn" {
		t.Fatalf("reloaded Validate = (%q,%v), want (dawn,true)", agentID, ok)
	}
}

func TestNamedStore_MultiplePerAgent(t *testing.T) {
	s, err := NewNamedStore(NamedTokenPath(t.TempDir()))
	if err != nil {
		t.Fatalf("NewNamedStore: %v", err)
	}
	a, _ := s.Create("amber", "one")
	b, _ := s.Create("amber", "two")

	if len(s.List("amber")) != 2 {
		t.Fatalf("List(amber) len = %d, want 2", len(s.List("amber")))
	}
	// Both validate to the same agent.
	if id, ok := s.Validate(a.Token); !ok || id != "amber" {
		t.Fatalf("Validate(a) = (%q,%v)", id, ok)
	}
	if id, ok := s.Validate(b.Token); !ok || id != "amber" {
		t.Fatalf("Validate(b) = (%q,%v)", id, ok)
	}

	// Deleting one leaves the other valid.
	if !s.Delete("amber", a.ID) {
		t.Fatal("Delete(a) = false")
	}
	if _, ok := s.Validate(a.Token); ok {
		t.Fatal("deleted token a still validates")
	}
	if id, ok := s.Validate(b.Token); !ok || id != "amber" {
		t.Fatalf("token b should still validate after deleting a: (%q,%v)", id, ok)
	}
	if len(s.List("amber")) != 1 {
		t.Fatalf("List(amber) after delete = %d, want 1", len(s.List("amber")))
	}
}

// clockedStore returns an in-memory store whose clock is driven by *now, so
// tests can advance time deterministically.
func clockedStore(t *testing.T, now *time.Time) *NamedStore {
	t.Helper()
	s, err := NewNamedStore("")
	if err != nil {
		t.Fatalf("NewNamedStore: %v", err)
	}
	s.now = func() time.Time { return *now }
	return s
}

func TestNamedStore_EffectiveDefaults(t *testing.T) {
	// 0 → defaults; explicit values pass through.
	zero := NamedToken{}
	if zero.EffectiveRatePerMin() != DefaultRatePerMin || zero.EffectiveBlockMinutes() != DefaultBlockMinutes {
		t.Fatalf("zero token = (%d,%d), want defaults (%d,%d)",
			zero.EffectiveRatePerMin(), zero.EffectiveBlockMinutes(), DefaultRatePerMin, DefaultBlockMinutes)
	}
	set := NamedToken{RatePerMin: 5, BlockMinutes: 2}
	if set.EffectiveRatePerMin() != 5 || set.EffectiveBlockMinutes() != 2 {
		t.Fatalf("set token = (%d,%d), want (5,2)", set.EffectiveRatePerMin(), set.EffectiveBlockMinutes())
	}
}

func TestNamedStore_Allow_TripsAndBlocks(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := clockedStore(t, &now)
	tok, _ := s.Create("amber", "gps")
	if !s.Update("amber", tok.ID, 3, 15) {
		t.Fatal("Update returned false")
	}

	// First 3 requests within the window are allowed.
	for i := 0; i < 3; i++ {
		if allowed, _ := s.Allow("amber", tok.ID); !allowed {
			t.Fatalf("request %d unexpectedly blocked", i+1)
		}
	}
	// The 4th trips the limit → blocked, retryAfter ≈ 15m.
	allowed, retry := s.Allow("amber", tok.ID)
	if allowed {
		t.Fatal("4th request should be blocked")
	}
	if retry < 14*time.Minute || retry > 15*time.Minute {
		t.Fatalf("retryAfter = %v, want ~15m", retry)
	}

	// Still blocked before expiry, and the block is NOT extended: advance 5m and
	// the remaining should shrink toward ~10m, not reset to 15m.
	now = now.Add(5 * time.Minute)
	allowed, retry = s.Allow("amber", tok.ID)
	if allowed {
		t.Fatal("still within block window, should be blocked")
	}
	if retry > 10*time.Minute {
		t.Fatalf("retryAfter = %v after 5m, block must not extend (want <=10m)", retry)
	}

	// After the block expires, requests flow again (counters reset).
	now = now.Add(11 * time.Minute)
	if allowed, _ := s.Allow("amber", tok.ID); !allowed {
		t.Fatal("request after block expiry should be allowed")
	}
}

func TestNamedStore_Allow_WindowSlides(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := clockedStore(t, &now)
	tok, _ := s.Create("amber", "gps")
	s.Update("amber", tok.ID, 2, 15)

	if allowed, _ := s.Allow("amber", tok.ID); !allowed {
		t.Fatal("req 1 blocked")
	}
	if allowed, _ := s.Allow("amber", tok.ID); !allowed {
		t.Fatal("req 2 blocked")
	}
	// Move past the 60s window so the earlier hits prune out; new request is fine.
	now = now.Add(61 * time.Second)
	if allowed, _ := s.Allow("amber", tok.ID); !allowed {
		t.Fatal("req after window slide should be allowed")
	}
}

func TestNamedStore_ResetBlocks(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := clockedStore(t, &now)
	a, _ := s.Create("amber", "gps")
	b, _ := s.Create("amber", "alarm")
	s.Update("amber", a.ID, 1, 15)
	s.Update("amber", b.ID, 1, 15)

	// Trip both into a block.
	s.Allow("amber", a.ID)
	s.Allow("amber", a.ID)
	s.Allow("amber", b.ID)
	s.Allow("amber", b.ID)

	// Clear just "gps" by name.
	if n := s.ResetBlocks("amber", "gps"); n != 1 {
		t.Fatalf("ResetBlocks(gps) = %d, want 1", n)
	}
	if allowed, _ := s.Allow("amber", a.ID); !allowed {
		t.Fatal("gps should be unblocked after reset")
	}
	// alarm is still blocked; clear-all removes it.
	if n := s.ResetBlocks("amber", ""); n != 1 {
		t.Fatalf("ResetBlocks(all) = %d, want 1", n)
	}
}

func TestNamedStore_Quota(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := clockedStore(t, &now)
	tok, _ := s.Create("amber", "gps")
	s.Update("amber", tok.ID, 3, 15)

	s.Allow("amber", tok.ID)
	s.Allow("amber", tok.ID)

	q := s.Quota("amber")
	if len(q) != 1 {
		t.Fatalf("Quota len = %d, want 1", len(q))
	}
	if q[0].Name != "gps" || q[0].RatePerMin != 3 || q[0].HitsInWindow != 2 || q[0].Blocked {
		t.Fatalf("Quota = %+v, want gps rate=3 hits=2 unblocked", q[0])
	}

	// Trip into a block; Quota reflects Blocked + remaining.
	s.Allow("amber", tok.ID)
	s.Allow("amber", tok.ID)
	q = s.Quota("amber")
	if !q[0].Blocked || q[0].BlockRemaining <= 0 {
		t.Fatalf("Quota after trip = %+v, want blocked with remaining", q[0])
	}
}

func TestNamedStore_UpdatePersists(t *testing.T) {
	path := NamedTokenPath(t.TempDir())
	s1, _ := NewNamedStore(path)
	tok, _ := s1.Create("amber", "gps")
	if !s1.Update("amber", tok.ID, 12, 7) {
		t.Fatal("Update returned false")
	}
	// A reload sees the persisted config.
	s2, _ := NewNamedStore(path)
	got := s2.List("amber")
	if len(got) != 1 || got[0].RatePerMin != 12 || got[0].BlockMinutes != 7 {
		t.Fatalf("reloaded config = %+v, want rate=12 block=7", got)
	}
	if s1.Update("amber", "nope", 1, 1) {
		t.Fatal("Update(missing) should return false")
	}
}

func TestNamedStore_EmptyPathInMemory(t *testing.T) {
	s, err := NewNamedStore("")
	if err != nil {
		t.Fatalf("NewNamedStore(\"\"): %v", err)
	}
	tok, err := s.Create("amber", "x")
	if err != nil {
		t.Fatalf("Create on in-memory store: %v", err)
	}
	if id, ok := s.Validate(tok.Token); !ok || id != "amber" {
		t.Fatalf("in-memory Validate = (%q,%v)", id, ok)
	}
}
