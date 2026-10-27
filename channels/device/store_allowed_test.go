package device

import (
	"context"
	"errors"
	"slices"
	"testing"
)

// SetDeviceAllowedAgents stores normalised, de-duplicated ids in their first
// order, which GetPaired and ListPaired return; an empty or blank list
// stores "every agent".
func TestSetDeviceAllowedAgents_RoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		input []string
		want  []string
	}{
		{"normalised and de-duplicated", []string{"Bob", "alice", "BOB", " ", "Alice"}, []string{"bob", "alice"}},
		{"single", []string{"alice"}, []string{"alice"}},
		{"empty means all", []string{}, nil},
		{"nil means all", nil, nil},
		{"only blanks means all", []string{"", "  "}, nil},
		{"invalid ids skipped", []string{"not valid", "-bob", "alice"}, []string{"alice"}},
		{"only invalid ids means all", []string{"not valid"}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := openTestStore(t)
			pairStoreDevice(t, s, "dev1")
			ctx := context.Background()
			if err := s.SetDeviceAllowedAgents(ctx, "dev1", []string{"third"}); err != nil {
				t.Fatalf("SetDeviceAllowedAgents seed: %v", err)
			}
			if err := s.SetDeviceAllowedAgents(ctx, "dev1", tc.input); err != nil {
				t.Fatalf("SetDeviceAllowedAgents: %v", err)
			}
			dev, ok, err := s.GetPaired(ctx, "dev1")
			if err != nil || !ok || !slices.Equal(dev.AllowedAgents, tc.want) {
				t.Fatalf("GetPaired = %+v ok=%v err=%v, want AllowedAgents %v", dev, ok, err, tc.want)
			}
			list, err := s.ListPaired(ctx)
			if err != nil || len(list) != 1 || !slices.Equal(list[0].AllowedAgents, tc.want) {
				t.Fatalf("ListPaired = %+v err=%v, want AllowedAgents %v", list, err, tc.want)
			}
		})
	}
}

func TestSetDeviceAllowedAgents_UnknownDevice(t *testing.T) {
	s := openTestStore(t)
	if err := s.SetDeviceAllowedAgents(context.Background(), "missing", []string{"alice"}); !errors.Is(err, ErrPairedNotFound) {
		t.Fatalf("SetDeviceAllowedAgents(unknown) = %v, want ErrPairedNotFound", err)
	}
}

// Re-approving a paired device keeps its allowed set.
func TestSetDeviceAllowedAgents_KeptOnReapproval(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	pairStoreDevice(t, s, "dev1")
	if err := s.SetDeviceAllowedAgents(ctx, "dev1", []string{"bob"}); err != nil {
		t.Fatalf("SetDeviceAllowedAgents: %v", err)
	}
	pairStoreDevice(t, s, "dev1")
	dev, ok, err := s.GetPaired(ctx, "dev1")
	if err != nil || !ok || !slices.Equal(dev.AllowedAgents, []string{"bob"}) {
		t.Fatalf("after re-approval GetPaired = %+v ok=%v err=%v, want AllowedAgents [bob]", dev, ok, err)
	}
}

func TestPairedDeviceAllows(t *testing.T) {
	tests := []struct {
		name    string
		allowed []string
		agent   string
		want    bool
	}{
		{"no restriction", nil, "alice", true},
		{"listed", []string{"bob"}, "bob", true},
		{"listed, other case", []string{"bob"}, "Bob", true},
		{"not listed", []string{"bob"}, "alice", false},
		{"blank id", []string{"bob"}, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := PairedDevice{AllowedAgents: tc.allowed}
			if got := d.Allows(tc.agent); got != tc.want {
				t.Fatalf("Allows(%q) with %v = %v, want %v", tc.agent, tc.allowed, got, tc.want)
			}
		})
	}
}

// A corrupted allowed_agents column is an error from GetPaired (the device
// is refused), never "every agent"; ListPaired keeps the row with no set so
// one bad row does not hide the others.
func TestAllowedAgents_CorruptedColumnIsAnError(t *testing.T) {
	for _, value := range []string{"not json", `{"a":1}`, `"bob"`, "null", `[1,2]`} {
		t.Run(value, func(t *testing.T) {
			s := openTestStore(t)
			pairStoreDevice(t, s, "dev1")
			ctx := context.Background()
			if _, err := s.db.ExecContext(ctx, `UPDATE paired_devices SET allowed_agents=? WHERE device_id='dev1'`, value); err != nil {
				t.Fatal(err)
			}
			if dev, ok, err := s.GetPaired(ctx, "dev1"); err == nil {
				t.Fatalf("GetPaired = %+v ok=%v, want an error", dev, ok)
			}
			list, err := s.ListPaired(ctx)
			if err != nil || len(list) != 1 || list[0].DeviceID != "dev1" || list[0].AllowedAgents != nil {
				t.Fatalf("ListPaired = %+v err=%v, want the row with no allowed set", list, err)
			}
		})
	}
}
