// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ctlExampleReply scripts the example configuration: findings JSON in review, text
// in debate, GUIDE then STOP from the chair, a Markdown report.
func ctlExampleReply(f *ctlForum, cl ctlCall) Reply {
	switch {
	case cl.Moderator && cl.Round == 1:
		return Reply{Outcome: OutcomeOK, Text: `{"decision":"GUIDE","reason":"Rollback is unexamined.","guidance":"Examine rollback."}`}
	case cl.Moderator:
		return Reply{Outcome: OutcomeOK, Text: `{"decision":"STOP","reason":"Positions are settled.","guidance":null}`}
	case cl.Layer == "review":
		return Reply{Outcome: OutcomeOK, Text: fmt.Sprintf(`{"agreements":["%s agrees"],"disagreements":["%s objects"]}`, cl.Participant, cl.Participant)}
	}
	return f.reply(cl)
}

// The example configuration end to end: Alice as herself, Bob as a clone, the
// chair and the editor fresh.
func TestCtlSpecExample(t *testing.T) {
	f := ctlLaunchRaw(t, []byte(cfgtExampleJSON))
	f.msg.respond = func(cl ctlCall) (Reply, error) { return ctlExampleReply(f, cl), nil }
	_, st := f.run()
	ctlWant(t, "status", st, StatusCompleted)
	res := f.result()
	ctlWant(t, "complete", res.Complete, true)
	ctlWant(t, "calls", res.Calls, 2+(2+1)+(2+1)+1) // review; debate r1+check, r2+check; report
	ctlWant(t, "result layers", len(res.Layers), 1)
	ctlWant(t, "result layer", res.Layers[0].LayerID, "report")
	ctlWant(t, "report outputs", len(res.Layers[0].Outputs), 1)
	debate := f.state().Layers["debate"]
	ctlWant(t, "debate reason", debate.EndReason, EndModeratorStop)
	ctlWant(t, "debate rounds", debate.RoundsPublished, 2)

	// Bob's review message is independent of Alice's (after_round).
	bobReview := f.ctlOne("bob", "review", 1, false).Message
	ctlContains(t, "bob review", bobReview, "Expose weaknesses before adoption.", "Challenge trust assumptions", "Proposal: replace the help desk", `"disagreements"`)
	ctlLacks(t, "bob review", bobReview, "alice agrees", "Challenge cost")
	// Debate: the routed review findings arrive once, in the first turn.
	alice1 := f.ctlOne("alice", "debate", 1, false).Message
	ctlContains(t, "alice debate 1", alice1, "Challenge findings", "bob objects", "alice agrees")
	ctlLacks(t, "alice debate 1", alice1, "Expose weaknesses")
	alice2 := f.ctlOne("alice", "debate", 2, false).Message
	ctlContains(t, "alice debate 2", alice2, "Examine rollback.", ctlMark("bob", "debate", 1))
	ctlLacks(t, "alice debate 2", alice2, "bob objects", ctlMark("alice", "debate", 1))
	// The chair gets its own route (the report) but never the review layer.
	chair := f.ctlOne("chair", "debate", 1, true).Message
	ctlContains(t, "chair", chair, "End repetitive discussion", "Proposal: replace the help desk", ctlMark("alice", "debate", 1))
	ctlLacks(t, "chair", chair, "bob objects")
	// The editor reads the source, the review and the debate.
	editor := f.ctlOne("editor", "report", 1, false).Message
	ctlContains(t, "editor", editor, "Prioritize supported findings", "alice agrees", ctlMark("bob", "debate", 2))

	tr := f.transcript()
	order := []string{
		"review · round 1 · alice", "review · round 1 · bob", "debate · round 1 · alice", "debate · round 1 · bob",
		"moderator chair: GUIDE", "Guidance: Examine rollback.", "debate · round 2 · alice", "moderator chair: STOP", "report · round 1 · editor",
	}
	pos := 0
	for _, want := range order {
		i := strings.Index(tr[pos:], want)
		if i < 0 {
			t.Fatalf("transcript lacks %q after offset %d:\n%s", want, pos, tr)
		}
		pos += i
	}
	ctlLacks(t, "transcript", tr, "Challenge cost", "Prioritize supported", "End repetitive")
}

