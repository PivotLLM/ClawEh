// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// Every row of the §5 table, with what each participant may and may not
// see when its message is composed.
func TestCtlExecutionTable(t *testing.T) {
	type visibility struct {
		pid, layer string
		round      int
		sees       []string
		hides      []string
	}
	tests := []struct {
		name  string
		layer Layer
		calls int
		check []visibility
	}{
		{
			name:  "one participant, one round",
			layer: Layer{ID: "draft", Participants: []string{"alice"}, Instructions: "LAYER-draft", Delivery: DeliveryAfterRound, MaxRounds: 1, Output: Output{Format: FormatMarkdown}},
			calls: 1,
			check: []visibility{{pid: "alice", layer: "draft", round: 1, sees: []string{"BRIEF-PURPOSE", "PRIV-ALICE", "LAYER-draft"}}},
		},
		{
			name:  "several participants, one round, after_round",
			layer: ctlLayer("opinions", DeliveryAfterRound, 1, FormatText),
			calls: 2,
			check: []visibility{
				{pid: "alice", layer: "opinions", round: 1, hides: []string{ctlMark("bob", "opinions", 1)}},
				{pid: "bob", layer: "opinions", round: 1, hides: []string{ctlMark("alice", "opinions", 1)}},
			},
		},
		{
			name:  "several participants, one round, per_turn",
			layer: ctlLayer("sequence", DeliveryPerTurn, 1, FormatText),
			calls: 2,
			check: []visibility{
				{pid: "alice", layer: "sequence", round: 1, hides: []string{ctlMark("bob", "sequence", 1)}},
				{pid: "bob", layer: "sequence", round: 1, sees: []string{ctlMark("alice", "sequence", 1)}},
			},
		},
		{
			name:  "several rounds, after_round",
			layer: ctlLayer("exchange", DeliveryAfterRound, 2, FormatText),
			calls: 4,
			check: []visibility{
				{pid: "bob", layer: "exchange", round: 1, hides: []string{ctlMark("alice", "exchange", 1)}},
				{
					pid: "alice", layer: "exchange", round: 2, sees: []string{ctlMark("bob", "exchange", 1)},
					hides: []string{ctlMark("alice", "exchange", 1), ctlMark("bob", "exchange", 2)},
				},
				{
					pid: "bob", layer: "exchange", round: 2, sees: []string{ctlMark("alice", "exchange", 1)},
					hides: []string{ctlMark("bob", "exchange", 1), ctlMark("alice", "exchange", 2)},
				},
			},
		},
		{
			name:  "several rounds, per_turn",
			layer: ctlLayer("talk", DeliveryPerTurn, 2, FormatText),
			calls: 4,
			check: []visibility{
				{pid: "bob", layer: "talk", round: 1, sees: []string{ctlMark("alice", "talk", 1)}},
				{pid: "alice", layer: "talk", round: 2, sees: []string{ctlMark("bob", "talk", 1)}, hides: []string{ctlMark("alice", "talk", 1)}},
				{
					pid: "bob", layer: "talk", round: 2, sees: []string{ctlMark("alice", "talk", 2)},
					hides: []string{ctlMark("alice", "talk", 1), ctlMark("bob", "talk", 1)},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := ctlConfig(tt.layer)
			cfg.Limits.MaxParallelCalls = 1 // after_round isolation must hold even when a peer committed first
			f := ctlLaunch(t, cfg)
			_, st := f.run()
			ctlWant(t, "status", st, StatusCompleted)
			ctlWant(t, "calls", len(f.msg.all()), tt.calls)
			res := f.result()
			ctlWant(t, "result layers", len(res.Layers), 1)
			ctlWant(t, "end reason", res.Layers[0].EndReason, EndRoundLimit)
			ctlWant(t, "outputs", len(res.Layers[0].Outputs), tt.calls)
			ctlWant(t, "complete", res.Complete, true)
			ctlWant(t, "omissions", len(res.Omissions), 0)
			for _, v := range tt.check {
				msg := f.ctlOne(v.pid, v.layer, v.round, false).Message
				ctlContains(t, v.pid+" message", msg, v.sees...)
				ctlLacks(t, v.pid+" message", msg, v.hides...)
			}
		})
	}
}

// §2.3: the first message in the forum carries the brief; the first in a
// layer the private instructions, layer instructions and routed inputs;
// later messages only what is new. Source text is quoted data.
func TestCtlDeltaMessages(t *testing.T) {
	l1 := ctlLayer("first", DeliveryPerTurn, 2, FormatText)
	l2 := ctlLayer("second", DeliveryAfterRound, 1, FormatText)
	l2.Inputs = []Route{{From: "layer:first", Select: SelectLastPerParticipant}}
	f := ctlLaunch(t, ctlConfig(l1, l2))
	_, st := f.run()
	ctlWant(t, "status", st, StatusCompleted)

	first := f.ctlOne("alice", "first", 1, false).Message
	ctlContains(t, "first message", first, headingBrief, "BRIEF-PURPOSE", "BRIEF-CONSTRAINT", headingInstructions, "PRIV-ALICE",
		headingLayer, "LAYER-first", headingInputs, dataNote, "### Source \"notes\"", "```text\nSOURCE-NOTES\n```", headingReply)
	ctlLacks(t, "first message", first, "PRIV-BOB")

	later := f.ctlOne("alice", "first", 2, false).Message
	ctlContains(t, "later message", later, headingNew, ctlMark("bob", "first", 1))
	ctlLacks(t, "later message", later, "BRIEF-PURPOSE", "PRIV-ALICE", "LAYER-first", "SOURCE-NOTES", ctlMark("alice", "first", 1))

	next := f.ctlOne("alice", "second", 1, false).Message
	ctlContains(t, "next layer", next, "PRIV-ALICE", "LAYER-second", headingInputs, ctlMark("bob", "first", 2), ctlMark("alice", "first", 2))
	ctlLacks(t, "next layer", next, "BRIEF-PURPOSE", ctlMark("bob", "first", 1))
}

// A single_shot participant gets its full routed context every turn,
// including its own earlier outputs marked as its own; a participant that
// keeps its conversation gets deltas.
func TestCtlSingleShotFullContext(t *testing.T) {
	cfg := ctlConfig(ctlLayer("talk", DeliveryPerTurn, 3, FormatText))
	bob := cfg.Participants["bob"]
	bob.Mode = FreshModeSingleShot
	cfg.Participants["bob"] = bob
	f := ctlLaunch(t, cfg)
	_, st := f.run()
	ctlWant(t, "status", st, StatusCompleted)
	for round := 1; round <= 3; round++ {
		msg := f.ctlOne("bob", "talk", round, false).Message
		ctlContains(t, "single_shot message", msg, "BRIEF-PURPOSE", "PRIV-BOB", "LAYER-talk", "SOURCE-NOTES", ctlMark("alice", "talk", round))
		for r := 1; r < round; r++ {
			ctlContains(t, "single_shot message", msg, ctlMark("alice", "talk", r), ctlMark("bob", "talk", r), "You (Bob), round")
		}
		if round > 1 {
			ctlContains(t, "single_shot message", msg, headingSoFar)
			alice := f.ctlOne("alice", "talk", round, false).Message
			ctlLacks(t, "memory participant", alice, "BRIEF-PURPOSE", "SOURCE-NOTES", ctlMark("bob", "talk", round-2))
		}
	}
}

// A JSON reply that fails its schema gets a repair carrying the errors;
// the repaired reply is published, the rejected one only kept on disk.
func TestCtlRepairSuccess(t *testing.T) {
	cfg := ctlConfig(Layer{
		ID: "review", Participants: []string{"alice"}, Instructions: "LAYER-review", Delivery: DeliveryAfterRound, MaxRounds: 1,
		Output: Output{Format: FormatJSON, Schema: "claims"},
	})
	cfg.Schemas = map[string]json.RawMessage{"claims": json.RawMessage(`{"type":"object","required":["claim"],"properties":{"claim":{"type":"string"}}}`)}
	f := ctlLaunch(t, cfg)
	f.msg.respond = func(cl ctlCall) (Reply, error) {
		if !cl.Repair {
			return Reply{Outcome: OutcomeOK, Text: `{"wrong":"REJECTED-CONTENT"}`}, nil
		}
		return Reply{Outcome: OutcomeOK, Text: "```json\n{\"claim\":\"REPAIRED\"}\n```"}, nil
	}
	_, st := f.run()
	ctlWant(t, "status", st, StatusCompleted)
	calls := f.msg.all()
	ctlWant(t, "calls", len(calls), 2)
	ctlContains(t, "first message", calls[0].Message, `"required":["claim"]`)
	ctlContains(t, "repair", calls[1].Message, headingRejected, "claim", "Send your complete reply again", `"required":["claim"]`)
	ctlLacks(t, "repair", calls[1].Message, "BRIEF-PURPOSE")
	att := f.attempts("review")
	ctlWant(t, "attempts", len(att), 2)
	if len(att[0].Reply.Issues) == 0 || len(att[1].Reply.Issues) != 0 || !att[1].Request.Repair {
		t.Fatalf("attempts: %+v", att)
	}
	res := f.result()
	data, err := f.s.ReadFile(res.Layers[0].Outputs[0].ContentFile)
	if err != nil {
		t.Fatal(err)
	}
	ctlWant(t, "stored output", string(data), `{"claim":"REPAIRED"}`)
	ctlLacks(t, "transcript", f.transcript(), "REJECTED-CONTENT")
	ctlContains(t, "transcript", f.transcript(), "REPAIRED")
}

// A single_shot participant's repair repeats the original request and
// quotes the rejected reply, since it remembers neither.
func TestCtlRepairSingleShot(t *testing.T) {
	cfg := ctlConfig(Layer{
		ID: "review", Participants: []string{"bob"}, Instructions: "LAYER-review", Delivery: DeliveryAfterRound, MaxRounds: 1,
		Inputs: []Route{{From: "source:notes"}}, Output: Output{Format: FormatJSON},
	})
	bob := cfg.Participants["bob"]
	bob.Mode = FreshModeSingleShot
	cfg.Participants["bob"] = bob
	f := ctlLaunch(t, cfg)
	f.msg.respond = func(cl ctlCall) (Reply, error) {
		if !cl.Repair {
			return Reply{Outcome: OutcomeOK, Text: `not json BAD-REPLY`}, nil
		}
		return Reply{Outcome: OutcomeOK, Text: `[1,2]`}, nil
	}
	_, st := f.run()
	ctlWant(t, "status", st, StatusCompleted)
	repair := f.msg.all()[1].Message
	ctlContains(t, "single_shot repair", repair, "BRIEF-PURPOSE", "LAYER-review", "SOURCE-NOTES", headingPreviousReply, "BAD-REPLY", headingRejected)
}

// Exhausting max_attempts_per_turn fails the run; nothing is published.
func TestCtlRepairExhausted(t *testing.T) {
	cfg := ctlConfig(Layer{
		ID: "review", Participants: []string{"alice"}, Instructions: "LAYER-review", Delivery: DeliveryPerTurn, MaxRounds: 1,
		Output: Output{Format: FormatJSON},
	})
	f := ctlLaunch(t, cfg)
	f.msg.respond = func(ctlCall) (Reply, error) { return Reply{Outcome: OutcomeOK, Text: "{broken BROKEN-REPLY"}, nil }
	_, st := f.run()
	ctlWant(t, "status", st, StatusFailed)
	res := f.result()
	ctlWant(t, "reason", res.Reason, EndAttemptsExhausted)
	ctlWant(t, "calls", res.Calls, 3)
	ctlWant(t, "complete", res.Complete, false)
	ctlLacks(t, "transcript", f.transcript(), "BROKEN-REPLY")
	ctlContains(t, "omissions", strings.Join(res.Omissions, "\n"), "layer review did not end", "round 1: no output from alice")
}

// An unsuccessful outcome (timeout, error, empty) is a rejected attempt
// whose message is sent again unchanged, not a repair.
func TestCtlUnsuccessfulOutcomeResends(t *testing.T) {
	for _, outcome := range []Outcome{OutcomeTimeout, OutcomeError, OutcomeEmpty, OutcomeCancelled} {
		t.Run(string(outcome), func(t *testing.T) {
			f := ctlLaunch(t, ctlConfig(Layer{ID: "one", Participants: []string{"alice"}, Instructions: "LAYER-one", Delivery: DeliveryAfterRound, MaxRounds: 1, Output: Output{Format: FormatText}}))
			n := 0
			f.msg.respond = func(cl ctlCall) (Reply, error) {
				n++
				if n == 1 {
					return Reply{Outcome: outcome}, nil
				}
				return f.reply(cl), nil
			}
			_, st := f.run()
			ctlWant(t, "status", st, StatusCompleted)
			calls := f.msg.all()
			ctlWant(t, "calls", len(calls), 2)
			ctlWant(t, "resent message", calls[1].Message, calls[0].Message)
			att := f.attempts("one")
			ctlWant(t, "first outcome", att[0].Reply.Outcome, outcome)
			ctlWant(t, "second is repair", att[1].Request.Repair, false)
		})
	}
}

// Every limit: the forum budget (incomplete), the layer budget (the layer
// ends call_limit and the next runs), the deadline (incomplete), the round
// limit (round_limit).
func TestCtlLimits(t *testing.T) {
	t.Run("forum max_calls", func(t *testing.T) {
		cfg := ctlConfig(ctlLayer("talk", DeliveryPerTurn, 3, FormatText))
		cfg.Limits.MaxCalls = 3
		f := ctlLaunch(t, cfg)
		_, st := f.run()
		ctlWant(t, "status", st, StatusIncomplete)
		res := f.result()
		ctlWant(t, "reason", res.Reason, EndForumCallLimit)
		ctlWant(t, "calls", res.Calls, 3)
		ctlWant(t, "outputs", len(res.Layers[0].Outputs), 3)
		ctlContains(t, "omissions", strings.Join(res.Omissions, "\n"), "round 2: no output from bob", "did not end")
	})
	t.Run("layer max_calls", func(t *testing.T) {
		l1 := ctlLayer("first", DeliveryAfterRound, 3, FormatText)
		l1.MaxCalls = 3
		l2 := ctlLayer("second", DeliveryAfterRound, 1, FormatText)
		l2.Inputs = []Route{{From: "layer:first"}}
		cfg := ctlConfig(l1, l2)
		cfg.ResultLayers = []string{"first", "second"}
		cfg.Limits.MaxParallelCalls = 1
		f := ctlLaunch(t, cfg)
		_, st := f.run()
		ctlWant(t, "status", st, StatusCompleted)
		res := f.result()
		ctlWant(t, "first reason", res.Layers[0].EndReason, EndCallLimit)
		ctlWant(t, "first calls", f.state().Layers["first"].Calls, 3)
		ctlWant(t, "published first outputs", len(res.Layers[0].Outputs), 2)
		ctlContains(t, "omissions", strings.Join(res.Omissions, "\n"), "round 2: no output from bob", "round 2: 1 committed output(s) not published")
		// The unpublished half-round is routed nowhere.
		second := f.ctlOne("alice", "second", 1, false).Message
		ctlContains(t, "second layer", second, ctlMark("bob", "first", 1))
		ctlLacks(t, "second layer", second, ctlMark("alice", "first", 2))
		ctlLacks(t, "transcript", f.transcript(), ctlMark("alice", "first", 2))
	})
	t.Run("deadline", func(t *testing.T) {
		f := ctlLaunch(t, ctlConfig(ctlLayer("talk", DeliveryPerTurn, 1, FormatText)), ctlDeadline(-time.Second))
		_, st := f.run()
		ctlWant(t, "status", st, StatusIncomplete)
		ctlWant(t, "reason", f.result().Reason, EndDeadline)
		ctlWant(t, "calls", len(f.msg.all()), 0)
	})
	t.Run("wait bounded by the deadline", func(t *testing.T) {
		f := ctlLaunch(t, ctlConfig(ctlLayer("talk", DeliveryPerTurn, 1, FormatText)), ctlDeadline(5*time.Second))
		_, st := f.run()
		ctlWant(t, "status", st, StatusCompleted)
		for _, cl := range f.msg.all() {
			if cl.Wait <= 0 || cl.Wait > 5*time.Second {
				t.Fatalf("wait %s not bounded by the deadline", cl.Wait)
			}
		}
	})
	t.Run("call timeout", func(t *testing.T) {
		f := ctlLaunch(t, ctlConfig(ctlLayer("talk", DeliveryPerTurn, 1, FormatText)))
		f.run()
		for _, cl := range f.msg.all() {
			ctlWant(t, "wait", cl.Wait, 60*time.Second)
		}
		ctlWant(t, "recorded wait", f.attempts("talk")[0].Request.WaitSeconds, 60)
	})
	t.Run("max_rounds", func(t *testing.T) {
		f := ctlLaunch(t, ctlConfig(ctlLayer("talk", DeliveryAfterRound, 2, FormatText)))
		_, st := f.run()
		ctlWant(t, "status", st, StatusCompleted)
		ctlWant(t, "reason", f.result().Layers[0].EndReason, EndRoundLimit)
		ctlWant(t, "calls", len(f.msg.all()), 4)
	})
}

// Skipped layers create nothing; an optional route from one supplies
// nothing.
func TestCtlSkippedLayer(t *testing.T) {
	off := false
	l1 := ctlLayer("skipped", DeliveryAfterRound, 1, FormatText)
	l1.Enabled = &off
	l2 := ctlLayer("used", DeliveryAfterRound, 1, FormatText)
	l2.Inputs = append(l2.Inputs, Route{From: "layer:skipped", Optional: true})
	f := ctlLaunch(t, ctlConfig(l1, l2))
	_, st := f.run()
	ctlWant(t, "status", st, StatusCompleted)
	for _, cl := range f.msg.all() {
		ctlWant(t, "layer", cl.Layer, "used")
	}
	if _, err := f.s.ReadLayerInputs("skipped"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("skipped layer inputs: %v", err)
	}
}

// Moderation: GUIDE is published to everyone before the next round and
// to the transcript; CONTINUE adds nothing; STOP ends the layer; the
// assessment and directed messages stay private, a directed message
// reaching only its addressee, once; no check after the last round.
func TestCtlModeration(t *testing.T) {
	layer := ctlLayer("debate", DeliveryAfterRound, 4, FormatText)
	layer.Moderator = &Moderator{Participant: "chair", AfterRound: 1, EveryRounds: 1, Schema: "assess", AllowDirected: true}
	cfg := ctlConfig(layer)
	cfg.Schemas = map[string]json.RawMessage{"assess": json.RawMessage(`{"type":"object","required":["score"],"properties":{"score":{"type":"integer"}}}`)}
	f := ctlLaunch(t, cfg)
	decisions := map[int]string{
		1: `{"decision":"GUIDE","reason":"REASON-1","guidance":"GUIDANCE-1","assessment":{"score":1},"directed":[{"to":"alice","text":"DIRECTED-ALICE"}]}`,
		2: `{"decision":"CONTINUE","reason":"REASON-2","guidance":null,"assessment":{"score":77}}`,
		3: `{"decision":"STOP","reason":"REASON-3","guidance":null,"assessment":{"score":3}}`,
	}
	f.msg.respond = func(cl ctlCall) (Reply, error) {
		if cl.Moderator {
			return Reply{Outcome: OutcomeOK, Text: decisions[cl.Round]}, nil
		}
		return f.reply(cl), nil
	}
	_, st := f.run()
	ctlWant(t, "status", st, StatusCompleted)
	res := f.result()
	ctlWant(t, "layer reason", res.Layers[0].EndReason, EndModeratorStop)
	ctlWant(t, "calls", res.Calls, 3*2+3)
	if len(f.msg.find("alice", "debate", 4, false)) != 0 {
		t.Fatal("a round ran after STOP")
	}

	alice2 := f.ctlOne("alice", "debate", 2, false).Message
	bob2 := f.ctlOne("bob", "debate", 2, false).Message
	ctlContains(t, "alice round 2", alice2, "GUIDANCE-1", headingDirected, "DIRECTED-ALICE")
	ctlContains(t, "bob round 2", bob2, "GUIDANCE-1")
	ctlLacks(t, "bob round 2", bob2, "DIRECTED-ALICE", headingDirected)
	alice3 := f.ctlOne("alice", "debate", 3, false).Message
	ctlLacks(t, "alice round 3", alice3, "DIRECTED-ALICE", "GUIDANCE-1", "REASON-2")

	tr := f.transcript()
	ctlContains(t, "transcript", tr, "moderator chair: GUIDE", "REASON-1", "Guidance: GUIDANCE-1", "moderator chair: CONTINUE", "REASON-2", "moderator chair: STOP")
	ctlLacks(t, "transcript", tr, "DIRECTED-ALICE", `"score"`, "77", "PRIV-")
	if strings.Index(tr, "Guidance: GUIDANCE-1") > strings.Index(tr, ctlMark("alice", "debate", 2)) {
		t.Fatal("guidance published after the next round")
	}

	mod1 := f.ctlOne("chair", "debate", 1, true).Message
	ctlContains(t, "moderator first check", mod1, "BRIEF-PURPOSE", "PRIV-CHAIR", "LAYER-debate", ctlMark("alice", "debate", 1),
		ctlMark("bob", "debate", 1), `"directed"`, `"assessment"`, "Participant ids: alice, bob")
	mod2 := f.ctlOne("chair", "debate", 2, true).Message
	ctlContains(t, "moderator later check", mod2, headingConversation, ctlMark("bob", "debate", 2), `"decision"`)
	ctlLacks(t, "moderator later check", mod2, "BRIEF-PURPOSE", ctlMark("bob", "debate", 1), "GUIDANCE-1")
}

// The moderator is never consulted after the last round.
func TestCtlModeratorNotAfterLastRound(t *testing.T) {
	layer := ctlLayer("debate", DeliveryPerTurn, 2, FormatText)
	layer.Moderator = &Moderator{Participant: "chair", AfterRound: 1, EveryRounds: 1}
	f := ctlLaunch(t, ctlConfig(layer))
	_, st := f.run()
	ctlWant(t, "status", st, StatusCompleted)
	ctlWant(t, "moderator checks", len(f.msg.find("chair", "debate", 1, true)), 1)
	ctlWant(t, "moderator checks after last", len(f.msg.find("chair", "debate", 2, true)), 0)
}

// An invalid decision gets a repair; a moderator that never produces one
// fails the run.
func TestCtlModeratorRepairAndFailure(t *testing.T) {
	layer := ctlLayer("debate", DeliveryPerTurn, 2, FormatText)
	layer.Moderator = &Moderator{Participant: "chair", AfterRound: 1, EveryRounds: 1}
	t.Run("repair", func(t *testing.T) {
		f := ctlLaunch(t, ctlConfig(layer))
		f.msg.respond = func(cl ctlCall) (Reply, error) {
			switch {
			case cl.Moderator && !cl.Repair:
				return Reply{Outcome: OutcomeOK, Text: `{"decision":"GUIDE","reason":"r","guidance":null}`}, nil
			case cl.Moderator:
				return Reply{Outcome: OutcomeOK, Text: `{"decision":"GUIDE","reason":"r","guidance":"FIXED-GUIDANCE"}`}, nil
			}
			return f.reply(cl), nil
		}
		_, st := f.run()
		ctlWant(t, "status", st, StatusCompleted)
		ctlWant(t, "moderator calls", len(f.msg.find("chair", "debate", 1, true)), 2)
		ctlContains(t, "transcript", f.transcript(), "FIXED-GUIDANCE")
	})
	t.Run("failure", func(t *testing.T) {
		f := ctlLaunch(t, ctlConfig(layer))
		f.msg.respond = func(cl ctlCall) (Reply, error) {
			if cl.Moderator {
				return Reply{Outcome: OutcomeOK, Text: `{"decision":"MAYBE"}`}, nil
			}
			return f.reply(cl), nil
		}
		_, st := f.run()
		ctlWant(t, "status", st, StatusFailed)
		ctlWant(t, "reason", f.result().Reason, EndModeratorFailed)
		ctlWant(t, "calls", f.result().Calls, 2+3)
	})
}

// Host failures: an Ask error for an existing agent fails the run with
// host_error; for a created participant the host no longer has, with
// participant_gone. A participant gone before Open fails the run at once.
func TestCtlHostFailures(t *testing.T) {
	t.Run("host error", func(t *testing.T) {
		f := ctlLaunch(t, ctlConfig(ctlLayer("talk", DeliveryPerTurn, 1, FormatText)))
		f.msg.respond = func(ctlCall) (Reply, error) { return Reply{}, errors.New("transport down") }
		_, st := f.run()
		ctlWant(t, "status", st, StatusFailed)
		ctlWant(t, "reason", f.result().Reason, EndHostError)
	})
	t.Run("participant gone mid-run", func(t *testing.T) {
		f := ctlLaunch(t, ctlConfig(ctlLayer("talk", DeliveryPerTurn, 1, FormatText)))
		f.msg.respond = func(cl ctlCall) (Reply, error) {
			if cl.Participant == "bob" {
				f.agents.mu.Lock()
				f.agents.gone["tmp-bob"] = true
				f.agents.mu.Unlock()
				return Reply{}, errors.New("no such agent")
			}
			return f.reply(cl), nil
		}
		_, st := f.run()
		ctlWant(t, "status", st, StatusFailed)
		ctlWant(t, "reason", f.result().Reason, EndParticipantGone)
	})
	t.Run("participant gone at open", func(t *testing.T) {
		f := ctlLaunch(t, ctlConfig(ctlLayer("talk", DeliveryPerTurn, 1, FormatText)))
		f.agents.gone["tmp-bob"] = true
		_, st := f.run()
		ctlWant(t, "status", st, StatusFailed)
		ctlWant(t, "reason", f.result().Reason, EndParticipantGone)
		ctlWant(t, "calls", len(f.msg.all()), 0)
	})
}

// A host shutdown (ctx cancelled) leaves the forum running on disk; a
// later Open resumes it.
func TestCtlShutdownLeavesForumResumable(t *testing.T) {
	f := ctlLaunch(t, ctlConfig(ctlLayer("talk", DeliveryPerTurn, 2, FormatText)))
	ctx, cancel := context.WithCancel(context.Background())
	f.msg.hook = func(ctx context.Context, cl ctlCall) error {
		if cl.Participant == "bob" && cl.Round == 2 {
			cancel()
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}
	c := f.open()
	if _, err := c.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run after shutdown: %v", err)
	}
	ctlWant(t, "status on disk", f.state().Status, StatusRunning)
	f.msg.hook = nil
	_, st := f.run()
	ctlWant(t, "status", st, StatusCompleted)
	att := f.attempts("talk")
	ctlWant(t, "bob round 2 attempts", len(att), 4+1) // the uncertain attempt was resent and counted
}

// Running a forum twice from the same configuration and seed gives the
// same routing, messages, transcript and result.
func TestCtlDeterministic(t *testing.T) {
	src := ctlLayer("source", DeliveryAfterRound, 2, FormatText)
	dst := ctlLayer("dealt", DeliveryAfterRound, 1, FormatText)
	dst.Inputs = []Route{{From: "layer:source", Distribute: DistributeRandom}}
	seed := int64(7)
	var runs [2]struct {
		transcript string
		messages   []string
		inputs     string
		outputs    []string
	}
	for i := range runs {
		cfg := ctlConfig(src, dst)
		cfg.Seed = &seed
		cfg.Limits.MaxParallelCalls = 1
		f := ctlLaunch(t, cfg)
		_, st := f.run()
		ctlWant(t, "status", st, StatusCompleted)
		runs[i].transcript = f.transcript()
		for _, cl := range f.msg.all() {
			runs[i].messages = append(runs[i].messages, cl.Message)
		}
		in, err := f.s.ReadLayerInputs("dealt")
		if err != nil {
			t.Fatal(err)
		}
		for _, items := range in.Participants {
			for j := range items {
				items[j].OutputID = "" // derived from the forum's own UUID
			}
		}
		data := ctlJSON(t, in)
		runs[i].inputs = data
		for _, o := range f.result().Layers[0].Outputs {
			runs[i].outputs = append(runs[i].outputs, o.Turn+"/"+o.ParticipantID)
		}
	}
	ctlWant(t, "transcript", runs[0].transcript, runs[1].transcript)
	ctlWant(t, "inputs", runs[0].inputs, runs[1].inputs)
	ctlWant(t, "messages", strings.Join(runs[0].messages, "\x00"), strings.Join(runs[1].messages, "\x00"))
	ctlWant(t, "outputs", strings.Join(runs[0].outputs, ","), strings.Join(runs[1].outputs, ","))
}

// A JSON layer with share publishes only the projection: peers, the
// transcript and downstream routes see it; the author's full output is
// kept.
func TestCtlSharePublishesProjection(t *testing.T) {
	share := []string{"/claim"}
	l1 := ctlLayer("review", DeliveryPerTurn, 1, FormatJSON)
	l1.Output.Share = &share
	l2 := ctlLayer("next", DeliveryAfterRound, 1, FormatText)
	l2.Inputs = []Route{{From: "layer:review"}}
	f := ctlLaunch(t, ctlConfig(l1, l2))
	_, st := f.run()
	ctlWant(t, "status", st, StatusCompleted)
	ctlContains(t, "bob sees", f.ctlOne("bob", "review", 1, false).Message, ctlMark("alice", "review", 1))
	ctlLacks(t, "bob sees", f.ctlOne("bob", "review", 1, false).Message, "DETAIL-")
	ctlLacks(t, "transcript", f.transcript(), "DETAIL-")
	ctlLacks(t, "next layer", f.ctlOne("alice", "next", 1, false).Message, "DETAIL-")
	full, err := f.s.ReadFile(f.state().Layers["review"].Outputs[0].ContentFile)
	if err != nil {
		t.Fatal(err)
	}
	ctlContains(t, "full output", string(full), "DETAIL-")
}

// A reply missing a shared member is rejected and repaired.
func TestCtlShareMissingIsRejected(t *testing.T) {
	share := []string{"/claim"}
	l := ctlLayer("review", DeliveryPerTurn, 1, FormatJSON)
	l.Participants = []string{"alice"}
	l.Output.Share = &share
	f := ctlLaunch(t, ctlConfig(l))
	f.msg.respond = func(cl ctlCall) (Reply, error) {
		if !cl.Repair {
			return Reply{Outcome: OutcomeOK, Text: `{"other":1}`}, nil
		}
		return f.reply(cl), nil
	}
	_, st := f.run()
	ctlWant(t, "status", st, StatusCompleted)
	ctlContains(t, "issues", strings.Join(f.attempts("review")[0].Reply.Issues, ";"), "/claim")
}

func TestCtlValidateOutput(t *testing.T) {
	share := []string{"/a"}
	tests := []struct {
		name    string
		out     Output
		text    string
		want    string
		issueIn string
	}{
		{"text kept", Output{Format: FormatText}, " hi\n", " hi\n", ""},
		{"markdown kept", Output{Format: FormatMarkdown}, "# T", "# T", ""},
		{"empty", Output{Format: FormatText}, " \n", "", "empty"},
		{"json canonical", Output{Format: FormatJSON}, `{"b":1, "a":2}`, `{"a":2,"b":1}`, ""},
		{"json fenced", Output{Format: FormatJSON}, "```json\n[1]\n```", `[1]`, ""},
		{"json bare fence", Output{Format: FormatJSON}, "```\n\"s\"\n```", `"s"`, ""},
		{"json two values", Output{Format: FormatJSON}, `{} {}`, "", "one valid JSON value"},
		{"json invalid", Output{Format: FormatJSON}, `{`, "", "one valid JSON value"},
		{"share present", Output{Format: FormatJSON, Share: &share}, `{"a":1,"b":2}`, `{"a":1,"b":2}`, ""},
		{"share missing", Output{Format: FormatJSON, Share: &share}, `{"b":2}`, "", "/a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content, issues := validateOutput(tt.out, tt.text, nil)
			if tt.issueIn != "" {
				ctlContains(t, "issues", strings.Join(issues, ";"), tt.issueIn)
				return
			}
			if len(issues) > 0 {
				t.Fatalf("issues: %v", issues)
			}
			ctlWant(t, "content", string(content), tt.want)
		})
	}
}

