// ClawEh
// License: MIT

package faulttest

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/internal/test/stubprovider"
	"github.com/PivotLLM/ClawEh/internal/testalerts"
)

// 7. Stop cancels a turn blocked on a provider that never answers: the turn
// ends at once instead of holding up the shutdown, nothing is sent to the
// user, no fallback or alert fires, the turn stays pending for replay on the
// next start, and Close finishes well inside its budget.
func TestShutdownMidTurn_CancelsTurnAndKeepsItPending(t *testing.T) {
	rec := testalerts.Install(t)
	primary, fallback := stubprovider.New(t), stubprovider.New(t)
	primary.Script(stubprovider.Reply("warm"))
	primary.SetDefault(stubprovider.Hang(0))
	cfg := newConfig(t, primary, fallback)
	l := startLoop(t, cfg, bus.NewMessageBus())
	l.al.SetAlerter(rec)

	if reply, _ := l.turn("warm up"); reply != "warm" {
		t.Fatalf("warm-up reply = %q", reply)
	}
	base := settledGoroutines()

	l.send("hang")
	waitFor(t, turnWait, "the hanging model request", func() bool { return primary.Hanging() == 1 })

	var closeTook time.Duration
	l.once.Do(func() {
		l.al.Stop()
		waitFor(t, time.Second, "the model request to be abandoned", func() bool { return primary.Hanging() == 0 })
		waitFor(t, time.Second, "the cancelled turn to end", func() bool {
			return runtime.NumGoroutine() <= base+2
		})
		l.cancel()
		<-l.done
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		start := time.Now()
		l.al.Close(ctx)
		closeTook = time.Since(start)
	})
	if closeTook > 2*time.Second {
		t.Errorf("Close took %v after the turns were cancelled", closeTook)
	}

	if out := drain(l.bus, quiet); len(out) != 0 {
		t.Errorf("an interrupted turn must send nothing; got %+v", out)
	}
	if fallback.Count() != 0 {
		t.Errorf("shutdown failed over to the fallback model (%d requests)", fallback.Count())
	}
	if got := rec.Alerts(); len(got) != 0 {
		t.Errorf("shutdown raised alerts: %+v", got)
	}
	if pending := pendingTurns(t, cfg); len(pending) != 1 {
		t.Errorf("pending_turns = %+v, want the interrupted turn kept for replay", pending)
	}
}
