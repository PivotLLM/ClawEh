// ClawEh
// License: MIT

package channels

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
)

// builtinFake is a built-in channel that hands every message it is sent to a
// channel the test reads.
type builtinFake struct {
	*BaseChannel
	sent chan bus.OutboundMessage
}

func (f *builtinFake) Start(context.Context) error { f.SetRunning(true); return nil }
func (f *builtinFake) Stop(context.Context) error  { f.SetRunning(false); return nil }
func (f *builtinFake) Send(_ context.Context, msg bus.OutboundMessage) error {
	f.sent <- msg
	return nil
}

// registerTestBuiltin registers a built-in for the test and removes it after.
func registerTestBuiltin(t *testing.T, name string, f ChannelFactory) {
	t.Helper()
	RegisterBuiltin(name, f)
	t.Cleanup(func() {
		builtinsMu.Lock()
		delete(builtins, name)
		builtinsMu.Unlock()
	})
}

// TestBuiltin_BuiltOnEveryManager: a registered built-in is part of every
// manager built (a reload builds a new one) with no configuration for it, is
// started with a worker, and receives its outbound messages, outcome
// included.
func TestBuiltin_BuiltOnEveryManager(t *testing.T) {
	built := 0
	var last *builtinFake
	registerTestBuiltin(t, "testbuiltin", func(_ *config.Config, b *bus.MessageBus) (Channel, error) {
		built++
		last = &builtinFake{BaseChannel: NewBaseChannel("testbuiltin", nil, b, nil), sent: make(chan bus.OutboundMessage, 1)}
		return last, nil
	})

	cfg := &config.Config{}
	for i := range 2 { // initial build, then a reload's rebuild
		msgBus := bus.NewMessageBus()
		m, err := NewManager(cfg, msgBus, nil)
		if err != nil {
			t.Fatalf("NewManager: %v", err)
		}
		if _, ok := m.GetChannel("testbuiltin"); !ok {
			t.Fatalf("build %d: built-in channel missing", i)
		}
		ctx, cancel := context.WithCancel(context.Background())
		if err := m.StartAll(ctx); err != nil {
			t.Fatalf("StartAll: %v", err)
		}
		want := bus.OutboundMessage{Channel: "testbuiltin", ChatID: "f/w/1", Content: "x", Outcome: bus.OutcomeOK}
		if err := msgBus.PublishOutbound(ctx, want); err != nil {
			t.Fatalf("PublishOutbound: %v", err)
		}
		select {
		case got := <-last.sent:
			if got != want {
				t.Fatalf("built-in got %+v, want %+v", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("built-in channel did not receive its outbound message")
		}
		if err := m.StopAll(context.Background()); err != nil {
			t.Fatalf("StopAll: %v", err)
		}
		cancel()
	}
	if built != 2 {
		t.Fatalf("factory ran %d times, want once per manager", built)
	}
}

// TestBuiltin_NoneRegistered: without a built-in, an empty configuration
// still builds no channels.
func TestBuiltin_NoneRegistered(t *testing.T) {
	m, err := NewManager(&config.Config{}, bus.NewMessageBus(), nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if n := len(m.GetEnabledChannels()); n != 0 {
		t.Fatalf("enabled channels = %d, want 0", n)
	}
}

// TestBuiltin_SkippedOrFailed: a factory that declines (nil channel) or fails
// adds nothing, and a configured channel of the same name is kept.
func TestBuiltin_SkippedOrFailed(t *testing.T) {
	registerTestBuiltin(t, "declines", func(*config.Config, *bus.MessageBus) (Channel, error) { return nil, nil })
	registerTestBuiltin(t, "fails", func(*config.Config, *bus.MessageBus) (Channel, error) { return nil, errors.New("no") })
	m, err := NewManager(&config.Config{}, bus.NewMessageBus(), nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if n := len(m.GetEnabledChannels()); n != 0 {
		t.Fatalf("enabled channels = %v, want none", m.GetEnabledChannels())
	}

	configured := &builtinFake{BaseChannel: NewBaseChannel("dup", nil, nil, nil)}
	registerTestBuiltin(t, "dup", func(*config.Config, *bus.MessageBus) (Channel, error) {
		t.Fatal("built-in factory ran for a name a configured channel holds")
		return nil, nil
	})
	m = &Manager{channels: map[string]Channel{"dup": configured}, workers: map[string]*channelWorker{}, config: &config.Config{}}
	m.initBuiltins()
	if ch, _ := m.GetChannel("dup"); ch != configured {
		t.Fatal("configured channel replaced by the built-in")
	}
}

// TestRegisterBuiltin_RejectsInvalid: an internal name (whose outbound is
// dropped), an empty name or a nil factory is a programming error.
func TestRegisterBuiltin_RejectsInvalid(t *testing.T) {
	f := func(*config.Config, *bus.MessageBus) (Channel, error) { return nil, nil }
	for _, tc := range []struct {
		name string
		f    ChannelFactory
	}{{"system", f}, {"", f}, {"ok", nil}} {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("RegisterBuiltin(%q) did not panic", tc.name)
				}
			}()
			RegisterBuiltin(tc.name, tc.f)
		}()
	}
}