func TestCtlParseDecision(t *testing.T) {
	layer := Layer{ID: "l", Participants: []string{"alice", "bob"}, Moderator: &Moderator{Participant: "chair", AllowDirected: true}}
	raw, err := EffectiveModeratorSchema(layer, nil)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := JSONSchemaValidator{}.Compile(raw)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		text     string
		schema   CompiledSchema
		directed bool
		want     DecisionKind
		issueIn  string
	}{
		{"continue", `{"decision":"CONTINUE","reason":"r","guidance":null}`, schema, true, DecisionContinue, ""},
		{"guide fenced", "```json\n{\"decision\":\"GUIDE\",\"reason\":\"r\",\"guidance\":\"g\"}\n```", schema, true, DecisionGuide, ""},
		{"stop", `{"decision":"STOP","reason":"r","guidance":null}`, nil, false, DecisionStop, ""},
		{"guide without guidance", `{"decision":"GUIDE","reason":"r","guidance":" "}`, nil, false, "", "nonblank"},
		{"continue with guidance", `{"decision":"CONTINUE","reason":"r","guidance":"g"}`, nil, false, "", "null"},
		{"unknown decision", `{"decision":"MAYBE","reason":"r","guidance":null}`, nil, false, "", "CONTINUE, GUIDE or STOP"},
		{"missing guidance", `{"decision":"STOP","reason":"r"}`, nil, false, "", `"guidance" is required`},
		{"unknown member", `{"decision":"STOP","reason":"r","guidance":null,"x":1}`, nil, false, "", `unknown member "x"`},
		{"directed not allowed", `{"decision":"STOP","reason":"r","guidance":null,"directed":[]}`, nil, false, "", "not allowed"},
		{"directed to stranger", `{"decision":"STOP","reason":"r","guidance":null,"directed":[{"to":"stranger","text":"t"}]}`, nil, true, "", "not a participant"},
		{"directed empty text", `{"decision":"STOP","reason":"r","guidance":null,"directed":[{"to":"bob","text":""}]}`, nil, true, "", "text is empty"},
		{"schema violation", `{"decision":"STOP","reason":5,"guidance":null}`, schema, true, "", "reason"},
		{"wrong type", `{"decision":"STOP","reason":5,"guidance":null}`, nil, false, "", "wrong type"},
		{"not an object", `[1]`, nil, false, "", "JSON object"},
		{"not json", `STOP`, nil, false, "", "valid JSON"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, issues := parseDecision(tt.text, tt.schema, tt.directed, layer.Participants)
			if tt.issueIn != "" {
				ctlContains(t, "issues", strings.Join(issues, ";"), tt.issueIn)
				if d != nil {
					t.Fatal("decision returned with issues")
				}
				return
			}
			if len(issues) > 0 {
				t.Fatalf("issues: %v", issues)
			}
			ctlWant(t, "decision", d.Decision, tt.want)
		})
	}
}

