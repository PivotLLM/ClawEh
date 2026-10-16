// ClawEh
// License: MIT

package channels

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/clock"
	"github.com/PivotLLM/ClawEh/media"
)

// failAfter bounds a wait for something the test expects to happen; it is a
// failure guard, never what the test's outcome depends on.
const failAfter = 5 * time.Second

// lifecycleFake is a channel that counts its starts and stops and hands every
// message it is sent to sent. Every Start signals entered; while gate is set
// Start waits for it to close; the first failStarts starts fail. Every Stop
// signals stopped.
type lifecycleFake struct {
	*BaseChannel
	gate       chan struct{}
	entered    chan struct{}
	stopped    chan struct{}
	failStarts int32
	starts     atomic.Int32
	stops      atomic.Int32
	sent       chan bus.OutboundMessage
}

func newLifecycleFake(name string, b *bus.MessageBus) *lifecycleFake {
	return &lifecycleFake{
		BaseChannel: NewBaseChannel(name, nil, b, nil),
		entered:     make(chan struct{}, 16),
		stopped:     make(chan struct{}, 16),
		sent:        make(chan bus.OutboundMessage, 16),
	}
}

func (f *lifecycleFake) Start(context.Context) error {
	n := f.starts.Add(1)
	f.entered <- struct{}{}
	if f.gate != nil {
		<-f.gate
	}
	if n <= f.failStarts {
		return errors.New("service unreachable")
	}
	f.SetRunning(true)
	return nil
}

func (f *lifecycleFake) Stop(context.Context) error {
	f.stops.Add(1)
	f.SetRunning(false)
	f.stopped <- struct{}{}
	return nil
}

