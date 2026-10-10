// ClawEh
// License: MIT

package agent

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/commands"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
)

// closingProvider is a stateful provider that records its Close.
type closingProvider struct {
	mockProvider
	closed atomic.Bool
}

func (p *closingProvider) Close() { p.closed.Store(true) }

// closeInBackground runs closeReplacedProvider and returns a channel closed
// when it returns.
func closeInBackground(al *AgentLoop, ctx context.Context, p providers.LLMProvider) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		al.closeReplacedProvider(ctx, p)
	}()
	return done
}

func waitClosed(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatalf("closeReplacedProvider did not return %s", what)
	}
}

// The replaced provider is closed as soon as the calls in flight on it end,
// whatever runs on other providers, and at once when none is in flight.
func TestCloseReplacedProvider_WaitsForItsOwnCalls(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))
	al := &AgentLoop{}
	fc := fakeClock(al)
	old, other := &closingProvider{}, &mockProvider{}

	waitClosed(t, closeInBackground(al, context.Background(), old), "with nothing in flight")
	if !old.closed.Load() {
		t.Fatal("not closed")
	}

	old.closed.Store(false)
	endOld := al.modelCalls.begin(old)
	endOther := al.modelCalls.begin(other)
	defer endOther() // a call on the new provider is never waited for
	done := closeInBackground(al, context.Background(), old)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := fc.BlockUntil(ctx, 1); err != nil {
		t.Fatalf("the close never started waiting: %v", err)
	}
	if old.closed.Load() {
		t.Fatal("closed while a call was in flight on it")
	}
	endOld()
	waitClosed(t, done, "once its call ended")
	if !old.closed.Load() {
		t.Fatal("not closed after its call ended")
	}
	if n := fc.Waiters(); n != 0 {
		t.Fatalf("waiters = %d after the close: its timer was left behind", n)
	}
}

// A call that outlasts the wait does not keep anything running: the close
// is forced after providerCloseWait on the loop's clock, or when ctx ends.
func TestCloseReplacedProvider_ForcedAfterTheWait(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))
	al := &AgentLoop{}
	fc := fakeClock(al)
	old := &closingProvider{}
	end := al.modelCalls.begin(old)
	defer end()

	done := closeInBackground(al, context.Background(), old)
	advanceWhenWaiting(t, fc, 1, providerCloseWait)
	waitClosed(t, done, "after the wait")
	if !old.closed.Load() || fc.Waiters() != 0 {
		t.Fatalf("closed = %v, waiters = %d", old.closed.Load(), fc.Waiters())
	}

	old.closed.Store(false)
	ctx, cancel := context.WithCancel(context.Background())
	done = closeInBackground(al, ctx, old)
	cancel()
	waitClosed(t, done, "when ctx ended")
	if !old.closed.Load() {
		t.Fatal("not closed when ctx ended")
	}
}

// Calls begin and end on the replaced provider while it is being closed
// (race detector).
func TestCloseReplacedProvider_CallsDuringTheWait(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))
	al := &AgentLoop{}
	old := &closingProvider{}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 200 {
				al.modelCalls.begin(old)()
			}
		})
	}
	for range 20 {
		al.closeReplacedProvider(context.Background(), old)
	}
	wg.Wait()
	select {
	case <-al.modelCalls.idle(old):
	default:
		t.Fatal("calls still counted after every one ended")
	}
}

// The cooldown commands read the fallback chain a reload swaps in under the
// loop's lock (race detector).
func TestCooldownHooks_RaceWithReload(t *testing.T) {
	cooldown := providers.NewCooldownTracker()
	al := &AgentLoop{fallback: providers.NewFallbackChain(cooldown)}
	rt := &commands.Runtime{}
	al.addCooldownHooks(rt)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			al.mu.Lock()
			al.fallback = providers.NewFallbackChain(cooldown)
			al.mu.Unlock()
		}
	})
	for range 200 {
		rt.ListCooldowns()
		rt.ClearCooldown("p", "m")
		rt.ResetCooldown()
	}
	close(stop)
	wg.Wait()
}

// A turn reads the configuration a reload swaps in under the loop's lock
// when it records a response and takes a direct answer (race detector).
func TestTurnConfigReads_RaceWithReload(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))
	al := &AgentLoop{cfg: &config.Config{}, dumpsDir: t.TempDir()}
	agent := &AgentInstance{ID: "alice"}
	al.exposeReasoningCache = map[string]bool{activeModelCacheKey(agent.ID, "s"): false}
	turn := &llmTurn{al: al, agent: agent, opts: processOptions{SessionKey: "s"}}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			al.mu.Lock()
			al.cfg = &config.Config{}
			al.mu.Unlock()
		}
	})
	for range 200 {
		resp := &providers.LLMResponse{Content: "hi"}
		turn.recordResponse(context.Background(), resp)
		turn.directAnswer(resp)
	}
	close(stop)
	wg.Wait()
}

// The MCP setup and the transcription echo read the configuration a reload
// swaps in under the loop's lock (race detector).
func TestMCPAndTranscriptionConfigReads_RaceWithReload(t *testing.T) {
	t.Cleanup(logger.RedirectForTest(&safeBufLoop{}))
	al := &AgentLoop{cfg: &config.Config{}}
	stop, started := make(chan struct{}), make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		close(started)
		for {
			select {
			case <-stop:
				return
			default:
			}
			al.mu.Lock()
			al.cfg = &config.Config{}
			al.mu.Unlock()
		}
	})
	<-started
	for range 2000 {
		if mgr := al.connectAndRegisterMCP(context.Background()); mgr != nil {
			t.Fatal("MCP connected with it disabled")
		}
		al.sendTranscriptionFeedback(context.Background(), "telegram", "1", "m", []string{"hi"})
	}
	close(stop)
	wg.Wait()
}
