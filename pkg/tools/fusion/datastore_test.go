// ClawEh
// License: MIT

package fusion

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *sqliteDataStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state", "fusion-tokens.db")
	ds, err := NewSQLiteDataStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteDataStore: %v", err)
	}
	s, ok := ds.(*sqliteDataStore)
	if !ok {
		t.Fatalf("NewSQLiteDataStore returned %T, want *sqliteDataStore", ds)
	}
	t.Cleanup(func() { _ = s.db.Close() })
	return s
}

func TestDataStore_RoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	want := []byte(`{"token":"abc"}`)
	if err := s.Set(ctx, "oauth", "alice/graph", want); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, ok, err := s.Get(ctx, "oauth", "alice/graph")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("Get: ok=false for a value that was just Set")
	}
	if !bytes.Equal(got, want) {
		t.Errorf("Get value = %q, want %q", got, want)
	}

	// Set overwrites the prior value under the same (collection,key).
	next := []byte(`{"token":"xyz"}`)
	if err := s.Set(ctx, "oauth", "alice/graph", next); err != nil {
		t.Fatalf("Set overwrite: %v", err)
	}
	got, _, err = s.Get(ctx, "oauth", "alice/graph")
	if err != nil {
		t.Fatalf("Get after overwrite: %v", err)
	}
	if !bytes.Equal(got, next) {
		t.Errorf("after overwrite value = %q, want %q", got, next)
	}
}

func TestDataStore_CollectionIsolation(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.Set(ctx, "oauth", "k", []byte("oauth-val")); err != nil {
		t.Fatalf("Set oauth: %v", err)
	}
	if err := s.Set(ctx, "creds", "k", []byte("creds-val")); err != nil {
		t.Fatalf("Set creds: %v", err)
	}

	got, ok, err := s.Get(ctx, "oauth", "k")
	if err != nil || !ok {
		t.Fatalf("Get oauth: ok=%v err=%v", ok, err)
	}
	if string(got) != "oauth-val" {
		t.Errorf("oauth/k = %q, want oauth-val (collection bleed)", got)
	}
	got, _, _ = s.Get(ctx, "creds", "k")
	if string(got) != "creds-val" {
		t.Errorf("creds/k = %q, want creds-val (collection bleed)", got)
	}
}

func TestDataStore_AbsentKey(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	got, ok, err := s.Get(ctx, "oauth", "missing")
	if err != nil {
		t.Fatalf("Get absent: unexpected error %v", err)
	}
	if ok {
		t.Error("Get absent: ok=true, want false")
	}
	if got != nil {
		t.Errorf("Get absent: value=%q, want nil", got)
	}
}

func TestDataStore_DeleteIdempotent(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.Set(ctx, "authcodes", "code1", []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Delete(ctx, "authcodes", "code1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, _ := s.Get(ctx, "authcodes", "code1"); ok {
		t.Error("Get after Delete: ok=true, want false")
	}
	// Deleting an absent record is not an error.
	if err := s.Delete(ctx, "authcodes", "code1"); err != nil {
		t.Errorf("Delete absent: unexpected error %v", err)
	}
}
