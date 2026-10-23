// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// cardsFake is a fake Cards: cards by ID; err makes every Render fail.
type cardsFake struct {
	cards map[string]CardInfo
	err   error
}

func (c *cardsFake) Render(_ context.Context, id string) (CardInfo, error) {
	if c.err != nil {
		return CardInfo{}, c.err
	}
	info, ok := c.cards[id]
	if !ok {
		return CardInfo{}, fmt.Errorf("%w: %q", ErrCardNotFound, id)
	}
	return info, nil
}

// cardsLibrary holds two cards: "sage" (nickname Sage) and "bobby".
func cardsLibrary() *cardsFake {
	return &cardsFake{cards: map[string]CardInfo{
		"sage":  {Name: "Sage", Prompt: "# Character\n\nYou are Sage."},
		"bobby": {Name: "Bobby", Prompt: "# Character\n\nYou are Bobby."},
	}}
}

func TestValidateCharacterCardForms(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		refused bool
	}{
		{"existing agent", "alice", true},
		{"clone", "bob", false},
		{"fresh", "editor", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := cfgtExample(t)
			p := cfg.Participants[tt.id]
			p.CharacterCard = "sage"
			cfg.Participants[tt.id] = p
			err := validateStatic(cfg)
			if tt.refused {
				cfgtWantIssue(t, err, "participants."+tt.id+".character_card",
					"character_card applies only to a fresh or clone participant; an existing agent uses its own card")
				return
			}
			if err != nil {
				t.Fatalf("validateStatic: %v", err)
			}
		})
	}
}

// cardsPreflight runs validateStatic and runPreflight on the example with
// the cards given per participant ID.
func cardsPreflight(t *testing.T, cards Cards, set map[string]string, names map[string]string) (*resolvedConfig, error) {
	t.Helper()
	cfg := cfgtExample(t)
	for id, card := range set {
		p := cfg.Participants[id]
		p.CharacterCard = card
		cfg.Participants[id] = p
	}
	for id, name := range names {
		p := cfg.Participants[id]
		p.Name = name
		cfg.Participants[id] = p
	}
	if err := validateStatic(cfg); err != nil {
		t.Fatalf("validateStatic: %v", err)
	}
	env := cfgtEnv(cfgtNewAgents())
	env.Cards = cards
	return runPreflight(context.Background(), cfg, env)
}

func TestPreflightRendersCards(t *testing.T) {
	res, err := cardsPreflight(t, cardsLibrary(), map[string]string{"bob": "bobby", "editor": "sage"}, nil)
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if len(res.Cards) != 2 || res.Cards["bob"].Name != "Bobby" || res.Cards["editor"].Prompt != "# Character\n\nYou are Sage." {
		t.Errorf("Cards = %+v", res.Cards)
	}
}