// pause drains the in-flight turn, commits it and stops; resume finishes
// without resending it.
func TestCtlPauseDrainsAndResumes(t *testing.T) {
	f := ctlLaunch(t, ctlConfig(ctlLayer("talk", DeliveryPerTurn, 2, FormatText)))
	inFlight, release := make(chan struct{}), make(chan struct{})
	f.msg.hook = func(_ context.Context, cl ctlCall) error {
		if cl.Participant == "alice" && cl.Round == 2 {
			close(inFlight)
			<-release
		}
		return nil
	}
	c := f.open()
	done := make(chan Status)
	go func() {
		st, err := c.Run(context.Background())
		if err != nil {
			t.Errorf("Run: %v", err)
		}
		done <- st
	}()
	<-inFlight
	if err := c.RequestPause(); err != nil {
		t.Fatal(err)
	}
	ctlWant(t, "status while draining", c.State().Status, StatusPausing)
	if err := c.RequestPause(); err != nil {
		t.Fatalf("repeated pause: %v", err)
	}
	close(release)
	ctlWant(t, "run status", <-done, StatusPaused)
	ctlWant(t, "status on disk", f.state().Status, StatusPaused)
	if c.committedOutput("talk", turnID(2, "alice")) == nil {
		t.Fatal("the in-flight turn was not committed")
	}
	ctlWant(t, "bob round 2 before resume", len(f.msg.find("bob", "talk", 2, false)), 0)
	ctlContains(t, "transcript", f.transcript(), ctlMark("alice", "talk", 2))

	if _, err := f.open().Run(context.Background()); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("running a paused forum without a resume: %v", err)
	}
	f.msg.hook = nil
	_, st := f.resume()
	ctlWant(t, "status after resume", st, StatusCompleted)
	ctlWant(t, "alice round 2 calls", len(f.msg.find("alice", "talk", 2, false)), 1)
	ctlWant(t, "bob round 2 calls", len(f.msg.find("bob", "talk", 2, false)), 1)
	ctlWant(t, "transcript entries", strings.Count(f.transcript(), "### talk"), 4)
}

// Pausing an after_round round lets every in-flight turn finish; nothing
// new is dispatched.
func TestCtlPauseAfterRoundParallel(t *testing.T) {
	f := ctlLaunch(t, ctlConfig(ctlLayer("talk", DeliveryAfterRound, 2, FormatText)))
	var arrived sync.WaitGroup
	arrived.Add(2)
	release := make(chan struct{})
	f.msg.hook = func(_ context.Context, cl ctlCall) error {
		if cl.Round == 1 {
			arrived.Done()
			<-release
		}
		return nil
	}
	c := f.open()
	done := make(chan Status)
	go func() {
		st, err := c.Run(context.Background())
		if err != nil {
			t.Errorf("Run: %v", err)
		}
		done <- st
	}()
	arrived.Wait()
	if err := c.RequestPause(); err != nil {
		t.Fatal(err)
	}
	close(release)
	ctlWant(t, "status", <-done, StatusPaused)
	ctlWant(t, "calls", len(f.msg.all()), 2)
	ctlWant(t, "rounds published", f.state().Layers["talk"].RoundsPublished, 1)
	f.msg.hook = nil
	_, st := f.resume()
	ctlWant(t, "status", st, StatusCompleted)
	ctlWant(t, "calls", len(f.msg.all()), 4)
}

