// ClawEh
// License: MIT

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/PivotLLM/ClawEh/ccv3"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/audit"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/utils"
)

// The character-card library (<CLAW_HOME>/cards, ccv3) for the Cards page
// and the Agents page's card selector. Every write is audited (card_import,
// card_delete); the ccv3 tool's writes are audited as tool calls.

// cardSummary is one card as the Cards page lists it.
type cardSummary struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Nickname         string   `json:"nickname,omitempty"`
	Tags             []string `json:"tags"`
	Creator          string   `json:"creator,omitempty"`
	ModificationDate int64    `json:"modification_date"`
	// AttachedTo lists the configured agents (ids as configured) whose
	// character_card names this card.
	AttachedTo []string `json:"attached_to"`
}

type cardsResponse struct {
	Cards   []cardSummary  `json:"cards"`
	Invalid []ccv3.Invalid `json:"invalid"`
}

func (h *Handler) registerCardRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/cards", h.handleListCards)
	mux.HandleFunc("GET /api/cards/{id}", h.handleGetCard)
	mux.HandleFunc("POST /api/cards/import", h.handleImportCard)
	mux.HandleFunc("DELETE /api/cards/{id}", h.handleDeleteCard)
}

// cardStore opens the library of the live configuration.
func (h *Handler) cardStore() (*config.Config, *ccv3.Store, error) {
	cfg, err := h.currentConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("load config: %w", err)
	}
	store, err := ccv3.Open(cfg.CardsPath())
	if err != nil {
		return nil, nil, fmt.Errorf("open the card library: %w", err)
	}
	return cfg, store, nil
}

// cardAttachments lists the configured agents whose character_card is id.
func cardAttachments(cfg *config.Config, id string) []string {
	out := []string{}
	for i := range cfg.Agents.List {
		a := &cfg.Agents.List[i]
		if a.CharacterCard != "" && ccv3.NormalizeID(a.CharacterCard) == ccv3.NormalizeID(id) {
			out = append(out, a.ID)
		}
	}
	return out
}

// attachedRefusal is the sentence refusing a change to a card some agent
// uses: "Card "x" is attached to agent Alice; detach it first.".
func attachedRefusal(cfg *config.Config, id string, agents []string) string {
	names := make([]string, 0, len(agents))
	for _, a := range agents {
		name := a
		if ac := cfg.AgentByID(a); ac != nil {
			name = ac.DisplayName()
		}
		names = append(names, name)
	}
	noun := "agent"
	if len(names) > 1 {
		noun = "agents"
	}
	return fmt.Sprintf("Card %q is attached to %s %s; detach it first.", id, noun, strings.Join(names, ", "))
}

// GET /api/cards
func (h *Handler) handleListCards(w http.ResponseWriter, _ *http.Request) {
	cfg, store, err := h.cardStore()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	cards, invalid, err := store.List()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "list the card library: "+err.Error())
		return
	}
	out := cardsResponse{Cards: make([]cardSummary, 0, len(cards)), Invalid: invalid}
	if out.Invalid == nil {
		out.Invalid = []ccv3.Invalid{}
	}
	for _, c := range cards {
		tags := c.Tags
		if tags == nil {
			tags = []string{}
		}
		out.Cards = append(out.Cards, cardSummary{
			ID: c.ID, Name: c.Name, Nickname: c.Nickname, Tags: tags, Creator: c.Creator,
			ModificationDate: c.ModificationDate, AttachedTo: cardAttachments(cfg, c.ID),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /api/cards/{id}: the stored envelope, as a download named <id>.json.
func (h *Handler) handleGetCard(w http.ResponseWriter, r *http.Request) {
	_, store, err := h.cardStore()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	id := r.PathValue("id")
	card, err := store.Get(id)
	if err != nil {
		writeCardError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", ccv3.NormalizeID(id)+".json"))
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(card); err != nil {
		logger.WarnCF("api", "response write failed", map[string]any{"error": err.Error()})
	}
}

// POST /api/cards/import: multipart with `file` (a V2 or V3 card as JSON or
// PNG), `id` (the card id) and optional `replace` ("true" to overwrite).
func (h *Handler) handleImportCard(w http.ResponseWriter, r *http.Request) {
	_, store, err := h.cardStore()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if perr := r.ParseMultipartForm(ccv3.MaxImportBytes); perr != nil { //nolint:gosec // the file part is capped at MaxImportBytes by the LimitReader below; maxMemory only bounds buffering
		writeJSONError(w, http.StatusBadRequest, "Invalid multipart form: "+perr.Error())
		return
	}
	id := strings.TrimSpace(r.FormValue("id"))
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "id is required")
		return
	}
	replace := r.FormValue("replace") == "true"
	file, _, err := r.FormFile("file")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "file is required")
		return
	}
	defer utils.CloseQuietly(file)
	raw, err := io.ReadAll(io.LimitReader(file, ccv3.MaxImportBytes+1))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "Failed to read file: "+err.Error())
		return
	}
	if len(raw) > ccv3.MaxImportBytes {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("The file is larger than %d MiB.", ccv3.MaxImportBytes>>20))
		return
	}
	// Replacing a card an agent uses is fine: the agent reads the new one
	// on its next turn.
	card, format, err := store.Import(id, raw, replace)
	if err != nil {
		writeCardError(w, err)
		return
	}
	recordCardWrite(r, audit.KindCardImport, id, map[string]any{"format": string(format), "replace": replace, "bytes": len(raw)})
	logger.InfoCF("ccv3", "Character card imported", map[string]any{
		"actor": audit.ActorFromContext(r.Context()), "card": ccv3.NormalizeID(id), "format": string(format), "replace": replace, "bytes": len(raw),
	})
	writeJSON(w, http.StatusOK, map[string]any{"id": ccv3.NormalizeID(id), "format": format, "card": card})
}

