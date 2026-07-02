// ClawEh
// License: MIT

package msgtoken

import (
	"os"
	"testing"
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