// Cancel cancels the in-flight ask, keeps committed outputs and ends
// cancelled (terminal, with result.json); the cancelled attempt stays
// reserved without a reply.
func TestCtlCancelInFlight(t *testing.T) {
	f := ctlLaunch(t, ctlConfig(ctlLayer("talk", DeliveryPerTurn, 2, FormatText)))
	inFlight := make(chan struct{})
	f.msg.hook = func(ctx context.Context, cl ctlCall) error {
		if cl.Participant == "bob" && cl.Round == 2 {
			close(inFlight)
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}
	c := f.open()
	done := make(chan Status)
	go func() {
		st, err := c.Run(context.Background())
		if err != nil {
			t.Errorf("Run: %v", err)
		}
		done <- st
	}()
	<-inFlight
	if err := c.RequestCancel(); err != nil {
		t.Fatal(err)
	}
	ctlWant(t, "status", <-done, StatusCancelled)
	res := f.result()
	ctlWant(t, "result status", res.Status, StatusCancelled)
	ctlWant(t, "reason", res.Reason, EndCancelled)
	ctlWant(t, "kept outputs", len(res.Layers[0].Outputs), 3)
	att := f.attempts("talk")
	last := att[len(att)-1]
	if last.Request.Turn != turnID(2, "bob") || last.Reply != nil {
		t.Fatalf("cancelled attempt: %+v", last)
	}
	if err := c.RequestCancel(); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("cancel after the end: %v", err)
	}
	if err := c.RequestPause(); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("pause after the end: %v", err)
	}
	if st, err := f.open().Run(context.Background()); err != nil || st != StatusCancelled {
		t.Fatalf("re-running a cancelled forum: %s, %v", st, err)
	}
	ctlWant(t, "calls after re-run", len(f.msg.all()), 4)
}

// A paused forum can be cancelled; running it then ends it cancelled
// without dispatching. The controller whose Run already returned refuses
// the request (nothing would complete it); a controller opened for the
// purpose, as the service does, accepts it.
func TestCtlCancelPaused(t *testing.T) {
	f := ctlLaunch(t, ctlConfig(ctlLayer("talk", DeliveryPerTurn, 2, FormatText)))
	c := f.open()
	if err := c.RequestPause(); err != nil {
		t.Fatal(err)
	}
	st, err := c.Run(context.Background())
	if err != nil || st != StatusPaused {
		t.Fatalf("Run: %s, %v", st, err)
	}
	ctlWant(t, "calls", len(f.msg.all()), 0)
	for name, req := range map[string]func() error{"cancel": c.RequestCancel, "pause": c.RequestPause} {
		reqErr := req()
		if !errors.Is(reqErr, errRunEnded) || !errors.Is(reqErr, ErrInvalidState) || !strings.Contains(reqErr.Error(), "forum ctl test ("+f.s.ID()+")") {
			t.Errorf("%s after Run returned: %v, want a refusal naming the forum", name, reqErr)
		}
	}
	ctlWant(t, "status after the refusals", f.state().Status, StatusPaused)
	c = f.open()
	if err = c.RequestCancel(); err != nil {
		t.Fatal(err)
	}
	st, err = c.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctlWant(t, "status", st, StatusCancelled)
	ctlWant(t, "calls", len(f.msg.all()), 0)
}

// Requests name the forum when the state refuses them.
func TestCtlRequestRefusalsNameTheForum(t *testing.T) {
	f := ctlLaunch(t, ctlConfig(ctlLayer("talk", DeliveryPerTurn, 1, FormatText)))
	_, st := f.run()
	ctlWant(t, "status", st, StatusCompleted)
	c := f.open() // a fresh controller over the terminal forum: Run not called
	for name, req := range map[string]func() error{"cancel": c.RequestCancel, "pause": c.RequestPause} {
		err := req()
		if !errors.Is(err, ErrInvalidState) || errors.Is(err, errRunEnded) ||
			!strings.Contains(err.Error(), "forum ctl test ("+f.s.ID()+") is") || !strings.Contains(err.Error(), "completed") {
			t.Errorf("%s of a completed forum: %v", name, err)
		}
	}
}

