// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

// Package ccv3 is the character-card library: Character Card V3 documents,
// their validation, the one rule that renders a card into a system prompt,
// the import of V2 and V3 cards (JSON or PNG) and the on-disk store.
//
// It imports only the standard library and internal/mergepatch, so every
// part of ClawEh (the tool, the API, the forum host, the agent builder) can
// use it without an import cycle.
//
// A card's data is kept as raw JSON (Card.Data), never round-tripped through
// a Go struct: member order, extensions, assets and the unknown keys an
// import preserves are written back exactly as they were. The typed view
// (cardData) is decoded only to read the fields Render and List need.
package ccv3

import (
	"encoding/json"
	"fmt"
	"strings"
)

// The envelope a stored card carries.
const (
	SpecV3        = "chara_card_v3"
	SpecVersionV3 = "3.0"
	specV2        = "chara_card_v2"
)

// Size limits. MaxCardBytes caps a stored card (the file as written) and the
// card JSON taken from an import; MaxImportBytes caps the bytes handed to
// Import, which for a PNG card include the image.
const (
	MaxCardBytes   = 1 << 20
	MaxImportBytes = 8 << 20
)

// Card is a CCv3 envelope. Data is the card's data object as stored.
type Card struct {
	Spec        string          `json:"spec"`
	SpecVersion string          `json:"spec_version"`
	Data        json.RawMessage `json:"data"`
}

// Summary is what List reports for one card.
type Summary struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Nickname         string   `json:"nickname,omitempty"`
	Tags             []string `json:"tags"`
	Creator          string   `json:"creator,omitempty"`
	ModificationDate int64    `json:"modification_date"`
}

// Invalid is a file in the card directory that is not a valid card.
type Invalid struct {
	File   string `json:"file"`
	Reason string `json:"reason"`
}

// cardData is the typed view of the fields Render and List read.
type cardData struct {
	Name             string         `json:"name"`
	Nickname         string         `json:"nickname"`
	Description      string         `json:"description"`
	Personality      string         `json:"personality"`
	Scenario         string         `json:"scenario"`
	MesExample       string         `json:"mes_example"`
	SystemPrompt     string         `json:"system_prompt"`
	Tags             []string       `json:"tags"`
	Creator          string         `json:"creator"`
	ModificationDate int64          `json:"modification_date"`
	CharacterBook    *characterBook `json:"character_book"`
}

type characterBook struct {
	Entries []bookEntry `json:"entries"`
}

type bookEntry struct {
	Content        string `json:"content"`
	Enabled        bool   `json:"enabled"`
	Constant       bool   `json:"constant"`
	InsertionOrder int64  `json:"insertion_order"`
}

// decodeData reads the typed view of a validated data object.
func decodeData(raw json.RawMessage) (cardData, error) {
	var d cardData
	if err := json.Unmarshal(raw, &d); err != nil {
		return cardData{}, fmt.Errorf("decode card data: %w", err)
	}
	return d, nil
}

// displayName is the name a card's character goes by: the nickname when set,
// else the name.
func (d *cardData) displayName() string {
	if strings.TrimSpace(d.Nickname) != "" {
		return d.Nickname
	}
	return d.Name
}

// sentinel is an exported error kind. Every kind is an expected refusal.
type sentinel struct{ msg string }

func (e *sentinel) Error() string { return e.msg }

// Refusal marks the error as an expected refusal (tools.IsExpectedRefusal).
func (*sentinel) Refusal() bool { return true }

// The error kinds the store returns; test with errors.Is. Each carries a
// sentence for the caller in Error() and satisfies Refusal().
var (
	ErrNotFound  error = &sentinel{"card not found"}
	ErrExists    error = &sentinel{"card already exists"}
	ErrInvalidID error = &sentinel{"invalid card id"}
	ErrTooLarge  error = &sentinel{"card too large"}
)

// refusal is a refusal sentence of one kind (nil for a refusal with no
// exported kind, such as a malformed import).
type refusal struct {
	kind error
	msg  string
}

func (e *refusal) Error() string { return e.msg }
func (e *refusal) Unwrap() error { return e.kind }

// Refusal marks the error as an expected refusal (tools.IsExpectedRefusal).
func (*refusal) Refusal() bool { return true }

func refuse(kind error, format string, args ...any) error {
	return &refusal{kind: kind, msg: fmt.Sprintf(format, args...)}
}

// Issue is one problem Validate found: the member's path inside data (such
// as "character_book.entries[0].keys") and what is wrong with it.
type Issue struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// ValidationError lists every problem with a card.
type ValidationError struct {
	Issues []Issue
}

func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Issues))
	for i, is := range e.Issues {
		parts[i] = is.Path + " " + is.Message
	}
	return "The card is not valid: " + strings.Join(parts, "; ") + "."
}

// Refusal marks the error as an expected refusal (tools.IsExpectedRefusal).
func (*ValidationError) Refusal() bool { return true }
