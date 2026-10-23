// ClawEh
// License: MIT

package channels

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
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
	// ignoreCtx makes a gated Start wait for the gate alone, like a library
	// call that does not honour its context.
	ignoreCtx bool
	starts    atomic.Int32
	stops     atomic.Int32
	sent      chan bus.OutboundMessage
}

func newLifecycleFake(name string, b *bus.MessageBus) *lifecycleFake {
	return &lifecycleFake{
		BaseChannel: NewBaseChannel(name, nil, b, nil),
		entered:     make(chan struct{}, 16),
		stopped:     make(chan struct{}, 16),
		sent:        make(chan bus.OutboundMessage, 16),
	}
}

func (f *lifecycleFake) Start(ctx context.Context) error {
	n := f.starts.Add(1)
	f.entered <- struct{}{}
	if f.gate != nil {
		if f.ignoreCtx {
			<-f.gate
		} else {
			select {
			case <-f.gate:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
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

// eventLog records events from several goroutines in the order they happen.
type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *eventLog) add(e string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, e)
}

func (l *eventLog) get() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.events)
}

// loggedStart is a lifecycleFake that logs when its Start returns.
type loggedStart struct {
	*lifecycleFake
	log *eventLog
}

func (f loggedStart) Start(ctx context.Context) error {
	err := f.lifecycleFake.Start(ctx)
	f.log.add(f.Name() + " started")
	return err
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
	log := &eventLog{}
	slow := newLifecycleFake("slow", mb)
	slow.gate = make(chan struct{})
	fast := newLifecycleFake("fast", mb)
	m.RegisterChannel("slow", loggedStart{slow, log})
	m.RegisterChannel("fast", fast)

	started := make(chan struct{})
	go func() {
		if err := m.StartAll(context.Background()); err != nil {
			t.Errorf("StartAll: %v", err)
		}
		log.add("StartAll returned")
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

	log.add("gate opened")
	close(slow.gate)
	await(t, started, "StartAll")
	// StartAll returned only after the slow channel had started, which it
	// could do only once the gate was open.
	if got, want := log.get(), []string{"gate opened", "slow started", "StartAll returned"}; !slices.Equal(got, want) {
		t.Fatalf("events = %q, want %q", got, want)
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

	// Let the retry begin and hold it inside Start, past its cancellation.
	old.gate, old.ignoreCtx = gate, true
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

// StopAll while a channel's first Start is blocked cancels that start rather
// than waiting for it: StopAll and StartAll return, the channel gets no
// worker, and what was held for it is dropped as not running.
func TestStopAll_CancelsBlockedStart(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()
	m := newTestManager()
	m.bus = mb
	slow := newLifecycleFake("slow", mb)
	slow.gate = make(chan struct{}) // never opened
	m.RegisterChannel("slow", slow)

	started := make(chan struct{})
	go func() {
		if err := m.StartAll(context.Background()); err != nil {
			t.Errorf("StartAll: %v", err)
		}
		close(started)
	}()
	await(t, slow.entered, "the slow channel's Start")
	var dropped []error
	routeText(m, bus.OutboundMessage{
		Channel: "slow", ChatID: "c", Content: "held",
		OnDelivery: func(err error) { dropped = append(dropped, err) },
	})

	stopped := make(chan struct{})
	go func() {
		if err := m.StopAll(context.Background()); err != nil {
			t.Errorf("StopAll: %v", err)
		}
		close(stopped)
	}()
	await(t, stopped, "StopAll")
	await(t, started, "StartAll")

	m.mu.RLock()
	w := m.workers["slow"]
	m.mu.RUnlock()
	if w != nil || slow.IsRunning() || slow.stops.Load() != 1 {
		t.Fatalf("worker=%v running=%v stops=%d after StopAll", w != nil, slow.IsRunning(), slow.stops.Load())
	}
	if len(dropped) != 1 || !errors.Is(dropped[0], ErrNotRunning) {
		t.Fatalf("held message reported %v, want ErrNotRunning", dropped)
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

// StopAll during a Reload whose rebuilt channel is still starting cancels
// that start instead of waiting for it: both return, the rebuilt channel gets
// no worker, what was held for it is dropped as not running, and a later
// Reload of the stopped manager builds without starting anything.
func TestReload_StopAllCancelsRebuildingStart(t *testing.T) {
	ff := registerFakeFactories(t)
	mb := bus.NewMessageBus()
	t.Cleanup(mb.Close)
	m, err := NewManager(discordAndSlack(), mb, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.StartAll(context.Background()); err != nil {
		t.Fatal(err)
	}

	builtCh := make(chan *lifecycleFake, 1)
	ff.onBuild = func(name string, n int, ch *lifecycleFake) error {
		if name == "discord" && n == 2 {
			ch.gate = make(chan struct{}) // never opened
			builtCh <- ch
		}
		return nil
	}
	next := discordAndSlack()
	next.Channels.Discord.Token = "d2"
	reloaded := make(chan ReloadResult, 1)
	go func() { reloaded <- m.Reload(context.Background(), next, nil) }()
	var rebuilt *lifecycleFake
	select {
	case rebuilt = <-builtCh:
	case <-time.After(failAfter):
		t.Fatal("discord was not rebuilt")
	}
	await(t, rebuilt.entered, "the rebuilt channel's Start")
	dropped := make(chan error, 1)
	routeText(m, bus.OutboundMessage{
		Channel: "discord", ChatID: "c", Content: "held",
		OnDelivery: func(err error) { dropped <- err },
	})

	stopped := make(chan struct{})
	go func() {
		if err := m.StopAll(context.Background()); err != nil {
			t.Errorf("StopAll: %v", err)
		}
		close(stopped)
	}()
	await(t, stopped, "StopAll")
	var res ReloadResult
	select {
	case res = <-reloaded:
	case <-time.After(failAfter):
		t.Fatal("Reload did not return")
	}
	if !slices.Equal(res.Restarted, []string{"discord"}) {
		t.Fatalf("reload = %+v, want discord restarted", res)
	}
	m.mu.RLock()
	w := m.workers["discord"]
	_, held := m.held["discord"]
	m.mu.RUnlock()
	if w != nil || held || rebuilt.IsRunning() {
		t.Fatalf("worker=%v hold=%v running=%v after StopAll", w != nil, held, rebuilt.IsRunning())
	}
	select {
	case err := <-dropped:
		if !errors.Is(err, ErrNotRunning) {
			t.Fatalf("held message reported %v, want ErrNotRunning", err)
		}
	default:
		t.Fatal("held message was not reported")
	}

	// The manager is stopped: a reload rebuilds the changed channel but does
	// not start it.
	ff.onBuild = nil
	next.Channels.Discord.Token = "d3"
	if res := m.Reload(context.Background(), next, nil); !slices.Equal(res.Restarted, []string{"discord"}) {
		t.Fatalf("reload after StopAll = %+v, want discord restarted", res)
	}
	third := ff.last("discord")
	m.mu.RLock()
	ch := m.channels["discord"]
	w = m.workers["discord"]
	m.mu.RUnlock()
	if ch != third || w != nil || third.starts.Load() != 0 {
		t.Fatalf("after reload of a stopped manager: registered=%v worker=%v starts=%d", ch == third, w != nil, third.starts.Load())
	}
}

// Two Reloads never overlap: the second builds nothing until the first,
// whose rebuilt channel is still starting, has returned; the channel then
// ends up as the second built it.
func TestReload_SerializedWithReload(t *testing.T) {
	ff := registerFakeFactories(t)
	m, _ := startedManager(t, discordAndSlack(), nil)

	gate := make(chan struct{})
	builtCh := make(chan *lifecycleFake, 1)
	var overlapped atomic.Bool
	ff.onBuild = func(name string, n int, ch *lifecycleFake) error {
		if name != "discord" {
			return nil
		}
		switch n {
		case 2:
			ch.gate = gate
			builtCh <- ch
		case 3:
			select {
			case <-gate:
			default:
				overlapped.Store(true) // built while the first reload was starting
			}
		}
		return nil
	}

	first := discordAndSlack()
	first.Channels.Discord.Token = "d2"
	second := discordAndSlack()
	second.Channels.Discord.Token = "d3"
	r1 := make(chan ReloadResult, 1)
	go func() { r1 <- m.Reload(context.Background(), first, nil) }()
	var rebuilt *lifecycleFake
	select {
	case rebuilt = <-builtCh:
	case <-time.After(failAfter):
		t.Fatal("discord was not rebuilt")
	}
	await(t, rebuilt.entered, "the first rebuild's Start")
	r2 := make(chan ReloadResult, 1)
	go func() { r2 <- m.Reload(context.Background(), second, nil) }()

	close(gate)
	for i, r := range []chan ReloadResult{r1, r2} {
		select {
		case res := <-r:
			if !slices.Equal(res.Restarted, []string{"discord"}) {
				t.Fatalf("reload %d = %+v, want discord restarted", i+1, res)
			}
		case <-time.After(failAfter):
			t.Fatalf("reload %d did not return", i+1)
		}
	}
	if overlapped.Load() {
		t.Fatal("the second reload built while the first was still starting its channel")
	}
	final := ff.last("discord")
	m.mu.RLock()
	w := m.workers["discord"]
	m.mu.RUnlock()
	if final == rebuilt || w == nil || w.ch != final || rebuilt.stops.Load() != 1 {
		t.Fatal("the channel is not the second reload's, or the first's was not stopped")
	}
}

// mediaFake is a lifecycleFake that also sends media.
type mediaFake struct {
	*lifecycleFake
	media chan bus.OutboundMediaMessage
}

func newMediaFake(name string, b *bus.MessageBus) mediaFake {
	return mediaFake{lifecycleFake: newLifecycleFake(name, b), media: make(chan bus.OutboundMediaMessage, 16)}
}

func (f mediaFake) SendMedia(_ context.Context, msg bus.OutboundMediaMessage) error {
	f.media <- msg
	return nil
}

// routeMedia routes msg as the outbound media dispatcher does, synchronously.
func routeMedia(m *Manager, msg bus.OutboundMediaMessage) {
	routeOutbound(context.Background(), m, msg.Channel, msg,
		func(w *channelWorker) chan bus.OutboundMediaMessage { return w.mediaQueue },
		func(w *channelWorker) <-chan struct{} { return w.mediaDone },
		holdMedia, func(bus.OutboundMediaMessage, error) {}, "unknown", "no worker")
}

// heldMedia is how many media messages are held for channel name.
func heldMedia(m *Manager, name string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	h := m.held[name]
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.media)
}

// startGated starts m in the background with ch's Start held at its gate,
// and returns once it is; the returned channel closes when StartAll returns.
func startGated(t *testing.T, m *Manager, ch *lifecycleFake) <-chan struct{} {
	t.Helper()
	started := make(chan struct{})
	go func() {
		if err := m.StartAll(context.Background()); err != nil {
			t.Errorf("StartAll: %v", err)
		}
		close(started)
	}()
	await(t, ch.entered, "the channel's Start")
	return started
}

// Media sent to a channel while it starts is held and reaches its SendMedia
// once it has started.
func TestHold_MediaReachesChannelAfterStart(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()
	m := newTestManager()
	m.bus = mb
	slow := newMediaFake("slow", mb)
	slow.gate = make(chan struct{})
	m.RegisterChannel("slow", slow)
	started := startGated(t, m, slow.lifecycleFake)
	defer func() {
		if err := m.StopAll(context.Background()); err != nil {
			t.Error(err)
		}
	}()

	routeMedia(m, bus.OutboundMediaMessage{Channel: "slow", ChatID: "c", Parts: []bus.MediaPart{{Ref: "media://1"}}})
	if n := heldMedia(m, "slow"); n != 1 {
		t.Fatalf("held media = %d, want 1", n)
	}
	close(slow.gate)
	await(t, started, "StartAll")
	select {
	case got := <-slow.media:
		if len(got.Parts) != 1 || got.Parts[0].Ref != "media://1" {
			t.Fatalf("SendMedia got %+v", got)
		}
	case <-time.After(failAfter):
		t.Fatal("held media never reached SendMedia")
	}
}

// Media held for a channel whose start StopAll cancels is dropped: the hold
// is gone and nothing reaches the channel.
func TestHold_MediaDroppedOnStopAll(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()
	m := newTestManager()
	m.bus = mb
	slow := newMediaFake("slow", mb)
	slow.gate = make(chan struct{}) // never opened
	m.RegisterChannel("slow", slow)
	started := startGated(t, m, slow.lifecycleFake)

	routeMedia(m, bus.OutboundMediaMessage{Channel: "slow", ChatID: "c"})
	if err := m.StopAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	await(t, started, "StartAll")
	m.mu.RLock()
	_, held := m.held["slow"]
	w := m.workers["slow"]
	m.mu.RUnlock()
	if held || w != nil {
		t.Fatalf("hold=%v worker=%v after StopAll", held, w != nil)
	}
	select {
	case msg := <-slow.media:
		t.Fatalf("dropped media was sent: %+v", msg)
	default:
	}
}

// abandonStart ends the hold: text is reported as not running, media is
// dropped with it.
func TestAbandonStart_DropsHeldTextAndMedia(t *testing.T) {
	m := newTestManager()
	var reported []error
	m.held["slow"] = &heldOutbound{
		text:  []bus.OutboundMessage{{Channel: "slow", Content: "hi", OnDelivery: func(err error) { reported = append(reported, err) }}},
		media: []bus.OutboundMediaMessage{{Channel: "slow", ChatID: "c"}},
	}
	m.abandonStart("slow")
	if _, held := m.held["slow"]; held {
		t.Fatal("hold kept after abandonStart")
	}
	if len(reported) != 1 || !errors.Is(reported[0], ErrNotRunning) {
		t.Fatalf("held text reported %v, want ErrNotRunning", reported)
	}
}

// A rebuild whose build fails drops held media with the hold.
func TestReload_FailedRebuildDropsHeldMedia(t *testing.T) {
	ff := registerFakeFactories(t)
	m, _ := startedManager(t, discordAndSlack(), nil)
	var heldDuringBuild int
	ff.onBuild = func(name string, n int, _ *lifecycleFake) error {
		if name != "discord" || n != 2 {
			return nil
		}
		routeMedia(m, bus.OutboundMediaMessage{Channel: "discord", ChatID: "c"})
		heldDuringBuild = heldMedia(m, "discord")
		return errors.New("bad token")
	}
	next := discordAndSlack()
	next.Channels.Discord.Token = "d2"
	m.Reload(context.Background(), next, nil)

	if heldDuringBuild != 1 {
		t.Fatalf("media held during the rebuild = %d, want 1", heldDuringBuild)
	}
	m.mu.RLock()
	_, held := m.held["discord"]
	_, ch := m.channels["discord"]
	m.mu.RUnlock()
	if held || ch {
		t.Fatalf("failed rebuild left hold=%v channel=%v", held, ch)
	}
}

// Held media counts toward maxHeldPerChannel together with text.
func TestHold_MediaCountsTowardBound(t *testing.T) {
	m := newTestManager()
	m.RegisterChannel("slow", newLifecycleFake("slow", nil))
	m.held["slow"] = &heldOutbound{}
	for i := range maxHeldPerChannel - 1 {
		routeText(m, bus.OutboundMessage{Channel: "slow", ChatID: "c", Content: "m" + strconv.Itoa(i)})
	}
	routeMedia(m, bus.OutboundMediaMessage{Channel: "slow", ChatID: "c"})
	if n := heldMedia(m, "slow"); n != 1 {
		t.Fatalf("held media = %d, want 1", n)
	}

	var dropped []error
	routeText(m, bus.OutboundMessage{
		Channel: "slow", ChatID: "c", Content: "one too many",
		OnDelivery: func(err error) { dropped = append(dropped, err) },
	})
	routeMedia(m, bus.OutboundMediaMessage{Channel: "slow", ChatID: "c"})
	if len(dropped) != 1 || !errors.Is(dropped[0], ErrNotRunning) {
		t.Fatalf("text past the bound reported %v, want ErrNotRunning", dropped)
	}
	if n, texts := heldMedia(m, "slow"), len(heldTexts(m, "slow")); n != 1 || texts != maxHeldPerChannel-1 {
		t.Fatalf("held %d texts and %d media, want %d and 1", texts, n, maxHeldPerChannel-1)
	}
}

// What was held while a channel started is sent before a message queued
// while that backlog is being sent: "three" is routed from the delivery of
// "one", when the worker is running and "two" is still in its backlog.
func TestHold_BacklogBeforeLaterMessages(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()
	m := newTestManager()
	m.bus = mb
	slow := newLifecycleFake("slow", mb)
	slow.gate = make(chan struct{})
	m.RegisterChannel("slow", slow)
	started := startGated(t, m, slow)
	defer func() {
		if err := m.StopAll(context.Background()); err != nil {
			t.Error(err)
		}
	}()

	queued := make(chan bool, 1) // whether "three" went to the worker, not the hold
	routeText(m, bus.OutboundMessage{
		Channel: "slow", ChatID: "c", Content: "one",
		OnDelivery: func(error) {
			m.mu.RLock()
			_, held := m.held["slow"]
			m.mu.RUnlock()
			routeText(m, bus.OutboundMessage{Channel: "slow", ChatID: "c", Content: "three"})
			queued <- !held
		},
	})
	routeText(m, bus.OutboundMessage{Channel: "slow", ChatID: "c", Content: "two"})
	close(slow.gate)
	await(t, started, "StartAll")
	for _, want := range []string{"one", "two", "three"} {
		if got := slow.receive(t); got.Content != want {
			t.Fatalf("channel got %q, want %q", got.Content, want)
		}
	}
	if !<-queued {
		t.Fatal(`"three" was held, not queued behind the backlog`)
	}
}

// registerFakeTelegram registers a Telegram bot factory building lifecycle
// fakes and returns them by channel name, the latest last.
func registerFakeTelegram(t *testing.T) func(name string) []*lifecycleFake {
	t.Helper()
	telegramBotFactoryMu.Lock()
	prev := telegramBotFactory
	telegramBotFactoryMu.Unlock()
	var mu sync.Mutex
	built := map[string][]*lifecycleFake{}
	RegisterTelegramBotFactory(func(bot config.TelegramBotConfig, b *bus.MessageBus) (Channel, error) {
		ch := newLifecycleFake(bot.ChannelName(), b)
		mu.Lock()
		built[bot.ChannelName()] = append(built[bot.ChannelName()], ch)
		mu.Unlock()
		return ch, nil
	})
	t.Cleanup(func() {
		telegramBotFactoryMu.Lock()
		telegramBotFactory = prev
		telegramBotFactoryMu.Unlock()
	})
	return func(name string) []*lifecycleFake {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(built[name])
	}
}

func twoTelegramBots() *config.Config {
	cfg := &config.Config{}
	cfg.Channels.Telegram = []config.TelegramBotConfig{
		{ID: "alice", Enabled: true, Token: "1:a", AllowFrom: config.FlexibleStringSlice{"*"}},
		{ID: "bob", Enabled: true, Token: "2:b", AllowFrom: config.FlexibleStringSlice{"*"}},
	}
	return cfg
}

// Each Telegram bot's input is its own entry: changing one bot rebuilds only
// that bot, and removing one stops only it.
func TestReload_TelegramBotsAreIndependent(t *testing.T) {
	built := registerFakeTelegram(t)
	m, _ := startedManager(t, twoTelegramBots(), nil)
	bob := built("telegram-bob")[0]

	changed := twoTelegramBots()
	changed.Channels.Telegram[0].Token = "1:a2"
	res := m.Reload(context.Background(), changed, nil)
	if !slices.Equal(res.Restarted, []string{"telegram-alice"}) || !slices.Equal(res.Kept, []string{"telegram-bob"}) {
		t.Fatalf("changing Alice's bot: %+v, want only telegram-alice restarted", res)
	}
	if n := len(built("telegram-alice")); n != 2 {
		t.Fatalf("Alice's bot built %d times, want 2", n)
	}
	if len(built("telegram-bob")) != 1 || bob.stops.Load() != 0 || bob.starts.Load() != 1 {
		t.Fatal("Bob's bot was touched by a change to Alice's")
	}

	removed := twoTelegramBots()
	removed.Channels.Telegram = removed.Channels.Telegram[1:]
	res = m.Reload(context.Background(), removed, nil)
	if !slices.Equal(res.Stopped, []string{"telegram-alice"}) || !slices.Equal(res.Kept, []string{"telegram-bob"}) {
		t.Fatalf("removing Alice's bot: %+v, want only telegram-alice stopped", res)
	}
	alice := built("telegram-alice")[1]
	if alice.stops.Load() != 1 || bob.stops.Load() != 0 || !bob.IsRunning() {
		t.Fatal("removing Alice's bot did not stop only hers")
	}
	if _, ok := m.GetChannel("telegram-alice"); ok {
		t.Fatal("removed bot still registered")
	}
}

// When discovery finds a different set of accounts, a new one is started and
// one no longer found is stopped; one still found is kept.
func TestReload_SecMsgDiscoveryChangesAccounts(t *testing.T) {
	accounts := []string{"alice", "bob"}
	registerFakeSecMsg(t, &accounts)
	cfg := &config.Config{}
	cfg.Channels.SecMsg = []config.SecMsgConfig{{Name: "signal", Enabled: true, Address: "127.0.0.1:9600", AllowFrom: config.FlexibleStringSlice{"*"}}}
	m, _ := startedManager(t, cfg, nil)
	alice, _ := m.GetChannel("signal-alice")
	bob, _ := m.GetChannel("signal-bob")

	accounts = []string{"alice", "carol"}
	res := m.Reload(context.Background(), cfg, nil)
	if !slices.Equal(res.Kept, []string{"signal-alice"}) || !slices.Equal(res.Started, []string{"signal-carol"}) ||
		!slices.Equal(res.Stopped, []string{"signal-bob"}) || len(res.Restarted) != 0 {
		t.Fatalf("reload = %+v, want alice kept, carol started, bob stopped", res)
	}
	if ch, _ := m.GetChannel("signal-alice"); ch != alice || !alice.IsRunning() {
		t.Fatal("an account still found was replaced or stopped")
	}
	if bob.IsRunning() {
		t.Fatal("an account no longer found is still running")
	}
	if carol, ok := m.GetChannel("signal-carol"); !ok || !carol.IsRunning() {
		t.Fatal("a newly found account was not started")
	}
}

// When discovery fails and none of the daemon's accounts is running, nothing
// is started for it.
func TestReload_SecMsgDiscoveryFailureWithNothingRunning(t *testing.T) {
	var accounts []string // discovery fails throughout
	registerFakeSecMsg(t, &accounts)
	ff := registerFakeFactories(t)
	cfg := discordAndSlack()
	m, _ := startedManager(t, cfg, nil)

	withSecMsg := discordAndSlack()
	withSecMsg.Channels.SecMsg = []config.SecMsgConfig{{Name: "signal", Enabled: true, Address: "127.0.0.1:9600", AllowFrom: config.FlexibleStringSlice{"*"}}}
	res := m.Reload(context.Background(), withSecMsg, nil)
	if len(res.Started)+len(res.Restarted)+len(res.Stopped) != 0 || !slices.Equal(res.Kept, []string{"discord", "slack"}) {
		t.Fatalf("reload = %+v, want nothing started for the unreachable daemon", res)
	}
	for _, name := range m.GetEnabledChannels() {
		if strings.HasPrefix(name, "signal") {
			t.Fatalf("channel %s registered although discovery failed", name)
		}
	}
	if ff.builds("discord") != 1 || ff.builds("slack") != 1 {
		t.Fatal("other channels were rebuilt")
	}
}