// Pause and cancel racing a run: whatever the interleaving, the log
// replays to the controller's state, a requested cancel always wins, and
// a paused forum resumes to the end.
func TestCtlPauseCancelRaces(t *testing.T) {
	for i := range 12 {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			layer := ctlLayer("talk", DeliveryAfterRound, 3, FormatText)
			layer.Moderator = &Moderator{Participant: "chair", AfterRound: 1, EveryRounds: 1}
			f := ctlLaunch(t, ctlConfig(layer))
			rng := rand.New(rand.NewPCG(uint64(i), 1))
			var rngMu sync.Mutex // parallel turns share the generator
			f.msg.hook = func(ctx context.Context, _ ctlCall) error {
				rngMu.Lock()
				wait := time.Duration(rng.IntN(3)) * time.Millisecond
				rngMu.Unlock()
				select {
				case <-time.After(wait):
				case <-ctx.Done():
					return ctx.Err()
				}
				return nil
			}
			c := f.open()
			done := make(chan Status)
			go func() {
				st, err := c.Run(context.Background())
				if err != nil {
					t.Errorf("Run: %v", err)
				}
				done <- st
			}()
			time.Sleep(time.Duration(i%5) * time.Millisecond)
			var wg sync.WaitGroup
			var cancelErr error
			wg.Go(func() {
				if err := c.RequestPause(); err != nil && !errors.Is(err, ErrInvalidState) {
					t.Errorf("RequestPause: %v", err)
				}
			})
			if i%2 == 0 {
				wg.Go(func() { cancelErr = c.RequestCancel() })
			}
			wg.Wait()
			st := <-done
			a := ctlJSON(t, c.State())
			b := ctlJSON(t, f.state())
			ctlWant(t, "state", a, b)
			switch f.state().Status {
			case StatusPaused:
				_, st = f.resume()
			case StatusCancelling:
				_, st = f.run()
			default:
			}
			switch {
			case i%2 == 0 && cancelErr == nil:
				ctlWant(t, "status after cancel", st, StatusCancelled)
			case i%2 == 0:
				ctlWant(t, "status when cancel came too late", st, StatusCompleted)
			default:
				ctlWant(t, "status", st, StatusCompleted)
			}
			if _, err := f.s.ReadResult(); err != nil {
				t.Fatalf("result: %v", err)
			}
		})
	}
}

// Restart: a saved accepted reply is adopted without asking again.
func TestCtlRestartAdoptsSavedReply(t *testing.T) {
	f := ctlLaunch(t, ctlConfig(ctlLayer("talk", DeliveryPerTurn, 1, FormatText)))
	c := f.open()
	c.crashHook = func(event string) bool { return event == "reply" }
	if _, err := c.Run(context.Background()); !errors.Is(err, errCrashed) {
		t.Fatalf("Run: %v", err)
	}
	ctlWant(t, "calls before restart", len(f.msg.all()), 1)
	_, st := f.run()
	ctlWant(t, "status", st, StatusCompleted)
	ctlWant(t, "alice calls", len(f.msg.find("alice", "talk", 1, false)), 1)
	ctlWant(t, "alice attempts", len(f.attempts("talk")), 2)
}

