// ClawEh
// License: MIT

package faulttest

import (
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/internal/test/stubprovider"
	"github.com/PivotLLM/ClawEh/internal/testalerts"
)

const (
	noCooldowns    = "  none\n"
	primaryParked  = primaryProvider + "/" + primaryModel + " — "
	tryingFallback = ".\nTrying fallback…"
)

// 1. A 429 with a short Retry-After is retried on the same model after the
// hint; when the retry succeeds the user gets one reply and nothing is parked.
func TestRateLimit_RetryAfterThenSuccess(t *testing.T) {
	primary, fallback := stubprovider.New(t), stubprovider.New(t)
	primary.Script(stubprovider.RateLimited(time.Second), stubprovider.Reply("primary ok"))
	fallback.SetDefault(stubprovider.Reply("fallback ok"))
	l := startLoop(t, newConfig(t, primary, fallback), bus.NewMessageBus())

	start := time.Now()
	reply, notices := l.turn("hello")
	if reply != "primary ok" {
		t.Errorf("reply = %q, want the primary's retry to succeed", reply)
	}
	if len(notices) != 0 {
		t.Errorf("notices = %q, want none (same-model retry is silent)", notices)
	}
	if elapsed := time.Since(start); elapsed < time.Second {
		t.Errorf("turn took %v, want >= 1s: Retry-After was not honoured", elapsed)
	}
	if primary.Count() != 2 {
		t.Errorf("primary saw %d requests, want 2 (429 then retry)", primary.Count())
	}
	if fallback.Count() != 0 {
		t.Errorf("fallback saw %d requests, want 0", fallback.Count())
	}
	if cd := l.cooldowns(); !strings.Contains(cd, noCooldowns) {
		t.Errorf("cooldowns after a successful retry:\n%s\nwant none", cd)
	}
}

// 1b. A 429 whose retry also 429s (now with a long Retry-After) fails over:
// one heads-up naming the status, one reply from the fallback, and the
// primary parked for the server's Retry-After.
func TestRateLimit_PersistentFailsOverAndParks(t *testing.T) {
	primary, fallback := stubprovider.New(t), stubprovider.New(t)
	primary.Script(stubprovider.RateLimited(time.Second), stubprovider.RateLimited(30*time.Second))
	fallback.SetDefault(stubprovider.Reply("fallback ok"))
	l := startLoop(t, newConfig(t, primary, fallback), bus.NewMessageBus())

	reply, notices := l.turn("hello")
	if reply != "fallback ok" {
		t.Errorf("reply = %q, want the fallback's", reply)
	}
	want := "⚠️ primary error HTTP 429 (rate limited)" + tryingFallback
	if len(notices) != 1 || notices[0] != want {
		t.Errorf("notices = %q, want [%q]", notices, want)
	}
	if primary.Count() != 2 || fallback.Count() != 1 {
		t.Errorf("requests primary=%d fallback=%d, want 2 and 1", primary.Count(), fallback.Count())
	}
	cd := l.cooldowns()
	if !strings.Contains(cd, primaryParked+"rate_limit") {
		t.Errorf("cooldowns:\n%s\nwant %s parked for rate_limit", cd, primaryProvider+"/"+primaryModel)
	}

	// The next turn skips the parked primary outright, says why, and still
	// answers from the fallback.
	reply, notices = l.turn("again")
	if reply != "fallback ok" {
		t.Errorf("second reply = %q, want the fallback's", reply)
	}
	if len(notices) != 1 || !strings.HasPrefix(notices[0], "⚠️ primary unavailable — rate limited (retry in ") ||
		!strings.HasSuffix(notices[0], "). Using fallback.") {
		t.Errorf("second-turn notices = %q, want one skip notice", notices)
	}
	if primary.Count() != 2 {
		t.Errorf("primary saw %d requests after being parked, want still 2", primary.Count())
	}
}