// await waits for a signal on c.
func await(t *testing.T, c <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(failAfter):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// routeText routes msg as the outbound dispatcher does, synchronously.
func routeText(m *Manager, msg bus.OutboundMessage) {
	routeOutbound(context.Background(), m, msg.Channel, msg,
		func(w *channelWorker) chan bus.OutboundMessage { return w.queue },
		func(w *channelWorker) <-chan struct{} { return w.done },
		holdText, reportDelivery, "unknown", "no worker")
}

// heldTexts is what is held for channel name.
func heldTexts(m *Manager, name string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	h := m.held[name]
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, msg := range h.text {
		out = append(out, msg.Content)
	}
	return out
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
	// onBuild, when set, adjusts the n-th channel built for name (from 1)
	// before it is returned, or fails the build with its error.
	onBuild func(name string, n int, ch *lifecycleFake) error
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
			ff.count[name]++
			n, onBuild := ff.count[name], ff.onBuild
			ff.mu.Unlock()
			if onBuild != nil {
				if err := onBuild(name, n, ch); err != nil {
					return nil, err
				}
			}
			ff.mu.Lock()
			ff.built[name] = ch
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

	await(t, slow.entered, "the slow channel's Start")
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

// Messages sent to a channel while a reload rebuilds it are held, and reach
// the new instance, in order, once it has started.
func TestReload_HoldsMessagesForRebuiltChannel(t *testing.T) {
	ff := registerFakeFactories(t)
	m, _ := startedManager(t, discordAndSlack(), nil)
	old := ff.last("discord")

	// The rebuilt channel's Start waits for the gate.
	gate := make(chan struct{})
	builtCh := make(chan *lifecycleFake, 1)
	ff.onBuild = func(name string, n int, ch *lifecycleFake) error {
		if name == "discord" && n == 2 {
			ch.gate = gate
			builtCh <- ch
		}
		return nil
	}

	next := discordAndSlack()
	next.Channels.Discord.Token = "d2"
	reloaded := make(chan struct{})
	go func() {
		m.Reload(context.Background(), next, nil)
		close(reloaded)
	}()
	var rebuilt *lifecycleFake
	select {
	case rebuilt = <-builtCh:
	case <-time.After(failAfter):
		t.Fatal("discord was not rebuilt")
	}
	await(t, rebuilt.entered, "the rebuilt channel's Start")
	routeText(m, bus.OutboundMessage{Channel: "discord", ChatID: "c", Content: "one"})
	routeText(m, bus.OutboundMessage{Channel: "discord", ChatID: "c", Content: "two"})
	if got := heldTexts(m, "discord"); !slices.Equal(got, []string{"one", "two"}) {
		t.Fatalf("held while starting: %q, want [one two]", got)
	}
	close(gate)
	await(t, reloaded, "the reload")

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

// A rebuild whose build fails drops what was held for the channel as not
// running, and leaves no channel or worker behind.
func TestReload_FailedRebuildDropsHeldMessages(t *testing.T) {
	ff := registerFakeFactories(t)
	m, _ := startedManager(t, discordAndSlack(), nil)

	var dropped []error
	ff.onBuild = func(name string, n int, _ *lifecycleFake) error {
		if name != "discord" || n != 2 {
			return nil
		}
		// The build runs inside Reload, while the hold is in place.
		for _, content := range []string{"one", "two"} {
			routeText(m, bus.OutboundMessage{
				Channel: "discord", ChatID: "c", Content: content,
				OnDelivery: func(err error) { dropped = append(dropped, err) },
			})
		}
		return errors.New("bad token")
	}
	next := discordAndSlack()
	next.Channels.Discord.Token = "d2"
	m.Reload(context.Background(), next, nil)

	if len(dropped) != 2 || !errors.Is(dropped[0], ErrNotRunning) || !errors.Is(dropped[1], ErrNotRunning) {
		t.Fatalf("held messages reported %v, want two ErrNotRunning", dropped)
	}
	m.mu.RLock()
	_, ch := m.channels["discord"]
	_, w := m.workers["discord"]
	_, h := m.held["discord"]
	m.mu.RUnlock()
	if ch || w || h {
		t.Fatalf("failed rebuild left channel=%v worker=%v hold=%v", ch, w, h)
	}
}

// A reload that rebuilds a channel still retrying its start ends the retries
// of the old instance: a retry that succeeds after it was taken out of service
// stops it again and registers no worker; the new instance's worker stays.
func TestReload_ChannelStillRetryingItsStart(t *testing.T) {
	ff := registerFakeFactories(t)
	clk := clock.NewFake(time.Unix(0, 0))
	gate := make(chan struct{})
	ff.onBuild = func(name string, n int, ch *lifecycleFake) error {
		if name == "discord" && n == 1 {
			ch.failStarts = 1 // the first attempt fails, the retry waits for the gate
		}
		return nil
	}
	mb := bus.NewMessageBus()
	t.Cleanup(mb.Close)
	m, err := NewManager(discordAndSlack(), mb, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.clock = clk
	old := ff.last("discord")
	if err := m.StartAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.StopAll(context.Background()); err != nil {
			t.Error(err)
		}
	})
	await(t, old.entered, "the first start")

	// Let the retry begin and hold it inside Start.
	old.gate = gate
	if err := clk.BlockUntil(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	clk.Advance(2 * StartRetryMin)
	await(t, old.entered, "the retry")

	next := discordAndSlack()
	next.Channels.Discord.Token = "d2"
	if res := m.Reload(context.Background(), next, nil); !slices.Equal(res.Restarted, []string{"discord"}) {
		t.Fatalf("reload = %+v, want discord restarted", res)
	}
	await(t, old.stopped, "the old instance's stop")

	close(gate) // the old retry now succeeds, too late
	await(t, old.stopped, "the late success undone")

	rebuilt := ff.last("discord")
	m.mu.RLock()
	w := m.workers["discord"]
	m.mu.RUnlock()
	if w == nil || w.ch != rebuilt {
		t.Fatal("the worker is not the rebuilt channel's")
	}
}

// StopAll while a channel's first Start is blocked waits for the start to
// finish, then stops everything; nothing is left running.
func TestStopAll_WhileStartBlocked(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()
	m := newTestManager()
	m.bus = mb
	slow := newLifecycleFake("slow", mb)
	slow.gate = make(chan struct{})
	m.RegisterChannel("slow", slow)

	started := make(chan struct{})
	go func() {
		if err := m.StartAll(context.Background()); err != nil {
			t.Errorf("StartAll: %v", err)
		}
		close(started)
	}()
	await(t, slow.entered, "the slow channel's Start")

	stopped := make(chan struct{})
	go func() {
		if err := m.StopAll(context.Background()); err != nil {
			t.Errorf("StopAll: %v", err)
		}
		close(stopped)
	}()
	close(slow.gate)
	await(t, started, "StartAll")
	await(t, stopped, "StopAll")
	if slow.IsRunning() || slow.stops.Load() != 1 {
		t.Fatalf("running=%v stops=%d after StopAll", slow.IsRunning(), slow.stops.Load())
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

// removalFake records whether the manager told it it was removed.
type removalFake struct {
	*lifecycleFake
	removed atomic.Bool
}

func (f *removalFake) Removed() { f.removed.Store(true) }

// A channel the configuration no longer asks for is told so (RemovalAware); a
// rebuilt one is not.
func TestReload_TellsRemovedChannels(t *testing.T) {
	registerFakeFactories(t)
	var built []*removalFake
	RegisterFactory("discord", func(_ *config.Config, b *bus.MessageBus) (Channel, error) {
		ch := &removalFake{lifecycleFake: newLifecycleFake("discord", b)}
		built = append(built, ch)
		return ch, nil
	})
	m, _ := startedManager(t, discordAndSlack(), nil)

	next := discordAndSlack()
	next.Channels.Discord.Token = "d2"
	m.Reload(context.Background(), next, nil)
	if built[0].removed.Load() {
		t.Fatal("a rebuilt channel was told it was removed")
	}

	next.Channels.Discord.Enabled = false
	m.Reload(context.Background(), next, nil)
	if !built[1].removed.Load() || built[1].stops.Load() != 1 {
		t.Fatal("a channel turned off was not stopped and told it was removed")
	}
}

// The hold for a starting channel takes maxHeldPerChannel messages; more are
// dropped as not running.
func TestHold_Bounded(t *testing.T) {
	m := newTestManager()
	m.RegisterChannel("slow", newLifecycleFake("slow", nil))
	m.held["slow"] = &heldOutbound{}
	var dropped []error
	for i := range maxHeldPerChannel + 2 {
		routeText(m, bus.OutboundMessage{
			Channel: "slow", ChatID: "c", Content: "m" + strconv.Itoa(i),
			OnDelivery: func(err error) { dropped = append(dropped, err) },
		})
	}
	if n := len(heldTexts(m, "slow")); n != maxHeldPerChannel {
		t.Fatalf("held %d, want %d", n, maxHeldPerChannel)
	}
	if len(dropped) != 2 || !errors.Is(dropped[0], ErrNotRunning) {
		t.Fatalf("overflow reported %v, want two ErrNotRunning", dropped)
	}
}

// registerFakeSecMsg registers a SecMsg factory building lifecycle fakes and
// a discovery function that returns *accounts, or fails when it is nil.
func registerFakeSecMsg(t *testing.T, accounts *[]string) {
	t.Helper()
	secmsgFactoryMu.Lock()
	prev := secmsgFactory
	secmsgFactoryMu.Unlock()
	RegisterSecMsgFactory(func(d config.SecMsgConfig, a config.SecMsgAccountConfig, b *bus.MessageBus) (Channel, error) {
		return newLifecycleFake(a.ChannelName(d), b), nil
	})
	t.Cleanup(func() {
		secmsgFactoryMu.Lock()
		secmsgFactory = prev
		secmsgFactoryMu.Unlock()
	})
	withDiscovery(t, func(context.Context, string) ([]string, error) {
		if *accounts == nil {
			return nil, errors.New("connection refused")
		}
		return *accounts, nil
	})
}

// When SecMsg discovery fails on a reload, the daemon's running accounts keep
// running; when the daemon entry itself changed, they are stopped.
func TestReload_SecMsgDiscoveryFailureKeepsRunningAccounts(t *testing.T) {
	accounts := []string{"alice"}
	registerFakeSecMsg(t, &accounts)
	cfg := &config.Config{}
	cfg.Channels.SecMsg = []config.SecMsgConfig{{Name: "signal", Enabled: true, Address: "127.0.0.1:9600", AllowFrom: config.FlexibleStringSlice{"*"}}}
	m, _ := startedManager(t, cfg, nil)
	running, _ := m.GetChannel("signal-alice")

	accounts = nil // the daemon is down
	if res := m.Reload(context.Background(), cfg, nil); !slices.Equal(res.Kept, []string{"signal-alice"}) {
		t.Fatalf("discovery failure: %+v, want signal-alice kept", res)
	}
	if ch, _ := m.GetChannel("signal-alice"); ch != running || !running.IsRunning() {
		t.Fatal("running account replaced or stopped by a discovery failure")
	}

	moved := &config.Config{}
	moved.Channels.SecMsg = []config.SecMsgConfig{{Name: "signal", Enabled: true, Address: "127.0.0.1:9700", AllowFrom: config.FlexibleStringSlice{"*"}}}
	if res := m.Reload(context.Background(), moved, nil); !slices.Equal(res.Stopped, []string{"signal-alice"}) {
		t.Fatalf("daemon changed while discovery fails: %+v, want signal-alice stopped", res)
	}
}
