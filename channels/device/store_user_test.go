package device

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// pairStoreDevice pairs deviceID (display name "Rabbit R1") in s.
func pairStoreDevice(t *testing.T, s *Store, deviceID string) {
	t.Helper()
	ctx := context.Background()
	reqID, err := s.CreatePending(ctx, PendingPairing{DeviceID: deviceID, PublicKey: "pk-" + deviceID, DisplayName: "Rabbit R1", Role: "node"})
	if err != nil {
		t.Fatalf("CreatePending: %v", err)
	}
	if _, _, err := s.Approve(ctx, reqID, nil, nil); err != nil {
		t.Fatalf("Approve: %v", err)
	}
}

// SetDeviceUser stores a trimmed name that GetPaired and ListPaired return,
// and "" clears it.
func TestSetDeviceUser_RoundTrip(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	pairStoreDevice(t, s, "dev1")

	if err := s.SetDeviceUser(ctx, "dev1", "  Alice \t"); err != nil {
		t.Fatalf("SetDeviceUser: %v", err)
	}
	dev, ok, err := s.GetPaired(ctx, "dev1")
	if err != nil || !ok || dev.UserName != "Alice" {
		t.Fatalf("GetPaired = %+v ok=%v err=%v, want UserName Alice", dev, ok, err)
	}
	list, err := s.ListPaired(ctx)
	if err != nil || len(list) != 1 || list[0].UserName != "Alice" {
		t.Fatalf("ListPaired = %+v err=%v, want one device with UserName Alice", list, err)
	}

	if err = s.SetDeviceUser(ctx, "dev1", ""); err != nil {
		t.Fatalf("SetDeviceUser clear: %v", err)
	}
	if dev, _, err = s.GetPaired(ctx, "dev1"); err != nil || dev.UserName != "" {
		t.Fatalf("after clear GetPaired = %+v err=%v, want UserName empty", dev, err)
	}
}

func TestSetDeviceUser_UnknownDevice(t *testing.T) {
	s := openTestStore(t)
	if err := s.SetDeviceUser(context.Background(), "missing", "Alice"); !errors.Is(err, ErrPairedNotFound) {
		t.Fatalf("SetDeviceUser(unknown) = %v, want ErrPairedNotFound", err)
	}
}

// A name longer than MaxDeviceUserNameLen characters, with a control
// character or not valid UTF-8 is refused and leaves the stored name alone;
// the length counts characters, not bytes.
func TestSetDeviceUser_RefusesInvalidNames(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	pairStoreDevice(t, s, "dev1")
	if err := s.SetDeviceUser(ctx, "dev1", "Alice"); err != nil {
		t.Fatalf("SetDeviceUser: %v", err)
	}

	for _, tc := range []struct {
		name, value string
	}{
		{"too long", strings.Repeat("a", MaxDeviceUserNameLen+1)},
		{"control character", "Al\x07ice"},
		{"newline inside", "Alice\nBob"},
		{"invalid UTF-8", "Al\xffice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.SetDeviceUser(ctx, "dev1", tc.value); !errors.Is(err, ErrInvalidUserName) {
				t.Fatalf("SetDeviceUser(%q) = %v, want ErrInvalidUserName", tc.value, err)
			}
			if dev, _, err := s.GetPaired(ctx, "dev1"); err != nil || dev.UserName != "Alice" {
				t.Fatalf("after the refusal GetPaired = %+v err=%v, want UserName Alice", dev, err)
			}
		})
	}

	for _, ok := range []string{strings.Repeat("a", MaxDeviceUserNameLen), strings.Repeat("é", MaxDeviceUserNameLen)} {
		if err := s.SetDeviceUser(ctx, "dev1", ok); err != nil {
			t.Fatalf("SetDeviceUser(%d characters) = %v, want accepted", MaxDeviceUserNameLen, err)
		}
	}
}

// A database created before agent_id and user_name existed gains both
// columns on open, and its devices read back with them empty.
func TestOpenStore_MigratesPairedDeviceColumns(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "gateway.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = old.ExecContext(ctx, `CREATE TABLE paired_devices (
  device_id       TEXT PRIMARY KEY,
  public_key      TEXT NOT NULL,
  display_name    TEXT,
  platform        TEXT,
  device_family   TEXT,
  client_id       TEXT,
  client_mode     TEXT,
  roles           TEXT,
  scopes          TEXT,
  created_at_ms   INTEGER NOT NULL,
  approved_at_ms  INTEGER NOT NULL,
  last_seen_at_ms INTEGER
)`); err != nil {
		t.Fatalf("create old schema: %v", err)
	}
	if _, err = old.ExecContext(ctx, `INSERT INTO paired_devices
		(device_id, public_key, display_name, platform, device_family, client_id, client_mode, roles, scopes, created_at_ms, approved_at_ms)
		VALUES ('dev-old','pk','Rabbit R1','','','','','["node"]','[]',1,1)`); err != nil {
		t.Fatalf("seed old row: %v", err)
	}
	if err = old.Close(); err != nil {
		t.Fatal(err)
	}

	// Twice: the second open finds the columns already there and keeps the
	// name the first pass stored.
	wantUser := ""
	for pass := 1; pass <= 2; pass++ {
		s, err := OpenStore(ctx, path)
		if err != nil {
			t.Fatalf("OpenStore pass %d: %v", pass, err)
		}
		dev, ok, err := s.GetPaired(ctx, "dev-old")
		if err != nil || !ok || dev.AgentID != "" || dev.UserName != wantUser || dev.DisplayName != "Rabbit R1" {
			t.Fatalf("pass %d: GetPaired = %+v ok=%v err=%v", pass, dev, ok, err)
		}
		if err := s.SetDeviceUser(ctx, "dev-old", "Alice"); err != nil {
			t.Fatalf("pass %d: SetDeviceUser on a migrated database: %v", pass, err)
		}
		wantUser = "Alice"
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