// 2. A 5xx on the primary fails over to the fallback with a notice carrying
// the status code; the reply comes from the fallback.
func TestServerError_FailsOver(t *testing.T) {
	primary, fallback := stubprovider.New(t), stubprovider.New(t)
	primary.Script(stubprovider.ServerError(503))
	fallback.SetDefault(stubprovider.Reply("fallback ok"))
	l := startLoop(t, newConfig(t, primary, fallback), bus.NewMessageBus())

	reply, notices := l.turn("hello")
	if reply != "fallback ok" {
		t.Errorf("reply = %q, want the fallback's", reply)
	}
	want := "⚠️ primary error HTTP 503 (timeout)" + tryingFallback
	if len(notices) != 1 || notices[0] != want {
		t.Errorf("notices = %q, want [%q]", notices, want)
	}
	if cd := l.cooldowns(); !strings.Contains(cd, primaryParked+"timeout") {
		t.Errorf("cooldowns:\n%s\nwant the primary parked after its 503", cd)
	}
}

// 3. A 413 on the only model is a context overflow: the loop tells the user,
// runs ForceCompress, retries the same request, and the retry's reply is the
// one the user gets. 413 never parks the model.
func TestContextOverflow_413CompactsAndRetries(t *testing.T) {
	primary := stubprovider.New(t)
	primary.Script(stubprovider.PayloadTooLarge(), stubprovider.Reply("after compaction"))
	l := startLoop(t, newConfig(t, primary, nil), bus.NewMessageBus())

	reply, notices := l.turn("hello")
	if reply != "after compaction" {
		t.Errorf("reply = %q, want the retried request's", reply)
	}
	if len(notices) != 1 || notices[0] != compressionNotice {
		t.Errorf("notices = %q, want [%q]", notices, compressionNotice)
	}
	if primary.Count() != 2 {
		t.Errorf("primary saw %d requests, want 2 (413 then the retry)", primary.Count())
	}
	if cd := l.cooldowns(); !strings.Contains(cd, noCooldowns) {
		t.Errorf("cooldowns after a 413:\n%s\nwant none (413 must never park a model)", cd)
	}
}

// 3b. OpenAI (and most compatible endpoints) report overflow as HTTP 400 with
// code context_length_exceeded, not 413. It must take the same compaction
// path: notice, ForceCompress, retry, one reply.
func TestContextOverflow_400ContextLengthExceededCompactsAndRetries(t *testing.T) {
	primary := stubprovider.New(t)
	primary.Script(stubprovider.ContextLengthExceeded(), stubprovider.Reply("after compaction"))
	l := startLoop(t, newConfig(t, primary, nil), bus.NewMessageBus())

	reply, notices := l.turn("hello")
	if reply != "after compaction" {
		t.Errorf("reply = %q, want the retried request's", reply)
	}
	if len(notices) != 1 || notices[0] != compressionNotice {
		t.Errorf("notices = %q, want [%q]", notices, compressionNotice)
	}
	if primary.Count() != 2 {
		t.Errorf("primary saw %d requests, want 2 (400 then the retry)", primary.Count())
	}
}

// 4. A provider that never answers is cut off by the turn budget: the user
// gets the time-limit message, nothing else, and the turn leaves no goroutine
// behind.
func TestHang_TurnTimeoutFires(t *testing.T) {
	primary, fallback := stubprovider.New(t), stubprovider.New(t)
	primary.Script(stubprovider.Reply("warm"))
	primary.SetDefault(stubprovider.Hang(0))
	fallback.SetDefault(stubprovider.Hang(0))
	cfg := newConfig(t, primary, fallback)
	cfg.Agents.Defaults.TurnTimeout = 1
	l := startLoop(t, cfg, bus.NewMessageBus())

	// A completed turn first, so the goroutine baseline includes the loop's
	// steady state (idle HTTP connections, context manager).
	if reply, _ := l.turn("warm up"); reply != "warm" {
		t.Fatalf("warm-up reply = %q", reply)
	}
	base := settledGoroutines()

	start := time.Now()
	l.send("hang")
	all := collectUntil(t, l.bus, turnWait, func(ms []bus.OutboundMessage) bool { return len(ms) > 0 })
	elapsed := time.Since(start)
	if len(all) != 1 {
		t.Fatalf("got %d outbound messages, want exactly 1: %+v", len(all), all)
	}
	want := "⚠️ This turn ran past the 1s time limit and was stopped. Some steps may have completed — ask me to continue if needed."
	if all[0].Content != want {
		t.Errorf("message = %q, want %q", all[0].Content, want)
	}
	if elapsed < time.Second || elapsed > 3*time.Second {
		t.Errorf("turn ended after %v, want about the 1s budget", elapsed)
	}

	waitFor(t, 3*time.Second, "goroutines to return to baseline", func() bool {
		return runtime.NumGoroutine() <= base+2
	})
}