// Restart: an attempt the shutdown cut uses up none. With one attempt
// allowed, it is resent as the same attempt and the run completes; the
// cut send still counts as a call, and the output is marked resent.
func TestCtlRestartResendsTheSameAttempt(t *testing.T) {
	cfg := ctlConfig(ctlLayer("talk", DeliveryPerTurn, 1, FormatText))
	cfg.Limits.MaxAttemptsPerTurn = 1
	f := ctlLaunch(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	f.msg.hook = func(ctx context.Context, cl ctlCall) error {
		cancel()
		return ctx.Err()
	}
	if _, err := f.open().Run(ctx); err == nil {
		t.Fatal("Run survived the shutdown")
	}
	f.msg.hook = nil
	_, st := f.run()
	ctlWant(t, "status", st, StatusCompleted)
	ctlWant(t, "alice calls", len(f.msg.find("alice", "talk", 1, false)), 2)
	ctlWant(t, "state calls", f.state().Calls, 2+1) // alice twice, bob once
	att := f.attempts("talk")
	if len(att) != 2 || att[0].Request.Attempt != 1 || !att[0].Request.Resent || att[0].Reply == nil {
		t.Fatalf("attempts = %+v, want alice's attempt 1 resent and answered", att)
	}
	outs := f.state().Layers["talk"].Outputs
	if len(outs) != 2 || outs[0].Attempt != 1 || !outs[0].Resent || outs[1].Resent {
		t.Errorf("outputs = %+v, want alice's attempt 1 marked resent", outs)
	}
}

// ctlCrashConfig is the example configuration with every controller path switched
// on: a directed message and an assessment, a JSON repair, a moderator
// that guides then stops.
func ctlCrashConfig(t *testing.T) []byte {
	t.Helper()
	cfg, err := decodeConfig([]byte(cfgtExampleJSON))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Limits.MaxAttemptsPerTurn = 3
	cfg.Limits.MaxCalls = 60
	seed := int64(11)
	cfg.Seed = &seed
	cfg.Schemas["assess"] = json.RawMessage(`{"type":"object","required":["score"],"properties":{"score":{"type":"integer"}}}`)
	for i := range cfg.Layers {
		if m := cfg.Layers[i].Moderator; m != nil {
			m.AllowDirected = true
			m.Schema = "assess"
		}
	}
	cfg.ResultLayers = []string{"review", "debate", "report"}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// ctlCrashForum launches the crash scenario with a stateless script, so a
// resend after a crash gets the same answer.
func ctlCrashForum(t *testing.T) *ctlForum {
	t.Helper()
	f := ctlLaunchRaw(t, ctlCrashConfig(t))
	f.msg.respond = func(cl ctlCall) (Reply, error) {
		switch {
		case cl.Moderator && cl.Round == 1:
			return Reply{Outcome: OutcomeOK, Text: `{"decision":"GUIDE","reason":"R1","guidance":"G1","assessment":{"score":1},"directed":[{"to":"bob","text":"DIRECTED-BOB"}]}`}, nil
		case cl.Moderator:
			return Reply{Outcome: OutcomeOK, Text: `{"decision":"STOP","reason":"R2","guidance":null,"assessment":{"score":2}}`}, nil
		case cl.Layer == "review" && cl.Participant == "bob" && !cl.Repair:
			return Reply{Outcome: OutcomeOK, Text: `{"agreements":"not an array"}`}, nil
		}
		return ctlExampleReply(f, cl), nil
	}
	return f
}

// ctlCrashOutcome is what must not depend on where a crash happened.
func ctlCrashOutcome(t *testing.T, f *ctlForum) string {
	t.Helper()
	res := f.result()
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s complete=%v omissions=%v\n", res.Status, res.Reason, res.Complete, res.Omissions)
	for _, l := range res.Layers {
		fmt.Fprintf(&b, "layer %s ended=%v %s\n", l.LayerID, l.Ended, l.EndReason)
		for _, o := range l.Outputs {
			data, err := f.s.ReadFile(o.PublishedFile)
			if err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&b, "  %s %s r%d %s\n", o.Turn, o.ParticipantID, o.Round, data)
		}
	}
	b.WriteString(f.transcript())
	return b.String()
}

// Crash and restart at every durable write of a full run (every commit,
// before and after state.json, every request, reply, output, inputs,
// transcript and result write): the resumed run reaches the same result
// and transcript, publishes every output once, and its state replays.
func TestCtlCrashAtEveryBoundary(t *testing.T) {
	ref := ctlCrashForum(t)
	var events []string
	var mu sync.Mutex
	c := ref.open()
	c.crashHook = func(ev string) bool {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
		return false
	}
	if st, err := c.Run(context.Background()); err != nil || st != StatusCompleted {
		t.Fatalf("reference run: %s, %v", st, err)
	}
	want := ctlCrashOutcome(t, ref)
	ctlContains(t, "reference transcript", want, "G1", "moderator chair: STOP")
	ctlLacks(t, "reference transcript", ref.transcript(), "DIRECTED-BOB", `"score"`)
	seen := map[string]bool{}
	for _, ev := range events {
		seen[ev] = true
	}
	for _, ev := range []string{"inputs", "request", "commit-before-state", "commit", "reply", "output", "transcript", "result"} {
		if !seen[ev] {
			t.Fatalf("reference run never wrote %q", ev)
		}
	}
	for n := 1; n <= len(events); n++ {
		t.Run(fmt.Sprintf("%03d-%s", n, events[n-1]), func(t *testing.T) {
			f := ctlCrashForum(t)
			c := f.open()
			var k atomic.Int32
			c.crashHook = func(string) bool { return int(k.Add(1)) == n }
			if _, err := c.Run(context.Background()); !errors.Is(err, errCrashed) {
				t.Fatalf("crashed run: %v", err)
			}
			_, st := f.run()
			ctlWant(t, "status", st, StatusCompleted)
			ctlWant(t, "outcome", ctlCrashOutcome(t, f), want)
			commits, err := f.s.ReadCommits()
			if err != nil {
				t.Fatal(err)
			}
			turns := map[string]int{}
			for _, cm := range commits {
				if cm.Kind == CommitTurn || cm.Kind == CommitModerated {
					turns[cm.Layer+"/"+cm.Turn]++
				}
			}
			for turn, count := range turns {
				if count != 1 {
					t.Fatalf("%s committed %d times", turn, count)
				}
			}
			if bob := f.msg.find("bob", "debate", 2, false); len(bob) == 0 || !strings.Contains(bob[len(bob)-1].Message, "DIRECTED-BOB") {
				t.Fatal("the directed message did not reach Bob's next turn")
			}
			if st := f.state(); st.Calls > f.snap.Limits.MaxCalls {
				t.Fatalf("calls %d above the limit", st.Calls)
			}
		})
	}
}

// A single_shot moderator is sent its earlier decisions in the layer and
// the whole conversation each time.
func TestCtlSingleShotModerator(t *testing.T) {
	layer := ctlLayer("debate", DeliveryAfterRound, 3, FormatText)
	layer.Moderator = &Moderator{Participant: "chair", AfterRound: 1, EveryRounds: 1}
	cfg := ctlConfig(layer)
	chair := cfg.Participants["chair"]
	chair.Mode = FreshModeSingleShot
	cfg.Participants["chair"] = chair
	f := ctlLaunch(t, cfg)
	f.msg.respond = func(cl ctlCall) (Reply, error) {
		if cl.Moderator {
			return Reply{Outcome: OutcomeOK, Text: fmt.Sprintf(`{"decision":"CONTINUE","reason":"REASON-%d","guidance":null}`, cl.Round)}, nil
		}
		return f.reply(cl), nil
	}
	_, st := f.run()
	ctlWant(t, "status", st, StatusCompleted)
	second := f.ctlOne("chair", "debate", 2, true).Message
	ctlContains(t, "single_shot moderator", second, "BRIEF-PURPOSE", "PRIV-CHAIR", headingDecisions, "REASON-1",
		ctlMark("alice", "debate", 1), ctlMark("bob", "debate", 2))
}

// openForum rebuilds a transcript that a crash left with a gap, a duplicate or
// a torn entry, from the commit log, without touching a correct one.
func TestCtlOpenRebuildsTranscript(t *testing.T) {
	layer := ctlLayer("debate", DeliveryPerTurn, 2, FormatText)
	layer.Moderator = &Moderator{Participant: "chair", AfterRound: 1, EveryRounds: 1}
	f := ctlLaunch(t, ctlConfig(layer))
	f.run()
	want := f.transcript()
	entries := strings.SplitAfter(want, "\n\n### ")
	for name, damaged := range map[string]string{
		"gap":       strings.Join(append(slices.Clone(entries[:2]), entries[3:]...), ""),
		"duplicate": want + entries[len(entries)-1],
		"torn":      want[:len(want)-7],
		"missing":   "",
	} {
		t.Run(name, func(t *testing.T) {
			if err := f.s.writeRel(fileTranscript, []byte(damaged), false); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(f.s.Path(fileTranscript))
			if err != nil {
				t.Fatal(err)
			}
			f.open()
			ctlWant(t, "transcript", f.transcript(), want)
			// Rewritten in place: a reader following the file keeps it.
			after, err := os.Stat(f.s.Path(fileTranscript))
			if err != nil || !os.SameFile(before, after) {
				t.Errorf("the transcript was replaced by another file (%v)", err)
			}
		})
	}
}

// The host shutting down (ErrShuttingDown from Ask, ctx still alive)
// leaves the attempt uncertain and the forum running on disk: no failed
// reply, no failure. A later run resends it and completes.
func TestCtlShuttingDownLeavesAttemptUncertain(t *testing.T) {
	f := ctlLaunch(t, ctlConfig(ctlLayer("talk", DeliveryPerTurn, 1, FormatText)))
	f.msg.hook = func(context.Context, ctlCall) error {
		return fmt.Errorf("agent loop stopping: %w", ErrShuttingDown)
	}
	if _, err := f.open().Run(context.Background()); !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("Run: %v, want ErrShuttingDown", err)
	}
	ctlWant(t, "status on disk", f.state().Status, StatusRunning)
	att := f.attempts("talk")
	ctlWant(t, "attempts", len(att), 1)
	if att[0].Reply != nil {
		t.Errorf("a reply was recorded for the shutdown: %+v", att[0].Reply)
	}
	f.msg.hook = nil
	_, st := f.run()
	ctlWant(t, "status", st, StatusCompleted)
}

