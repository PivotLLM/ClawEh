// ClawEh
// License: MIT

package channels

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/media"
)

// failAfter bounds a wait for something the test expects to happen; it is a
// failure guard, never what the test's outcome depends on.
const failAfter = 5 * time.Second

// lifecycleFake is a channel that counts its starts and stops and hands every
// message it is sent to sent. When gate is set, Start waits for it to close.
type lifecycleFake struct {
	*BaseChannel
	gate    chan struct{}
	entered chan struct{} // closed when Start is entered, if set
	starts  atomic.Int32
	stops   atomic.Int32
	sent    chan bus.OutboundMessage
}

func newLifecycleFake(name string, b *bus.MessageBus) *lifecycleFake {
	return &lifecycleFake{
		BaseChannel: NewBaseChannel(name, nil, b, nil),
		sent:        make(chan bus.OutboundMessage, 16),
	}
}

func (f *lifecycleFake) Start(context.Context) error {
	f.starts.Add(1)
	if f.entered != nil {
		close(f.entered)
	}
	if f.gate != nil {
		<-f.gate
	}
	f.SetRunning(true)
	return nil
}

func (f *lifecycleFake) Stop(context.Context) error {
	f.stops.Add(1)
	f.SetRunning(false)
	return nil
}

func (f *lifecycleFake) Send(_ context.Context, msg bus.OutboundMessage) error {
	f.sent <- msg
	return nil
}

// receive returns the next message f is sent.
func (f *lifecycleFake) receive(t *testing.T) bus.OutboundMessage {
	t.Helper()
	select {
	case msg := <-f.sent:
		return msg
	case <-time.After(failAfter):
		t.Fatal("channel was not sent the message")
		return bus.OutboundMessage{}
	}
}

// fakeFactories registers counting factories for discord, slack and webui,
// whose inputs are their sections, and returns the last channel each built.
// The previous registrations are restored after the test.
type fakeFactories struct {
	mu    sync.Mutex
	built map[string]*lifecycleFake
	count map[string]int
}

func registerFakeFactories(t *testing.T) *fakeFactories {
	t.Helper()
	ff := &fakeFactories{built: map[string]*lifecycleFake{}, count: map[string]int{}}
	inputs := map[string]FactoryInputs{
		"discord": func(cfg *config.Config) any { return cfg.Channels.Discord },
		"slack":   func(cfg *config.Config) any { return cfg.Channels.Slack },
		"webui":   func(cfg *config.Config) any { return cfg.Channels.WebUI },
	}
	for name, in := range inputs {
		factoriesMu.Lock()
		prevF, hadF := factories[name]
		factoriesMu.Unlock()
		factoryInputsMu.Lock()
		prevI, hadI := factoryInputs[name]
		factoryInputsMu.Unlock()
		RegisterFactory(name, func(_ *config.Config, b *bus.MessageBus) (Channel, error) {
			ch := newLifecycleFake(name, b)
			ff.mu.Lock()
			ff.built[name] = ch
			ff.count[name]++
			ff.mu.Unlock()
			return ch, nil
		})
		RegisterFactoryInputs(name, in)
		t.Cleanup(func() {
			factoriesMu.Lock()
			if hadF {
				factories[name] = prevF
			} else {
				delete(factories, name)
			}
			factoriesMu.Unlock()
			factoryInputsMu.Lock()
			if hadI {
				factoryInputs[name] = prevI
			} else {
				delete(factoryInputs, name)
			}
			factoryInputsMu.Unlock()
		})
	}
	return ff
}

func (ff *fakeFactories) last(name string) *lifecycleFake {
	ff.mu.Lock()
	defer ff.mu.Unlock()
	return ff.built[name]
}

func (ff *fakeFactories) builds(name string) int {
	ff.mu.Lock()
	defer ff.mu.Unlock()
	return ff.count[name]
}

func discordAndSlack() *config.Config {
	cfg := &config.Config{}
	cfg.Channels.Discord = config.DiscordConfig{Enabled: true, Token: "d1", AllowFrom: config.FlexibleStringSlice{"*"}}
	cfg.Channels.Slack = config.SlackConfig{Enabled: true, BotToken: "s1", AllowFrom: config.FlexibleStringSlice{"*"}}
	return cfg
}