// settledGoroutines samples runtime.NumGoroutine until two consecutive
// readings agree.
func settledGoroutines() int {
	prev := runtime.NumGoroutine()
	for range 50 {
		time.Sleep(20 * time.Millisecond)
		n := runtime.NumGoroutine()
		if n == prev {
			return n
		}
		prev = n
	}
	return prev
}

// 5. A restart during a tool call: the next loop on the same data dir replays
// the interrupted message on its original channel and chat after telling the
// user, delivers the reply there, and clears pending_turns.
func TestRestartMidTurn_ReplaysOnOriginalChannel(t *testing.T) {
	primary := stubprovider.New(t)
	tool := newBlockingTool()
	defer close(tool.release)
	cfg := newConfig(t, primary, nil)
	cfg.Agents.List[0].Tools = []string{tool.Name()}

	primary.Script(stubprovider.CallTool(tool.Name(), ""))
	primary.SetDefault(stubprovider.Reply("recovered reply"))

	first := startLoop(t, cfg, bus.NewMessageBus(), tool)
	first.send("please block")
	tool.waitStarted(t, turnWait)

	pending := pendingTurns(t, cfg)
	if len(pending) != 1 {
		t.Fatalf("pending_turns = %+v, want the in-flight turn recorded", pending)
	}
	var sessionKey string
	for k, pt := range pending {
		sessionKey = k
		if pt.Channel != testChannel || pt.ChatID != testChatID || pt.Attempts != 0 {
			t.Errorf("pending_turns[%q] = %+v, want %s/%s with 0 attempts", k, pt, testChannel, testChatID)
		}
	}
	first.crash()

	second := startLoop(t, cfg, bus.NewMessageBus())
	all := collectUntil(t, second.bus, turnWait, func(ms []bus.OutboundMessage) bool {
		return len(ms) >= 2
	})
	if len(all) != 2 {
		t.Fatalf("got %d outbound messages on the new loop, want notice + reply: %+v", len(all), all)
	}
	for i, m := range all {
		if m.Channel != testChannel || m.ChatID != testChatID {
			t.Errorf("message %d on %s/%s, want the original %s/%s", i, m.Channel, m.ChatID, testChannel, testChatID)
		}
	}
	if !strings.HasPrefix(all[0].Content, "I was restarted while working on your last request") {
		t.Errorf("first message = %q, want the restart notice", all[0].Content)
	}
	if all[1].Content != "recovered reply" {
		t.Errorf("second message = %q, want the replayed turn's reply", all[1].Content)
	}
	if got := primary.Requests(); len(got) != 2 || !strings.Contains(got[1].Text(), "please block") {
		t.Errorf("replay did not resend the interrupted message; requests: %d", len(got))
	}
	if pt := pendingTurns(t, cfg); len(pt) != 0 {
		t.Errorf("pending_turns after recovery = %+v, want cleared", pt)
	}
	agentMain, ok := second.al.GetRegistry().GetAgent("main")
	if !ok {
		t.Fatal("agent main missing")
	}
	if keys, err := agentMain.Sessions.ListPendingSessions(); err != nil || len(keys) != 0 {
		t.Errorf("session store pending sessions = %v (err %v), want none for %s", keys, err, sessionKey)
	}
}