// A turn cancelled while the run's context has ended (the service is
// closing) is not recorded as a failed reply either.
func TestCtlCancelledByShutdownIsNotAFailedAttempt(t *testing.T) {
	f := ctlLaunch(t, ctlConfig(ctlLayer("talk", DeliveryPerTurn, 1, FormatText)))
	ctx, cancel := context.WithCancel(context.Background())
	f.msg.respond = func(cl ctlCall) (Reply, error) {
		cancel()
		return Reply{Outcome: OutcomeCancelled}, nil
	}
	if _, err := f.open().Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run: %v, want context.Canceled", err)
	}
	att := f.attempts("talk")
	if len(att) != 1 || att[0].Reply != nil {
		t.Fatalf("attempts after the shutdown: %+v", att)
	}
	ctlWant(t, "status on disk", f.state().Status, StatusRunning)

	// Without a shutdown, a cancelled turn is an unsuccessful attempt.
	f.msg.respond = func(cl ctlCall) (Reply, error) {
		if cl.Participant == "alice" && len(f.msg.find("alice", "talk", 1, false)) == 2 {
			return Reply{Outcome: OutcomeCancelled}, nil
		}
		return f.reply(cl), nil
	}
	_, st := f.run()
	ctlWant(t, "status", st, StatusCompleted)
	att = f.attempts("talk")
	// The resend is still attempt 1 (the shutdown used up none); its
	// cancelled outcome is recorded and attempt 2 answers.
	if len(att) != 3 || !att[0].Request.Resent || att[0].Reply == nil || att[0].Reply.Outcome != OutcomeCancelled {
		t.Errorf("attempts = %+v, want the resent attempt 1 recorded as cancelled", att)
	}
}