func TestCtlFence(t *testing.T) {
	got := fence("text", "a ``` b ```` c")
	if !strings.HasPrefix(got, "`````text\n") || !strings.HasSuffix(got, "\n`````") {
		t.Fatalf("fence did not outgrow the content's backticks: %q", got)
	}
}

func TestCtlModeratorDue(t *testing.T) {
	m := &Moderator{AfterRound: 2, EveryRounds: 3}
	var due []int
	for r := 1; r <= 10; r++ {
		if moderatorDue(m, r) {
			due = append(due, r)
		}
	}
	data := ctlJSON(t, due)
	ctlWant(t, "due rounds", data, "[2,5,8]")
	ctlWant(t, "nil moderator", moderatorDue(nil, 1), false)
}

// The live controller folds every commit with replayApply, so its state
// equals Replay of the log at the end of every run.
func TestCtlStateMatchesReplay(t *testing.T) {
	layer := ctlLayer("debate", DeliveryAfterRound, 3, FormatText)
	layer.Moderator = &Moderator{Participant: "chair", AfterRound: 1, EveryRounds: 1}
	f := ctlLaunch(t, ctlConfig(layer))
	c, _ := f.run()
	live := c.State()
	replayed := f.state()
	a := ctlJSON(t, live)
	b := ctlJSON(t, replayed)
	ctlWant(t, "state", a, b)
	cached, err := f.s.ReadState()
	if err != nil {
		t.Fatal(err)
	}
	cj := ctlJSON(t, cached)
	ctlWant(t, "state.json", cj, b)
}

