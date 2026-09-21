// ClawEh
// License: MIT

package audit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/channels/device"
)

func TestCollectDevices_NoStoreIsUnavailableNotCreated(t *testing.T) {
	cfg, env := fixtureConfig(t)
	s := collectDevices(cfg, env)
	paired := findTable(t, s, "Paired devices")
	contains(t, paired.Rows[0][0], "unavailable: no device store at", "missing store")
	if _, err := os.Stat(filepath.Join(env.DataDir, "state", "gateway.db")); !os.IsNotExist(err) {
		t.Error("the audit must not create the device store")
	}
	st := findTable(t, s, "Settings")
	_, gw := findRow(t, st, "Device gateway")
	if gw[1] != "on" {
		t.Errorf("device gateway = %q", gw[1])
	}
}

func TestCollectDevices_ListsPairedAndPending(t *testing.T) {
	cfg, env := fixtureConfig(t)
	stateDir := filepath.Join(env.DataDir, "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	store, err := device.OpenStore(ctx, filepath.Join(stateDir, "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	reqID, err := store.CreatePending(ctx, device.PendingPairing{
		DeviceID: "dev-1", DisplayName: "Rabbit", Platform: "r1", Role: "operator", RemoteIP: "192.168.1.9",
	})
	if err != nil {
		t.Fatal(err)
	}
	paired, tokens, err := store.Approve(ctx, reqID, []string{"operator"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetDeviceAgent(ctx, paired.DeviceID, "bob"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreatePending(ctx, device.PendingPairing{
		DeviceID: "dev-2", DisplayName: "Watch", Platform: "wear", Role: "operator", RemoteIP: "10.0.0.5",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	s := collectDevices(cfg, env)
	pt := findTable(t, s, "Paired devices")
	_, r := findRow(t, pt, "Rabbit (r1)")
	if r[1] != "dev-1" || r[2] != "bob" || r[3] == unknown {
		t.Errorf("paired row = %v", r)
	}
	pend := findTable(t, s, "Pending pairings")
	_, p := findRow(t, pend, "Watch (wear)")
	if p[1] != "dev-2" || p[2] != "10.0.0.5" {
		t.Errorf("pending row = %v", p)
	}
	txt := RenderText(&Report{Sections: []Section{s}})
	for _, tok := range tokens {
		if strings.Contains(txt, tok.Token) {
			t.Error("device token value leaked")
		}
	}
}
