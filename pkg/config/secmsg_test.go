package config

import "testing"

func TestSecMsgAccountChannelName(t *testing.T) {
	daemon := SecMsgConfig{Name: "signal"}
	tests := []struct {
		name    string
		account SecMsgAccountConfig
		daemon  SecMsgConfig
		want    string
	}{
		{"explicit name wins", SecMsgAccountConfig{Account: "droid1", Name: "work"}, daemon, "work"},
		{"derived from daemon + account", SecMsgAccountConfig{Account: "droid1"}, daemon, "signal-droid1"},
		{"auto-select account uses daemon name", SecMsgAccountConfig{}, daemon, "signal"},
		{"no daemon name falls back to secmsg", SecMsgAccountConfig{Account: "droid1"}, SecMsgConfig{}, "secmsg-droid1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.account.ChannelName(tt.daemon); got != tt.want {
				t.Errorf("ChannelName = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSecMsgBoundAccounts(t *testing.T) {
	// An empty accounts list synthesizes a single auto-selecting account so a
	// minimal daemon config still yields one channel.
	empty := SecMsgConfig{Name: "signal"}.BoundAccounts()
	if len(empty) != 1 || empty[0].Account != "" {
		t.Fatalf("empty accounts should synthesize one auto-select account, got %+v", empty)
	}

	two := SecMsgConfig{Accounts: []SecMsgAccountConfig{{Account: "droid1"}, {Account: "droid2"}}}.BoundAccounts()
	if len(two) != 2 {
		t.Fatalf("expected 2 accounts, got %d", len(two))
	}
}
