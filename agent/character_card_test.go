// ClawEh
// License: MIT

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/agentreg"
	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/ccv3"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/providers"
)

const testCardPrompt = "# Character\n\nYou are Bob.\n\n## Personality\nDry."

// A card replaces the assistant sentence of the identity block and sits
// between the rules and the workspace files, which still apply.
func TestCardPrompt_PlacementAndIdentity(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "SOUL.md"), []byte("Be kind."), 0o600); err != nil {
		t.Fatal(err)
	}
	plain := NewContextBuilder(ws).BuildSystemPrompt()
	if !strings.Contains(plain, "You are a helpful AI assistant.") || strings.Contains(plain, "# Character") {
		t.Fatalf("prompt without a card:\n%s", plain)
	}

	got := NewContextBuilder(ws).WithCardPrompt("  " + testCardPrompt + "\n").BuildSystemPrompt()
	if strings.Contains(got, "helpful AI assistant") {
		t.Error("the assistant sentence is still there with a card")
	}
	identity := strings.Index(got, "You are the character described below.")
	rules := strings.Index(got, "## Important Rules")
	card := strings.Index(got, testCardPrompt)
	soul := strings.Index(got, "## SOUL.md")
	if identity < 0 || rules < 0 || card < 0 || soul < 0 || identity >= rules || rules >= card || card >= soul {
		t.Errorf("order identity=%d rules=%d card=%d soul=%d in:\n%s", identity, rules, card, soul, got)
	}
}

// A clone's own card wins over its source's configured card; a configured
// card that does not exist leaves the agent without a card.
func TestWithCharacterCard_CloneOverrideAndMissing(t *testing.T) {
	cfg := &config.Config{}
	ac := &config.AgentConfig{ID: "alice", CharacterCard: "nobody"}
	spec := agentreg.Spec{ID: "alice", Config: ac, Workspace: t.TempDir()}

	cb := withCharacterCard(NewContextBuilder(spec.Workspace), spec, ac, cfg)
	if cb.hasCard() {
		t.Error("a missing card still counts as a card")
	}

	spec.CardPrompt = testCardPrompt
	cb = withCharacterCard(NewContextBuilder(spec.Workspace), spec, ac, cfg)
	if cb.cardPrompt != testCardPrompt || cb.cardPath != "" {
		t.Errorf("clone card: prompt %q path %q", cb.cardPrompt, cb.cardPath)
	}
	if got := cb.sourcePaths(); len(got) != 6 {
		t.Errorf("a clone's rendered card is not a file to watch; sourcePaths = %v", got)
	}
}

// cardsConfig is newTestConfig with a data directory, so cfg.CardsPath()
// points into the test's own temp space, plus one card, "watson".
func cardsConfig(t *testing.T) *config.Config {
	t.Helper()
	t.Setenv("CLAW_HOME", t.TempDir())
	base := newTestConfig(t)
	cfg := config.DefaultConfig()
	cfg.Agents = base.Agents
	store, err := ccv3.Open(cfg.CardsPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create("watson", json.RawMessage(`{"name":"Watson","description":"A doctor.","personality":"Loyal."}`)); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// A configured agent's card is rendered from the library into its static
// prompt, the file is a prompt source, and an edit shows on the next build.
func TestWithCharacterCard_ConfiguredAgentRendersFromLibrary(t *testing.T) {
	cfg := cardsConfig(t)
	ac := &cfg.Agents.List[0]
	ac.CharacterCard = "Watson" // the configured case does not matter
	spec := agentreg.Spec{ID: "main", Config: ac, Workspace: t.TempDir()}

	cb := newAgentContextBuilder(spec, ac, cfg)
	got := cb.BuildSystemPrompt()
	for _, want := range []string{"You are the character described below.", "# Character", "You are Watson.", "## Description\nA doctor."} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt lacks %q:\n%s", want, got)
		}
	}
	wantPath := filepath.Join(cfg.CardsPath(), "watson.json")
	if cb.cardPath != wantPath || !slices.Contains(cb.sourcePaths(), wantPath) {
		t.Errorf("card path %q not tracked (sourcePaths %v)", cb.cardPath, cb.sourcePaths())
	}

	store, err := ccv3.Open(cfg.CardsPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update("watson", json.RawMessage(`{"description":"A surgeon."}`)); err != nil {
		t.Fatal(err)
	}
	if got := cb.BuildSystemPrompt(); !strings.Contains(got, "A surgeon.") {
		t.Errorf("prompt does not follow the card edit:\n%s", got)
	}
}

// renderCard names a missing card with ErrCardNotFound.
func TestRenderCard_Missing(t *testing.T) {
	cfg := cardsConfig(t)
	if _, err := renderCard(cfg, "nobody"); !errors.Is(err, ErrCardNotFound) || !strings.Contains(err.Error(), `"nobody"`) {
		t.Errorf("renderCard(nobody) = %v, want ErrCardNotFound naming it", err)
	}
	if !cardExists(cfg, "WATSON") || cardExists(cfg, "nobody") {
		t.Error("cardExists is wrong")
	}
}

// A sub-agent spawned on a card runs with the card in its system prompt; a
// missing card is refused before any clone exists.
func TestRunSubagentTask_CharacterCard(t *testing.T) {
	restore := logger.RedirectForTest(&safeBufLoop{})
	defer restore()
	cfg := cardsConfig(t)
	cfg.Agents.List[0].Tools = []string{"*"}
	provider := &sequenceProvider{responses: []*providers.LLMResponse{{Content: "done"}}, errors: []error{nil}}
	al := mustNewAgentLoop(t, cfg, bus.NewMessageBus(), provider, nil)

	before := len(al.GetRegistry().ListTemp())
	_, release, err := al.runSubagentTask(context.Background(), "main", "do it", "", "nobody", nil)
	release()
	if !errors.Is(err, ErrCardNotFound) {
		t.Fatalf("missing card: err = %v, want ErrCardNotFound", err)
	}
	if got := len(al.GetRegistry().ListTemp()); got != before {
		t.Errorf("a refused spawn left %d temporary agents", got-before)
	}

	_, release, err = al.runSubagentTask(context.Background(), "main", "do it", "", "watson", nil)
	defer release()
	if err != nil {
		t.Fatalf("runSubagentTask with card: %v", err)
	}
	provider.mu.Lock()
	msgs := provider.lastMessages
	provider.mu.Unlock()
	if len(msgs) == 0 || msgs[0].Role != "system" || !strings.Contains(msgs[0].Content, "You are Watson.") || !strings.Contains(msgs[0].Content, "You are the character described below.") {
		t.Errorf("the worker's system prompt lacks the card:\n%v", msgs)
	}
}