// startedManager builds and starts a manager for cfg, stopped after the test.
func startedManager(t *testing.T, cfg *config.Config, store media.MediaStore) (*Manager, *bus.MessageBus) {
	t.Helper()
	mb := bus.NewMessageBus()
	t.Cleanup(mb.Close)
	m, err := NewManager(cfg, mb, store)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := m.StartAll(context.Background()); err != nil {
		t.Fatalf("StartAll: %v", err)
	}
	t.Cleanup(func() {
		if err := m.StopAll(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return m, mb
}

func publish(t *testing.T, mb *bus.MessageBus, channel, content string) {
	t.Helper()
	if err := mb.PublishOutbound(context.Background(), bus.OutboundMessage{Channel: channel, ChatID: "c", Content: content}); err != nil {
		t.Fatalf("PublishOutbound: %v", err)
	}
}

// A channel whose Start blocks delays no other channel: the others start and
// deliver while it is still starting, StartAll (and with it readiness) waits
// for it, and what was sent to it meanwhile is delivered, in order, once it
// has started.
func TestStartAll_SlowChannelDelaysNoOther(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()
	m := newTestManager()
	m.bus = mb
	slow := newLifecycleFake("slow", mb)
	slow.gate = make(chan struct{})
	slow.entered = make(chan struct{})
	fast := newLifecycleFake("fast", mb)
	m.RegisterChannel("slow", slow)
	m.RegisterChannel("fast", fast)

	started := make(chan struct{})
	go func() {
		if err := m.StartAll(context.Background()); err != nil {
			t.Errorf("StartAll: %v", err)
		}
		close(started)
	}()
	defer func() {
		if err := m.StopAll(context.Background()); err != nil {
			t.Error(err)
		}
	}()

	select {
	case <-slow.entered:
	case <-time.After(failAfter):
		t.Fatal("slow channel's Start was not called")
	}
	publish(t, mb, "slow", "one")
	publish(t, mb, "slow", "two")
	publish(t, mb, "fast", "hello")
	if got := fast.receive(t); got.Content != "hello" {
		t.Fatalf("fast channel got %q", got.Content)
	}
	select {
	case <-started:
		t.Fatal("StartAll returned while a channel was still starting")
	default:
	}

	close(slow.gate)
	select {
	case <-started:
	case <-time.After(failAfter):
		t.Fatal("StartAll did not return once the slow channel started")
	}
	for _, want := range []string{"one", "two"} {
		if got := slow.receive(t); got.Content != want {
			t.Fatalf("slow channel got %q, want %q", got.Content, want)
		}
	}
}

// A reload keeps a channel whose settings did not change (same instance, no
// stop or start), rebuilds one whose settings changed, starts an added one
// and stops a removed one.
func TestReload_RebuildsOnlyChangedChannels(t *testing.T) {
	ff := registerFakeFactories(t)
	cfg := discordAndSlack()
	m, mb := startedManager(t, cfg, nil)
	discord1, slack1 := ff.last("discord"), ff.last("slack")

	// Unchanged: nothing happens.
	res := m.Reload(context.Background(), discordAndSlack(), nil)
	if !slices.Equal(res.Kept, []string{"discord", "slack"}) || len(res.Restarted)+len(res.Started)+len(res.Stopped) != 0 {
		t.Fatalf("unchanged reload = %+v", res)
	}
	if ch, _ := m.GetChannel("slack"); ch != slack1 {
		t.Fatal("unchanged channel replaced")
	}
	if slack1.starts.Load() != 1 || slack1.stops.Load() != 0 || discord1.stops.Load() != 0 {
		t.Fatal("unchanged channel stopped or restarted")
	}

	// Discord's token changes, Slack goes, WebUI comes.
	next := discordAndSlack()
	next.Channels.Discord.Token = "d2"
	next.Channels.Slack = config.SlackConfig{}
	next.Channels.WebUI = config.WebUIConfig{Enabled: true, Token: "w", AllowFrom: config.FlexibleStringSlice{"*"}}
	res = m.Reload(context.Background(), next, nil)
	want := ReloadResult{Restarted: []string{"discord"}, Started: []string{"webui"}, Stopped: []string{"slack"}}
	if !slices.Equal(res.Kept, want.Kept) || !slices.Equal(res.Restarted, want.Restarted) ||
		!slices.Equal(res.Started, want.Started) || !slices.Equal(res.Stopped, want.Stopped) {
		t.Fatalf("reload = %+v, want %+v", res, want)
	}
	discord2 := ff.last("discord")
	if discord2 == discord1 || discord1.stops.Load() != 1 || discord2.starts.Load() != 1 {
		t.Fatal("changed channel not rebuilt")
	}
	if slack1.stops.Load() != 1 {
		t.Fatal("removed channel not stopped")
	}
	if _, ok := m.GetChannel("slack"); ok {
		t.Fatal("removed channel still registered")
	}

	// The rebuilt and added channels deliver; the removed one is unknown.
	publish(t, mb, "discord", "to discord")
	if got := discord2.receive(t); got.Content != "to discord" {
		t.Fatalf("rebuilt channel got %q", got.Content)
	}
	publish(t, mb, "webui", "to webui")
	if got := ff.last("webui").receive(t); got.Content != "to webui" {
		t.Fatalf("added channel got %q", got.Content)
	}
	if err := m.SendMessage(context.Background(), bus.OutboundMessage{Channel: "slack", ChatID: "c", Content: "x"}); err == nil {
		t.Fatal("send to a removed channel succeeded")
	}
}

// Messages sent to a channel while a reload rebuilds it reach the new
// instance, in order, once it has started.
func TestReload_HoldsMessagesForRebuiltChannel(t *testing.T) {
	ff := registerFakeFactories(t)
	m, mb := startedManager(t, discordAndSlack(), nil)
	old := ff.last("discord")

	// The rebuilt channel's Start waits: the factory runs inside Reload, so
	// gate it through the next build.
	gate := make(chan struct{})
	entered := make(chan struct{})
	RegisterFactory("discord", func(_ *config.Config, b *bus.MessageBus) (Channel, error) {
		ch := newLifecycleFake("discord", b)
		ch.gate, ch.entered = gate, entered
		ff.mu.Lock()
		ff.built["discord"] = ch
		ff.mu.Unlock()
		return ch, nil
	})

	next := discordAndSlack()
	next.Channels.Discord.Token = "d2"
	reloaded := make(chan struct{})
	go func() {
		m.Reload(context.Background(), next, nil)
		close(reloaded)
	}()
	select {
	case <-entered:
	case <-time.After(failAfter):
		t.Fatal("rebuilt channel's Start was not called")
	}
	publish(t, mb, "discord", "one")
	publish(t, mb, "discord", "two")
	close(gate)
	<-reloaded

	rebuilt := ff.last("discord")
	for _, want := range []string{"one", "two"} {
		if got := rebuilt.receive(t); got.Content != want {
			t.Fatalf("rebuilt channel got %q, want %q", got.Content, want)
		}
	}
	select {
	case msg := <-old.sent:
		t.Fatalf("old instance was sent %q", msg.Content)
	default:
	}
}

// What every channel is given and nothing reads as an input: a change to the
// media store rebuilds every channel; a change to voice settings, which no
// channel reads (transcription is set on the agent loop), rebuilds none.
func TestReload_SharedInputs(t *testing.T) {
	ff := registerFakeFactories(t)
	store := media.NewFileMediaStore()
	m, _ := startedManager(t, discordAndSlack(), store)

	voice := discordAndSlack()
	voice.Voice.EchoTranscription = true
	if res := m.Reload(context.Background(), voice, store); len(res.Kept) != 2 {
		t.Fatalf("voice change: %+v, want both kept", res)
	}

	if res := m.Reload(context.Background(), voice, media.NewFileMediaStore()); !slices.Equal(res.Restarted, []string{"discord", "slack"}) {
		t.Fatalf("media store change: %+v, want both restarted", res)
	}
	if ff.builds("discord") != 2 || ff.builds("slack") != 2 {
		t.Fatal("media store change did not rebuild every channel")
	}
}

// A channel whose inputs cannot be compared (no registered inputs, or a
// built-in) is rebuilt on every reload.
func TestReload_UncomparableInputsRebuild(t *testing.T) {
	ff := registerFakeFactories(t)
	factoryInputsMu.Lock()
	delete(factoryInputs, "slack")
	factoryInputsMu.Unlock()
	registerTestBuiltin(t, "testbuiltin", func(_ *config.Config, b *bus.MessageBus) (Channel, error) {
		return newLifecycleFake("testbuiltin", b), nil
	})

	m, _ := startedManager(t, discordAndSlack(), nil)
	res := m.Reload(context.Background(), discordAndSlack(), nil)
	if !slices.Equal(res.Kept, []string{"discord"}) || !slices.Equal(res.Restarted, []string{"slack", "testbuiltin"}) {
		t.Fatalf("reload = %+v, want discord kept, slack and the built-in restarted", res)
	}
	if ff.builds("slack") != 2 {
		t.Fatal("slack not rebuilt")
	}
}