// A run that meets ErrCorrupt ends the forum failed with EndCorrupt and
// logs the cause at Error naming the forum.
func TestCtlCorruptEndsFailed(t *testing.T) {
	f := ctlLaunch(t, ctlConfig(ctlLayer("talk", DeliveryPerTurn, 1, FormatText)))
	c := f.open()
	c.crashHook = func(event string) bool { return event == "reply" }
	if _, err := c.Run(context.Background()); !errors.Is(err, errCrashed) {
		t.Fatalf("Run: %v", err)
	}
	// The saved accepted reply no longer validates (an empty text).
	rel := attemptRel("talk", turnID(1, "alice"), 1) + "/" + fileReply
	var reply map[string]any
	data, err := f.s.ReadFile(rel)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &reply); err != nil {
		t.Fatal(err)
	}
	reply["text"] = ""
	if data, err = json.Marshal(reply); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(f.s.Path(rel), data, 0o600); err != nil {
		t.Fatal(err)
	}
	c = f.open()
	st, err := c.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	ctlWant(t, "status", st, StatusFailed)
	ctlWant(t, "reason", f.result().Reason, EndCorrupt)
	found := false
	for _, line := range f.log.lines {
		found = found || (strings.HasPrefix(line, "ERROR forum ctl test ("+f.s.ID()+") run 1: its records are corrupt") && strings.Contains(line, "no longer validates"))
	}
	if !found {
		t.Errorf("the corruption was not logged at Error: %q", f.log.lines)
	}
}
