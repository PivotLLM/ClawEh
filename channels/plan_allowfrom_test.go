// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package channels

import (
	"testing"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
)

// registerFakeFactory registers a factory for name for the test only.
func registerFakeFactory(t *testing.T, name string) {
	t.Helper()
	factoriesMu.Lock()
	prev, had := factories[name]
	factoriesMu.Unlock()
	RegisterFactory(name, func(_ *config.Config, b *bus.MessageBus) (Channel, error) {
		return newLifecycleFake(name, b), nil
	})
	t.Cleanup(func() {
		factoriesMu.Lock()
		defer factoriesMu.Unlock()
		if had {
			factories[name] = prev
		} else {
			delete(factories, name)
		}
	})
}

// An empty allow_from on the device channel means any paired device, so it
// must not get the "nobody will be able to connect" warning; another channel
// with an empty allow_from still does.
func TestPlan_EmptyAllowFromWarning(t *testing.T) {
	registerFakeFactory(t, "device")
	registerFakeFactory(t, "webui")

	cfg := &config.Config{}
	cfg.Channels.Device.Enabled = true
	cfg.Channels.WebUI.Enabled = true
	cfg.Channels.WebUI.Token = "alice-token"

	m := &Manager{bus: bus.NewMessageBus()}
	checks := map[string]bool{}
	for _, s := range m.plan(cfg) {
		checks[s.name] = s.checkAllowFrom
	}
	if v, ok := checks["device"]; !ok || v {
		t.Errorf("device: planned %v, checkAllowFrom %v; want planned with no allow_from warning", ok, v)
	}
	if v, ok := checks["webui"]; !ok || !v {
		t.Errorf("webui: planned %v, checkAllowFrom %v; want the allow_from warning", ok, v)
	}
}
