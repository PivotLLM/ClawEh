// ClawEh
// License: MIT

package agent

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/ctxengine"
	"github.com/PivotLLM/ctxengine/memory"
	"github.com/PivotLLM/ctxengine/session"

	"github.com/PivotLLM/ClawEh/cronmsg"
	"github.com/PivotLLM/ClawEh/providers"
)

// TestCronFire_SurvivesAgeCompactionOnArrival reproduces the production loss of
// a scheduled job's fire: a session whose oldest message is past the age
// trigger receives the next fire of a job that already fired several times, the
// age-triggered compaction runs inside AddUserMessage, and the new fire must
// still be in the live window and be the request the model answers. Earlier
// fires of the same job, with replies between them, must not be dropped either.
//
// It goes through getContextManager, so the context manager and the session
// store are the ones a turn uses, built with the agent's real options; wiring a
// noise key that treats every fire of one job as a repeat makes it fail.
func TestCronFire_SurvivesAgeCompactionOnArrival(t *testing.T) {
	const (
		jobFP      = "3f9a1c0d"
		jobMessage = "Run the morning report."
		sessionKey = "agent:main:main"
	)

	al := newTestAgentLoop(t).al
	agent := al.registry.Default()
	if agent == nil {
		t.Fatal("no default agent")
	}
	// The summarization chain falls back to the agent's provider; it must be in
	// place before the context manager is built.
	summarizer := &scriptedProvider{content: `{"version":2,"state":{"goals":[{"text":"morning email reports","refs":[{"seq_start":1,"seq_end":2}]}]},"covered_seq_start":0,"covered_seq_end":0}`}
	agent.Provider = summarizer

	// Daily fires of one job with gaps, as in the incident (comments give the
	// production seqs): the oldest message is past the default 7-day age
	// trigger, and the default 5-day retention cut falls between the fire 5
	// days ago and the one 4 days ago. Times are relative to now, an hour
	// early, so no message sits on the retention boundary whenever it runs.
	now := time.Now()
	at := func(daysAgo int, minutes int) time.Time {
		return now.AddDate(0, 0, -daysAgo).Add(-time.Hour + time.Duration(minutes)*time.Minute)
	}
	fire := func(daysAgo int) memory.StoredMessage {
		ts := at(daysAgo, 0)
		return memory.StoredMessage{CreatedAt: ts, Role: "user", Content: cronmsg.Build(jobFP, ts, jobMessage)}
	}
	report := func(daysAgo int, text string) memory.StoredMessage {
		return memory.StoredMessage{CreatedAt: at(daysAgo, 5), Role: "assistant", Content: text}
	}

	seeded := []memory.StoredMessage{
		report(10, "Morning email: 3 new messages, nothing urgent."), // 536
		fire(7), // 537
		report(7, "Morning email: 5 new messages, one invoice."), // 538
		fire(6), // 539 (no reply)
		fire(5), // 540
		report(5, "Morning email: 2 new messages from Bob."), // 541
		fire(4), // 542
		report(4, "Morning email: 4 new messages, a meeting request."), // 543
		fire(3),                                  // 544
		report(3, "Morning email: inbox empty."), // 545
	}

	cm, release := al.getContextManager(agent, sessionKey)
	defer release()
	ctx := context.Background()

	// Write each message through the context manager, as a turn does (window
	// and archive; the summary must cite archived seqs), then backdate the
	// window, whose timestamps the age trigger and retention read.
	for i := range seeded {
		add := cm.AddAssistantMessage
		if seeded[i].Role == "user" {
			add = cm.AddUserMessage
		}
		seq, err := add(ctx, seeded[i].Message)
		if err != nil {
			t.Fatalf("seed message %d: %v", i, err)
		}
		seeded[i].Seq = seq
	}
	store, ok := agent.Sessions.(*session.SQLiteStore)
	if !ok {
		t.Fatalf("agent session store is %T, want *session.SQLiteStore", agent.Sessions)
	}
	if err := store.SetHistoryWithSeqs(sessionKey, seeded); err != nil {
		t.Fatalf("backdate session: %v", err)
	}
	if summarizer.callCount() != 0 {
		t.Fatal("compaction ran while seeding; the seeded window is not the incident's")
	}

	newFire := cronmsg.Build(jobFP, now, jobMessage) // 546
	if _, err := cm.AddUserMessage(ctx, providers.Message{Role: "user", Content: newFire}); err != nil {
		t.Fatalf("AddUserMessage: %v", err)
	}

	if summarizer.callCount() == 0 {
		t.Fatal("the age-triggered compaction did not run; the test is not exercising the incident")
	}
	if agent.Sessions.GetSummary(sessionKey) == "" {
		t.Fatal("the age-triggered compaction produced no summary")
	}

	window := agent.Sessions.GetHistory(sessionKey)
	contents := make([]string, 0, len(window))
	for _, m := range window {
		contents = append(contents, m.Content)
	}
	describe := func() string {
		var b strings.Builder
		for _, m := range window {
			fmt.Fprintf(&b, "\n  %s: %.80q", m.Role, m.Content)
		}
		return b.String()
	}

	// The fires inside the retention period, replies between them, stay in
	// the live window as separate messages.
	for _, want := range []memory.StoredMessage{fire(4), report(4, "Morning email: 4 new messages, a meeting request."), fire(3), report(3, "Morning email: inbox empty.")} {
		if !slices.Contains(contents, want.Content) {
			t.Errorf("live window lost %s %.60q; window:%s", want.Role, want.Content, describe())
		}
	}
	if !slices.Contains(contents, newFire) {
		t.Fatalf("live window lost the fire that just arrived; window:%s", describe())
	}

	asm, err := cm.Assemble(ctx, ctxengine.AssembleRequest{})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	lastUser := ""
	for _, m := range asm.Messages {
		if m.Role == "user" {
			lastUser = m.Content
		}
	}
	if lastUser != newFire {
		t.Errorf("the request answers %.80q, want the fire that just arrived %.80q", lastUser, newFire)
	}
}