// Every Ask carries the forum it is made for, so the host can name the
// launching agent as the sender.
func TestCtlAskCarriesForum(t *testing.T) {
	f := ctlLaunch(t, ctlConfig(ctlLayer("first", DeliveryPerTurn, 1, FormatText)), func(f *ctlForum) {
		f.snap.Origin = Origin{AgentID: "alice", Channel: "telegram", ChatID: "42"}
	})
	var seen []AskInfo
	var mu sync.Mutex
	f.msg.hook = func(ctx context.Context, _ ctlCall) error {
		info, ok := AskInfoFromContext(ctx)
		if !ok {
			t.Error("Ask context carries no forum")
		}
		mu.Lock()
		seen = append(seen, info)
		mu.Unlock()
		return nil
	}
	_, st := f.run()
	ctlWant(t, "status", st, StatusCompleted)
	if len(seen) == 0 {
		t.Fatal("no Ask was made")
	}
	for _, info := range seen {
		if info.ForumID != f.s.ID() || info.Origin.AgentID != "alice" || info.Origin.ChatID != "42" {
			t.Errorf("AskInfo = %+v, want forum %s launched by alice", info, f.s.ID())
		}
	}
	if _, ok := AskInfoFromContext(context.Background()); ok {
		t.Error("AskInfoFromContext reports a forum on a plain context")
	}
}