// 5b. A turn that is interrupted on every replay is replayed at most twice;
// the third restart gives up with a notice and clears the flag instead of
// crash-looping.
func TestRestartMidTurn_GivesUpAfterTwoReplays(t *testing.T) {
	primary := stubprovider.New(t)
	tool := newBlockingTool()
	defer close(tool.release)
	cfg := newConfig(t, primary, nil)
	cfg.Agents.List[0].Tools = []string{tool.Name()}
	primary.SetDefault(stubprovider.CallTool(tool.Name(), ""))

	l := startLoop(t, cfg, bus.NewMessageBus(), tool)
	l.send("please block")
	tool.waitStarted(t, turnWait)
	l.crash()

	for attempt := 1; attempt <= 2; attempt++ {
		l = startLoop(t, cfg, bus.NewMessageBus(), tool)
		all := collectUntil(t, l.bus, turnWait, func(ms []bus.OutboundMessage) bool { return len(ms) >= 1 })
		if len(all) != 1 || !strings.HasPrefix(all[0].Content, "I was restarted while working on your last request — here is the result") {
			t.Fatalf("restart %d: outbound = %+v, want the replay notice only", attempt, all)
		}
		tool.waitStarted(t, turnWait)
		pending := pendingTurns(t, cfg)
		if len(pending) != 1 {
			t.Fatalf("restart %d: pending_turns = %+v", attempt, pending)
		}
		for _, pt := range pending {
			if pt.Attempts != attempt {
				t.Errorf("restart %d: attempts = %d, want %d", attempt, pt.Attempts, attempt)
			}
		}
		l.crash()
	}

	requests := primary.Count()
	l = startLoop(t, cfg, bus.NewMessageBus(), tool)
	all := collectUntil(t, l.bus, turnWait, func(ms []bus.OutboundMessage) bool { return len(ms) >= 1 })
	if len(all) != 1 {
		t.Fatalf("third restart: outbound = %+v, want the give-up notice only", all)
	}
	if all[0].Channel != testChannel || all[0].ChatID != testChatID ||
		!strings.HasPrefix(all[0].Content, "I was restarted while working on your last request and could not finish it") {
		t.Errorf("third restart: message = %+v, want the give-up notice on %s/%s", all[0], testChannel, testChatID)
	}
	if primary.Count() != requests {
		t.Errorf("third restart replayed the turn (%d requests, was %d)", primary.Count(), requests)
	}
	if pt := pendingTurns(t, cfg); len(pt) != 0 {
		t.Errorf("pending_turns after giving up = %+v, want cleared", pt)
	}
}

// 6. A 401 on the primary parks it for the auth/billing cooldown at once,
// raises exactly one operator alert, and the reply comes from the fallback.
func TestAuthFailure_ParksAlertsAndFailsOver(t *testing.T) {
	rec := testalerts.Install(t)
	primary, fallback := stubprovider.New(t), stubprovider.New(t)
	primary.SetDefault(stubprovider.Unauthorized())
	fallback.SetDefault(stubprovider.Reply("fallback ok"))
	l := startLoop(t, newConfig(t, primary, fallback), bus.NewMessageBus())
	l.al.SetAlerter(rec)

	reply, notices := l.turn("hello")
	if reply != "fallback ok" {
		t.Errorf("reply = %q, want the fallback's", reply)
	}
	want := "⚠️ primary error HTTP 401 (auth failed)" + tryingFallback
	if len(notices) != 1 || notices[0] != want {
		t.Errorf("notices = %q, want [%q]", notices, want)
	}
	if cd := l.cooldowns(); !strings.Contains(cd, primaryParked+"auth") {
		t.Errorf("cooldowns:\n%s\nwant the primary parked for auth", cd)
	}

	// Parked on the first failure for the full category cooldown: the next
	// turn does not touch the primary and says it is skipped for auth.
	reply, notices = l.turn("again")
	if reply != "fallback ok" {
		t.Errorf("second reply = %q, want the fallback's", reply)
	}
	wantSkip := "⚠️ primary unavailable — auth failed (retry in 30m). Using fallback."
	if len(notices) != 1 || notices[0] != wantSkip {
		t.Errorf("second-turn notices = %q, want [%q]", notices, wantSkip)
	}
	if primary.Count() != 1 {
		t.Errorf("primary saw %d requests, want 1 (parked after the 401)", primary.Count())
	}

	alerts := rec.Alerts()
	if len(alerts) != 1 {
		t.Fatalf("got %d alerts, want exactly 1: %+v", len(alerts), alerts)
	}
	if alerts[0].Title != "Model authentication or billing failure" {
		t.Errorf("alert title = %q", alerts[0].Title)
	}
	if alerts[0].EventID != primaryProvider+"/"+primaryModel {
		t.Errorf("alert event id = %q, want %s", alerts[0].EventID, primaryProvider+"/"+primaryModel)
	}
}
