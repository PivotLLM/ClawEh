// ClawEh
// License: MIT

package agent

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/PivotLLM/ClawEh/agentreg"
	"github.com/PivotLLM/ClawEh/ccv3"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
)

// Character cards in an agent's prompt. The library is
// <CLAW_HOME>/cards/<id>.json (config.CardsPath, opened through ccv3.Open);
// a configured agent names a card with agents.list[].character_card, and a
// clone made with a card of its own carries the rendered text
// (agentreg.Spec.CardPrompt). Fresh temporary agents are not handled here:
// their whole prompt is composed by their creator (agent_services.go).

// ErrCardNotFound is returned when a temporary agent is created on a
// character card that does not exist.
var ErrCardNotFound = errors.New("character card does not exist")

// cardStore is the library of cfg.
func cardStore(cfg *config.Config) (*ccv3.Store, error) {
	return ccv3.Open(cfg.CardsPath())
}

// cardExists reports whether card id is in the library of cfg.
func cardExists(cfg *config.Config, id string) bool {
	store, err := cardStore(cfg)
	return err == nil && store.Exists(id)
}

// renderCard renders card id from the library for a temporary agent created
// with it. ErrCardNotFound names a card that does not exist.
func renderCard(cfg *config.Config, id string) (string, error) {
	store, err := cardStore(cfg)
	if err != nil {
		return "", err
	}
	text, err := store.Render(id)
	if errors.Is(err, ccv3.ErrNotFound) {
		return "", fmt.Errorf("%w: %q", ErrCardNotFound, id)
	}
	return text, err
}

// withCharacterCard adds the agent's character card to its context builder:
// a clone's own rendered card, else the card the configuration names,
// rendered from the library whenever the static prompt is built and tracked
// by its file for cache invalidation. A configured card that does not exist
// logs one WARN at build and leaves the agent without it; the Agents page
// and Check Up show the missing card.
func withCharacterCard(cb *ContextBuilder, spec agentreg.Spec, agentCfg *config.AgentConfig, cfg *config.Config) *ContextBuilder {
	if spec.CardPrompt != "" {
		return cb.WithCardPrompt(spec.CardPrompt)
	}
	if agentCfg == nil || agentCfg.CharacterCard == "" {
		return cb
	}
	id := agentCfg.CharacterCard
	if !cardExists(cfg, id) {
		logger.WarnCF("agent", "Character card does not exist; the agent runs without it",
			map[string]any{"agent": spec.Label(), "card": id})
		return cb
	}
	path := filepath.Join(cfg.CardsPath(), ccv3.NormalizeID(id)+".json")
	return cb.WithCharacterCard(path, func() (string, error) { return renderCard(cfg, id) })
}