// DELETE /api/cards/{id}: refused while a configured agent uses the card.
func (h *Handler) handleDeleteCard(w http.ResponseWriter, r *http.Request) {
	cfg, store, err := h.cardStore()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	id := r.PathValue("id")
	if agents := cardAttachments(cfg, id); len(agents) > 0 {
		writeJSONError(w, http.StatusConflict, attachedRefusal(cfg, id, agents))
		return
	}
	if err := store.Delete(id); err != nil {
		writeCardError(w, err)
		return
	}
	recordCardWrite(r, audit.KindCardDelete, id, nil)
	logger.InfoCF("ccv3", "Character card deleted", map[string]any{
		"actor": audit.ActorFromContext(r.Context()), "card": ccv3.NormalizeID(id),
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// writeCardError maps a library error to a status: a missing card is 404, an
// existing one 409, every other refusal (invalid id, invalid card, too
// large, not a card) 400 with the library's sentence, anything else 500.
func writeCardError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ccv3.ErrNotFound):
		writeJSONError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ccv3.ErrExists):
		writeJSONError(w, http.StatusConflict, err.Error())
	case isCardRefusal(err):
		writeJSONError(w, http.StatusBadRequest, err.Error())
	default:
		writeJSONError(w, http.StatusInternalServerError, err.Error())
	}
}

func isCardRefusal(err error) bool {
	var r interface{ Refusal() bool }
	return errors.As(err, &r) && r.Refusal()
}

// recordCardWrite writes the audit row of a card import or delete: who, from
// where, which card. Card contents are never recorded.
func recordCardWrite(r *http.Request, kind, id string, details map[string]any) {
	store := audit.Default()
	if store == nil {
		return
	}
	var detail string
	if details != nil {
		if b, err := json.Marshal(details); err == nil {
			detail = string(b)
		}
	}
	store.Record(audit.Event{
		Kind:    kind,
		Actor:   audit.ActorFromContext(r.Context()),
		Sender:  clientIP(r),
		Summary: ccv3.NormalizeID(id),
		Details: detail,
		Outcome: audit.OutcomeOK,
	})
}

// agentCards maps each configured agent (by identity) to its character card.
func agentCards(cfg *config.Config) map[string]string {
	out := map[string]string{}
	for i := range cfg.Agents.List {
		a := &cfg.Agents.List[i]
		out[config.NormalizeAgentID(a.ID)] = ccv3.NormalizeID(a.CharacterCard)
	}
	return out
}

// missingCardsIntroduced lists, for a save of the configuration, each agent
// whose character_card the save sets or changes to a card the library
// (cardsDir) does not have. A card already missing in the file does not block
// an unrelated save (the Agents page and Check Up show it), like a dangling
// model.
func missingCardsIntroduced(before map[string]string, cardsDir string, next *config.Config) []string {
	var out []string
	var store *ccv3.Store
	for i := range next.Agents.List {
		a := &next.Agents.List[i]
		if a.CharacterCard == "" {
			continue
		}
		id := ccv3.NormalizeID(a.CharacterCard)
		if before[config.NormalizeAgentID(a.ID)] == id {
			continue
		}
		if store == nil {
			s, err := ccv3.Open(cardsDir)
			if err != nil {
				out = append(out, fmt.Sprintf("%s: the character card library cannot be opened: %v", a.DisplayName(), err))
				return out
			}
			store = s
		}
		if !store.Exists(id) {
			out = append(out, fmt.Sprintf("%s: character card %q does not exist.", a.DisplayName(), a.CharacterCard))
		}
	}
	return out
}