// ctlCouncil is answer (Alice, Bob) -> review (Alice, Bob, reading answer
// anonymously) -> verdict (the chair, reading answer by name).
func ctlCouncil() *Config {
	answer := ctlLayer("answer", DeliveryAfterRound, 1, FormatText)
	review := ctlLayer("review", DeliveryAfterRound, 1, FormatText)
	review.Inputs = []Route{{From: "layer:answer", Anonymous: true}}
	verdict := ctlLayer("verdict", DeliveryAfterRound, 1, FormatText)
	verdict.Participants = []string{"chair"}
	verdict.Inputs = []Route{{From: "layer:answer"}, {From: "layer:review"}}
	cfg := ctlConfig(answer, review, verdict)
	cfg.Limits.MaxParallelCalls = 1
	return cfg
}

// An anonymous reader sees the others' outputs as "Response <letter>" with
// no author and never its own; a named reader of the same layer sees the
// author with the letter; the transcript keeps the real names.
func TestCtlAnonymousInputs(t *testing.T) {
	f := ctlLaunch(t, ctlCouncil())
	_, st := f.run()
	ctlWant(t, "status", st, StatusCompleted)

	alice := f.ctlOne("alice", "review", 1, false).Message
	ctlContains(t, "alice review", alice, `### Response B, layer "answer", round 1`, ctlMark("bob", "answer", 1))
	ctlLacks(t, "alice review", alice, "### Bob", ctlMark("alice", "answer", 1), "Response A")
	bob := f.ctlOne("bob", "review", 1, false).Message
	ctlContains(t, "bob review", bob, `### Response A, layer "answer", round 1`, ctlMark("alice", "answer", 1))
	ctlLacks(t, "bob review", bob, "### Alice", ctlMark("bob", "answer", 1), "Response B")

	chair := f.ctlOne("chair", "verdict", 1, false).Message
	ctlContains(t, "chair verdict", chair,
		`### Alice (Response A), layer "answer", round 1`, `### Bob (Response B), layer "answer", round 1`,
		`### Alice, layer "review", round 1`, `### Bob, layer "review", round 1`)

	ctlLacks(t, "transcript", f.transcript(), "Response A", "Response B")
}