func TestPreflightCardIssues(t *testing.T) {
	tests := []struct {
		name  string
		cards Cards
		set   map[string]string
		names map[string]string
		path  string
		want  string
	}{
		{
			"missing card", cardsLibrary(),
			map[string]string{"editor": "nobody"},
			nil, "participants.editor.character_card", `character card "nobody" does not exist`,
		},
		{
			"no cards on the host", nil,
			map[string]string{"bob": "bobby"},
			nil, "participants.bob.character_card", "character cards are not available on this host",
		},
		{
			"two card names", cardsLibrary(),
			map[string]string{"chair": "sage", "editor": "sage"},
			nil, "participants.editor.character_card", `participants "chair" and "editor" are both named "Sage"`,
		},
		{
			"card name and configured name", cardsLibrary(),
			map[string]string{"editor": "sage"},
			map[string]string{"chair": "sage"},
			"participants.editor.character_card", `participants "chair" and "editor" are both named "Sage"`,
		},
		{
			"configured name after a card name", cardsLibrary(),
			map[string]string{"chair": "bobby"},
			map[string]string{"editor": "BOBBY"},
			"participants.editor.name", `participants "chair" and "editor" are both named "BOBBY"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := cardsPreflight(t, tt.cards, tt.set, tt.names)
			cfgtWantIssue(t, err, tt.path, tt.want)
		})
	}
}

func TestPreflightDistinctCardNamesPass(t *testing.T) {
	// A configured name wins over the card's, so two participants on one
	// card are fine once one of them is named.
	_, err := cardsPreflight(t, cardsLibrary(), map[string]string{"chair": "sage", "editor": "sage"}, map[string]string{"editor": "Editor"})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
}

func TestPreflightCardHostError(t *testing.T) {
	boom := errors.New("library unreadable")
	_, err := cardsPreflight(t, &cardsFake{err: boom}, map[string]string{"editor": "sage"}, nil)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the host's error", err)
	}
	if ve, ok := errors.AsType[*ValidationError](err); ok {
		t.Fatalf("a host failure is not a validation issue: %v", ve)
	}
}

const cardsLaunchJSON = `{
  "version": 1, "name": "svc-cards",
  "brief": {"purpose": "Test character cards.", "task": "Answer briefly."},
  "participants": {
    "alice":  {"agent": "alice"},
    "bob":    {"clone": "bob", "character_card": "bobby"},
    "chair":  {"model": "default", "character_card": "sage", "system_prompt": "  You chair a review.  "},
    "editor": {"model": "default", "character_card": "sage", "name": "Editor"},
    "plain":  {"model": "default", "system_prompt": "Be brief."}
  },
  "limits": {"max_calls": 10, "max_duration_seconds": 600,
    "call_timeout_seconds": 60, "max_attempts_per_turn": 2, "max_parallel_calls": 2},
  "layers": [
    {"id": "talk", "participants": ["alice", "bob", "chair", "editor", "plain"], "instructions": "Say hello.",
     "delivery": "after_round", "max_rounds": 1, "output": {"format": "text"}}
  ]
}`

func TestSvcLaunchWithCharacterCards(t *testing.T) {
	e := svcSetup(t)
	e.cards = cardsLibrary()
	e.restart()
	id, _ := e.launch(cardsLaunchJSON)
	parts, err := e.store(id).ReadParticipants()
	if err != nil {
		t.Fatal(err)
	}
	sage := "# Character\n\nYou are Sage."
	want := map[string]struct{ name, card, prompt string }{
		"alice":  {"alice", "", ""},
		"bob":    {"Bobby", "bobby", "# Character\n\nYou are Bobby."},
		"chair":  {"Sage", "sage", sage},
		"editor": {"Editor", "sage", sage},
		"plain":  {"plain", "", ""},
	}
	for pid, w := range want {
		rec := parts.Participants[pid]
		if rec.Name != w.name || rec.CharacterCard != w.card || rec.CardPrompt != w.prompt {
			t.Errorf("%s = %+v, want name %q card %q prompt %q", pid, rec, w.name, w.card, w.prompt)
		}
	}
	if len(e.agents.clones) != 1 || e.agents.clones[0].CardPrompt != "# Character\n\nYou are Bobby." {
		t.Errorf("clones = %+v", e.agents.clones)
	}
	prompts := map[string]bool{}
	for _, f := range e.agents.freshes {
		prompts[f.SystemPrompt] = true
	}
	for _, p := range []string{sage + "\n\n# Instructions\n\nYou chair a review.", sage, "Be brief."} {
		if !prompts[p] {
			t.Errorf("no fresh participant got the prompt %q; got %v", p, prompts)
		}
	}
}

func TestFreshPrompt(t *testing.T) {
	tests := []struct {
		name, card, system, want string
	}{
		{"neither", "", "", ""},
		{"system prompt only", "", "Be brief.", "Be brief."},
		{"card only", "CARD", "", "CARD"},
		{"card and blank system prompt", "CARD", "  \n", "CARD"},
		{"card and system prompt", "CARD", "\n Be brief. \n", "CARD\n\n# Instructions\n\nBe brief."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := freshPrompt(tt.card, tt.system); got != tt.want {
				t.Errorf("freshPrompt = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRouterNamesFromParticipants(t *testing.T) {
	cfg := &Config{Participants: map[string]Participant{
		"bob":    {Clone: "bob", CharacterCard: "bobby"},
		"alice":  {Agent: "alice", Name: "Alice"},
		"editor": {Model: "default"},
	}}
	parts := &Participants{Participants: map[string]ParticipantRecord{"bob": {ID: "bob", Name: "Bobby"}}}
	r := newRouter(cfg, &Snapshot{}, parts, nil)
	for id, want := range map[string]string{"bob": "Bobby", "alice": "Alice", "editor": "editor"} {
		if got := r.participantName(id); got != want {
			t.Errorf("participantName(%s) = %q, want %q", id, got, want)
		}
	}
}
