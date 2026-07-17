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

func TestSecMsgWithDefaults(t *testing.T) {
	daemon := SecMsgConfig{
		Name:         "signal",
		AllowFrom:    FlexibleStringSlice{"+15551112222"},
		GroupTrigger: GroupTriggerConfig{MentionOnly: true},
	}

	// An account that sets nothing inherits the daemon-level defaults — this is
	// the shape a discovered account arrives in.
	inherited := daemon.WithDefaults(SecMsgAccountConfig{Account: "droid1"})
	if len(inherited.AllowFrom) != 1 || inherited.AllowFrom[0] != "+15551112222" {
		t.Errorf("allow_from not inherited: %+v", inherited.AllowFrom)
	}
	if !inherited.GroupTrigger.MentionOnly {
		t.Errorf("group_trigger not inherited: %+v", inherited.GroupTrigger)
	}

	// An account with its own values overrides the daemon defaults.
	override := daemon.WithDefaults(SecMsgAccountConfig{
		Account:      "droid2",
		AllowFrom:    FlexibleStringSlice{"+15553334444"},
		GroupTrigger: GroupTriggerConfig{Prefixes: []string{"!"}},
	})
	if len(override.AllowFrom) != 1 || override.AllowFrom[0] != "+15553334444" {
		t.Errorf("allow_from override lost: %+v", override.AllowFrom)
	}
	if override.GroupTrigger.MentionOnly || len(override.GroupTrigger.Prefixes) != 1 {
		t.Errorf("group_trigger override lost: %+v", override.GroupTrigger)
	}
}