// The labels come from inputs.json: a forum interrupted inside the
// anonymous layer resends the same message after a restart.
func TestCtlAnonymousInputsSurviveRestart(t *testing.T) {
	f := ctlLaunch(t, ctlCouncil())
	ctx, cancel := context.WithCancel(context.Background())
	f.msg.hook = func(ctx context.Context, cl ctlCall) error {
		if cl.Participant == "bob" && cl.Layer == "review" {
			cancel()
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}
	if _, err := f.open().Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run after shutdown: %v", err)
	}
	f.msg.hook = nil
	_, st := f.run()
	ctlWant(t, "status", st, StatusCompleted)

	var sent []string
	for _, cl := range f.msg.all() {
		if cl.Participant == "bob" && cl.Layer == "review" {
			sent = append(sent, cl.Message)
		}
	}
	ctlWant(t, "bob review messages", len(sent), 2)
	ctlWant(t, "resent message", sent[1], sent[0])
	ctlContains(t, "bob review", sent[1], `### Response A, layer "answer", round 1`)
	ctlLacks(t, "bob review", sent[1], "### Alice")
}

// An optional anonymous input with no other author's output for a reader
// (here its producer is disabled) tells it so instead of saying nothing;
// a named optional input that gives nothing still says nothing.
func TestCtlAnonymousNoOtherResponses(t *testing.T) {
	off := false
	answer := ctlLayer("answer", DeliveryAfterRound, 1, FormatText)
	answer.Enabled = &off
	review := ctlLayer("review", DeliveryAfterRound, 1, FormatText)
	review.Inputs = []Route{{From: "layer:answer", Anonymous: true, Optional: true}}
	named := ctlLayer("named", DeliveryAfterRound, 1, FormatText)
	named.Participants = []string{"chair"}
	named.Inputs = []Route{{From: "layer:answer", Optional: true}}
	f := ctlLaunch(t, ctlConfig(answer, review, named))
	_, st := f.run()
	ctlWant(t, "status", st, StatusCompleted)

	for _, pid := range []string{"alice", "bob"} {
		msg := f.ctlOne(pid, "review", 1, false).Message
		ctlContains(t, pid+" review", msg, headingInputs, "### Responses from layer \"answer\"\n"+noOtherResponses)
	}
	ctlLacks(t, "chair", f.ctlOne("chair", "named", 1, false).Message, noOtherResponses, headingInputs)
}
