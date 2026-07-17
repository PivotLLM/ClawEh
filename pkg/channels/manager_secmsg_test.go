package channels

import (
	"context"
	"errors"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/config"
)

// withDiscovery swaps in a fake SecMsg discovery function for the duration of a
// test, restoring the previous registration afterward.
func withDiscovery(t *testing.T, f SecMsgDiscovery) {
	t.Helper()
	secmsgDiscoveryMu.Lock()
	prev := secmsgDiscovery
	secmsgDiscoveryMu.Unlock()
	RegisterSecMsgDiscovery(f)
	t.Cleanup(func() {
		secmsgDiscoveryMu.Lock()
		secmsgDiscovery = prev
		secmsgDiscoveryMu.Unlock()
	})
}

func TestResolveSecMsgAccounts_Discovery(t *testing.T) {
	withDiscovery(t, func(_ context.Context, addr string) ([]string, error) {
		if addr != "127.0.0.1:9600" {
			t.Errorf("unexpected address %q", addr)
		}
		return []string{"droid1", "droid2"}, nil
	})

	m := &Manager{}
	cfg := config.SecMsgConfig{
		Name:      "signal",
		Address:   "127.0.0.1:9600",
		AllowFrom: config.FlexibleStringSlice{"+15551112222"},
	}

	got := m.resolveSecMsgAccounts(cfg)
	if len(got) != 2 {
		t.Fatalf("expected 2 discovered accounts, got %d: %+v", len(got), got)
	}
	for i, want := range []string{"droid1", "droid2"} {
		if got[i].Account != want {
			t.Errorf("account[%d] = %q, want %q", i, got[i].Account, want)
		}
		if len(got[i].AllowFrom) != 1 || got[i].AllowFrom[0] != "+15551112222" {
			t.Errorf("account[%d] did not inherit daemon allow_from: %+v", i, got[i].AllowFrom)
		}
	}
}

func TestResolveSecMsgAccounts_DiscoveryFailureBindsNothing(t *testing.T) {
	withDiscovery(t, func(_ context.Context, _ string) ([]string, error) {
		return nil, errors.New("dial: connection refused")
	})

	m := &Manager{}
	got := m.resolveSecMsgAccounts(config.SecMsgConfig{Name: "signal", Address: "127.0.0.1:9600"})
	if got != nil {
		t.Fatalf("discovery failure should bind no accounts, got %+v", got)
	}
}

func TestResolveSecMsgAccounts_NoLinkedAccountsBindsNothing(t *testing.T) {
	withDiscovery(t, func(_ context.Context, _ string) ([]string, error) {
		return nil, nil
	})

	m := &Manager{}
	got := m.resolveSecMsgAccounts(config.SecMsgConfig{Name: "signal", Address: "127.0.0.1:9600"})
	if got != nil {
		t.Fatalf("no linked accounts should bind nothing, got %+v", got)
	}
}

func TestResolveSecMsgAccounts_ExplicitAccountsSkipDiscovery(t *testing.T) {
	withDiscovery(t, func(_ context.Context, _ string) ([]string, error) {
		t.Fatal("discovery must not run when accounts are pinned")
		return nil, nil
	})

	m := &Manager{}
	cfg := config.SecMsgConfig{
		Name:      "signal",
		Address:   "127.0.0.1:9600",
		AllowFrom: config.FlexibleStringSlice{"+15551112222"},
		Accounts: []config.SecMsgAccountConfig{
			{Account: "droid1"}, // inherits daemon allow_from
			{Account: "droid2", AllowFrom: config.FlexibleStringSlice{"+9"}}, // overrides
		},
	}

	got := m.resolveSecMsgAccounts(cfg)
	if len(got) != 2 {
		t.Fatalf("expected 2 pinned accounts, got %d", len(got))
	}
	if got[0].AllowFrom[0] != "+15551112222" {
		t.Errorf("pinned account[0] should inherit daemon allow_from, got %+v", got[0].AllowFrom)
	}
	if got[1].AllowFrom[0] != "+9" {
		t.Errorf("pinned account[1] should keep its own allow_from, got %+v", got[1].AllowFrom)
	}
}
