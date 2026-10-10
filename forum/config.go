// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// The configuration contract (DESIGN.md §2): the types an agent's forum.json
// decodes into. The JSON tags are the configuration's field names.
// Decoding is in decode.go, validation in validate.go.

// configVersion is the only format version this package accepts.
const configVersion = 1

// Config is one complete forum procedure.
type Config struct {
	Version      int                        `json:"version"`
	Name         string                     `json:"name,omitempty"`
	Brief        Brief                      `json:"brief"`
	Sources      map[string]Source          `json:"sources,omitempty"`
	Participants map[string]Participant     `json:"participants"`
	Schemas      map[string]json.RawMessage `json:"schemas,omitempty"`
	Layers       []Layer                    `json:"layers"`
	Limits       Limits                     `json:"limits"`
	// Seed drives random routing; nil means "generate at launch and persist
	// in snapshot.json".
	Seed *int64 `json:"seed,omitempty"`
	// ResultLayers are the layer IDs included in the result, in order; empty
	// means the last enabled layer.
	ResultLayers []string `json:"result_layers,omitempty"`
}

// Brief is shared with every participant. Secrets belong in
// selectively routed sources, never here.
type Brief struct {
	Purpose         string   `json:"purpose"`
	Task            string   `json:"task"`
	SuccessCriteria []string `json:"success_criteria,omitempty"`
	Constraints     []string `json:"constraints,omitempty"`
}

// Format is a content format: how a source is decoded or an output is kept.
type Format string

// Content formats (a source's `decode`, a layer's `output.format`).
const (
	FormatText     Format = "text"
	FormatMarkdown Format = "markdown"
	FormatJSON     Format = "json"
)

// Extension is the file extension outputs and materialised sources use.
func (f Format) Extension() string {
	switch f {
	case FormatMarkdown:
		return ".md"
	case FormatJSON:
		return ".json"
	case FormatText:
		return ".txt"
	}
	return ".txt"
}

// Source is subject matter routed into layers. Exactly one of Inline
// and File is set.
type Source struct {
	Decode Format `json:"decode"`
	// Inline is the source content: for decode text or markdown a JSON
	// string; for decode json any JSON value, materialised indented on its
	// own. nil means absent.
	Inline json.RawMessage `json:"inline,omitempty"`
	// File is a path the launching agent's file tools would read: relative
	// to its workspace, or under one of its mounts (maestro/..., a
	// configured mount); the host resolves it (preflightEnv.ResolveFile).
	// It is read once at launch and copied into sources/, so later
	// edits do not affect a running forum.
	File string `json:"file,omitempty"`
}

// ParticipantForm is which of the three participant forms an entry uses.
type ParticipantForm string

// Participant forms.
const (
	FormExisting ParticipantForm = "existing" // `agent`: the real agent
	FormClone    ParticipantForm = "clone"    // `clone`: temporary copy, deleted when the forum ends
	FormFresh    ParticipantForm = "fresh"    // `model`: temporary agent with no tools
)

// Participant is one entry of `participants`. Exactly one of Agent,
// Clone or Model (without Agent/Clone) selects the form.
type Participant struct {
	Agent string `json:"agent,omitempty"`
	Clone string `json:"clone,omitempty"`
	// Model is the fresh participant's model (one of the launching agent's
	// models), or for the clone form an optional override that must be one
	// of the source agent's models. An existing agent always runs on
	// its own model; `model` on the `agent` form is rejected.
	Model        string    `json:"model,omitempty"`
	SystemPrompt string    `json:"system_prompt,omitempty"`
	Mode         FreshMode `json:"mode,omitempty"`
	// Instructions are private to this participant and are never
	// published. Optional.
	Instructions string `json:"instructions,omitempty"`
	// Name is how the participant is attributed in the transcript; the
	// participant ID when empty.
	Name string `json:"name,omitempty"`
}

// Form returns the participant's form. It does not validate: an entry
// setting both `agent` and `clone` is reported by Validate, and Form returns
// FormExisting for it.
func (p Participant) Form() ParticipantForm {
	switch {
	case p.Agent != "":
		return FormExisting
	case p.Clone != "":
		return FormClone
	default:
		return FormFresh
	}
}

// Delivery is when a layer publishes its turns.
type Delivery string

// Delivery modes.
const (
	DeliveryAfterRound Delivery = "after_round" // turns of a round see only completed earlier rounds
	DeliveryPerTurn    Delivery = "per_turn"    // each turn is committed and published before the next is sent
)

// Layer is one ordered interaction. The array order in Config.Layers
// is the execution order; IDs are stable and never renumbered.
type Layer struct {
	ID string `json:"id"`
	// Enabled defaults to true when absent.
	Enabled *bool `json:"enabled,omitempty"`
	// Participants is the ordered, nonempty set of participant IDs; it is
	// also the turn order.
	Participants []string `json:"participants"`
	Instructions string   `json:"instructions"`
	// Inputs are the routes feeding the layer; empty means brief and
	// instructions only.
	Inputs    []Route  `json:"inputs,omitempty"`
	Delivery  Delivery `json:"delivery"`
	MaxRounds int      `json:"max_rounds"`
	// MaxCalls is this layer's optional budget: messages sent to its
	// participants and moderator, repairs included. Exhausting it ends the
	// layer with reason EndCallLimit.
	MaxCalls  int        `json:"max_calls,omitempty"`
	Output    Output     `json:"output"`
	Moderator *Moderator `json:"moderator,omitempty"`
}

// IsEnabled applies the default of true.
func (l Layer) IsEnabled() bool { return l.Enabled == nil || *l.Enabled }

// Output is a layer's output contract (DESIGN.md §7.6 for `share`).
type Output struct {
	Format Format `json:"format"`
	// Schema names an entry of Config.Schemas; JSON only.
	Schema string `json:"schema,omitempty"`
	// Share is the JSON Pointer allowlist defining the published projection
	// of a JSON output: nil (omitted) publishes the whole output, an empty
	// array publishes nothing, otherwise the named object members are
	// copied into a fresh object. Pointers must be nonempty, select object
	// members (no array traversal) and not overlap; a pointer that resolves
	// to nothing fails output validation. JSON only.
	Share *[]string `json:"share,omitempty"`
}

// Route selection, view and distribution values.
type (
	Select     string
	View       string
	Distribute string
)

// Route field values. The zero value of each means its default.
const (
	SelectAll                Select = "all"                  // default
	SelectLastPerParticipant Select = "last_per_participant" // the newest record of each author

	ViewPublished View = "published" // default: the published projection
	ViewFull      View = "full"      // the full output; requires explicit `to`

	DistributeAll             Distribute = "all"              // default: every recipient gets the whole bundle
	DistributeSameParticipant Distribute = "same_participant" // each recipient gets the records it authored; layer routes only
	DistributeRandom          Distribute = "random"           // shuffled records dealt round-robin to the recipients
)

// Route is one input of a layer or of a moderator.
type Route struct {
	// From is "source:<id>" or "layer:<id>"; a layer must be earlier in the
	// configuration (backward only).
	From   string `json:"from"`
	Select Select `json:"select,omitempty"`
	// Authors filters layer outputs by participant ID; layer routes only.
	Authors []string `json:"authors,omitempty"`
	View    View     `json:"view,omitempty"`
	// Paths is a JSON Pointer allowlist applied to the selected view; it
	// cannot widen visibility, a missing path fails, and it is rejected for
	// text and markdown producers.
	Paths []string `json:"paths,omitempty"`
	// To names the recipients; empty means every participant of the
	// consuming layer. A moderator route has no To.
	To         []string   `json:"to,omitempty"`
	Distribute Distribute `json:"distribute,omitempty"`
	// Optional allows an empty selection or a disabled producer; it never
	// excuses malformed data or a projection error.
	Optional bool `json:"optional,omitempty"`
	// Anonymous shows the producing layer's outputs to the recipients as
	// "Response A", "Response B", ... without their authors, and leaves out
	// each recipient's own outputs. Layer routes only. The letter is the
	// author's position in the producing layer's participants, so it is
	// the same for every reader; every other reader of that layer sees the
	// author with the letter ("Bob (Response A)").
	Anonymous bool `json:"anonymous,omitempty"`
}

// RouteKind is the kind of a route's producer.
type RouteKind string

// Route producer kinds.
const (
	RouteFromSource RouteKind = "source"
	RouteFromLayer  RouteKind = "layer"
)

// Producer splits From into its kind and ID. It fails on any other shape.
func (r Route) Producer() (RouteKind, string, error) {
	kind, id, ok := strings.Cut(r.From, ":")
	if !ok || id == "" {
		return "", "", fmt.Errorf("route from %q: want \"source:<id>\" or \"layer:<id>\"", r.From)
	}
	switch RouteKind(kind) {
	case RouteFromSource, RouteFromLayer:
		return RouteKind(kind), id, nil
	}
	return "", "", fmt.Errorf("route from %q: unknown producer kind %q", r.From, kind)
}

// ConversationView is how much of the layer's conversation the moderator
// is shown.
type ConversationView string

// Conversation views.
const (
	ConversationViewPublished ConversationView = "published" // default: the published conversation
	ConversationViewFull      ConversationView = "full"      // full current-layer outputs
)

// Moderator configures a layer's moderator. Its
// participant must not be one of the layer's participants.
type Moderator struct {
	Participant string `json:"participant"`
	// AfterRound is the first round after which the moderator is consulted;
	// EveryRounds the interval after that. Both are positive. Checks happen
	// after rounds after_round + k*every_rounds while below max_rounds.
	AfterRound       int              `json:"after_round"`
	EveryRounds      int              `json:"every_rounds"`
	Inputs           []Route          `json:"inputs,omitempty"`
	ConversationView ConversationView `json:"conversation_view,omitempty"`
	// Schema names an entry of Config.Schemas for the decision's required
	// `assessment` property; it never redefines the engine-owned fields.
	Schema string `json:"schema,omitempty"`
	// AllowDirected adds the optional engine-owned `directed` array to the
	// effective decision schema, letting the moderator address the layer's
	// participants individually (directed messages).
	AllowDirected bool `json:"allow_directed,omitempty"`
}

// Limits protect against a runaway forum. All five are required and
// positive; the install's maximums (Ceilings, Service option WithCeilings)
// are enforced by runPreflight.
type Limits struct {
	// MaxCalls is the forum's total budget: every message sent to a
	// participant, repairs and moderator checks included.
	MaxCalls           int `json:"max_calls"`
	MaxDurationSeconds int `json:"max_duration_seconds"`
	CallTimeoutSeconds int `json:"call_timeout_seconds"`
	// MaxAttemptsPerTurn includes the initial attempt; it applies to each
	// moderator checkpoint as well.
	MaxAttemptsPerTurn int `json:"max_attempts_per_turn"`
	MaxParallelCalls   int `json:"max_parallel_calls"`
}

// Ceilings are the install's maximums on four of a configuration's limits
// (and on every layer's max_calls). A zero field is no ceiling.
type Ceilings struct {
	MaxCalls           int `json:"max_calls"`
	MaxDurationSeconds int `json:"max_duration_seconds"`
	CallTimeoutSeconds int `json:"call_timeout_seconds"`
	MaxParallelCalls   int `json:"max_parallel_calls"`
}

// DecisionKind is the engine-owned moderator verdict.
type DecisionKind string

// Moderator decisions.
const (
	DecisionContinue DecisionKind = "CONTINUE"
	DecisionGuide    DecisionKind = "GUIDE" // publishes Guidance to all participants before the next round
	DecisionStop     DecisionKind = "STOP"  // ends the layer
)

// Decision is the moderator's JSON reply. Decision, Reason,
// Guidance and Directed are engine-owned; Assessment follows
// Moderator.Schema. CONTINUE and STOP require null guidance, GUIDE a
// nonempty one.
type Decision struct {
	Decision   DecisionKind      `json:"decision"`
	Reason     string            `json:"reason"`
	Guidance   *string           `json:"guidance"`
	Assessment json.RawMessage   `json:"assessment,omitempty"`
	Directed   []DirectedMessage `json:"directed,omitempty"`
}

// DirectedMessage is a private note from the moderator to one participant,
// delivered inside that participant's next turn of this forum and never
// written to the transcript.
type DirectedMessage struct {
	To   string `json:"to"`
	Text string `json:"text"`
}

// The accepted values of each enumerated field, in one place: validateStatic
// checks against them and the published JSON Schema lists them. An empty
// select, view, distribute, mode or conversation_view means its default.
var (
	formatValues           = []Format{FormatText, FormatMarkdown, FormatJSON}
	deliveryValues         = []Delivery{DeliveryAfterRound, DeliveryPerTurn}
	freshModeValues        = []FreshMode{FreshModeMemory, FreshModeNoMemory, FreshModeSingleShot}
	selectValues           = []Select{SelectAll, SelectLastPerParticipant}
	viewValues             = []View{ViewPublished, ViewFull}
	distributeValues       = []Distribute{DistributeAll, DistributeSameParticipant, DistributeRandom}
	conversationViewValues = []ConversationView{ConversationViewPublished, ConversationViewFull}
)

// minPositive is the smallest value of every limit, of a layer's max_rounds
// and of a moderator's after_round and every_rounds (validateStatic and the
// published schema).
const minPositive = 1

// valueList renders accepted values for a message: "a, b, c".
func valueList[T ~string](values []T) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = string(v)
	}
	return strings.Join(parts, ", ")
}

// configIDPattern is the configuration ID syntax: letters, digits,
// underscore and hyphen, not starting with a digit.
var configIDPattern = regexp.MustCompile(`^[A-Za-z_-][A-Za-z0-9_-]*$`)

// validID reports whether id is a well-formed configuration ID.
func validID(id string) bool { return configIDPattern.MatchString(id) }

// EnabledLayers returns the enabled layers in execution order.
func (c *Config) EnabledLayers() []Layer {
	out := make([]Layer, 0, len(c.Layers))
	for _, l := range c.Layers {
		if l.IsEnabled() {
			out = append(out, l)
		}
	}
	return out
}

// Layer returns the layer with this ID and whether it exists.
func (c *Config) Layer(id string) (Layer, bool) {
	for _, l := range c.Layers {
		if l.ID == id {
			return l, true
		}
	}
	return Layer{}, false
}

// EffectiveResultLayers applies the default: ResultLayers when set,
// otherwise the last enabled layer. It returns nil when no layer is enabled.
func (c *Config) EffectiveResultLayers() []string {
	if len(c.ResultLayers) > 0 {
		return c.ResultLayers
	}
	enabled := c.EnabledLayers()
	if len(enabled) == 0 {
		return nil
	}
	return []string{enabled[len(enabled)-1].ID}
}

// effectiveModeratorSchema builds the decision schema the moderator of
// layer is held to: a closed object with required `decision`
// (enum CONTINUE, GUIDE, STOP), `reason` (string) and `guidance` (string
// or null); a required `assessment` holding assessment (the schema named
// by Moderator.Schema) when that is set; and, when AllowDirected is set,
// an optional `directed` array of objects with required `to` (enum of the
// layer's participants) and nonempty `text`. The result is stored in
// Snapshot.ModeratorSchemas and sent with every moderator request. It
// fails only if assessment is not a JSON object.
//
// The guidance rule is expressed in the schema itself (if decision is
// GUIDE then guidance is a string with a non-space character, else null),
// so a validator enforces it; guidanceIssue states the same rule in Go. An assessment schema without its own `$id` gets
// assessmentSchemaID, so its internal references ("#/$defs/...") resolve
// within it rather than against the decision schema's root.
func effectiveModeratorSchema(layer Layer, assessment json.RawMessage) (json.RawMessage, error) {
	if layer.Moderator == nil {
		return nil, fmt.Errorf("layer %q has no moderator", layer.ID)
	}
	required := []string{"decision", "reason", "guidance"}
	properties := map[string]any{
		"decision": map[string]any{"enum": []DecisionKind{DecisionContinue, DecisionGuide, DecisionStop}},
		"reason":   map[string]any{"type": "string"},
		"guidance": map[string]any{"type": []string{"string", "null"}},
	}
	if assessment != nil {
		var members map[string]json.RawMessage
		if err := json.Unmarshal(assessment, &members); err != nil || members == nil {
			return nil, fmt.Errorf("layer %q: moderator schema %q is not a JSON object", layer.ID, layer.Moderator.Schema)
		}
		if _, ok := members["$id"]; !ok {
			members["$id"] = json.RawMessage(`"` + assessmentSchemaID + `"`)
		}
		properties["assessment"] = members
		required = append(required, "assessment")
	}
	if layer.Moderator.AllowDirected {
		to := append([]string{}, layer.Participants...)
		properties["directed"] = map[string]any{
			"type": "array",
			"items": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"to", "text"},
				"properties": map[string]any{
					"to":   map[string]any{"enum": to},
					"text": map[string]any{"type": "string", "minLength": 1},
				},
			},
		}
	}
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             required,
		"properties":           properties,
		"if": map[string]any{
			"required":   []string{"decision"},
			"properties": map[string]any{"decision": map[string]any{"const": DecisionGuide}},
		},
		"then": map[string]any{"properties": map[string]any{"guidance": map[string]any{"type": "string", "pattern": guidancePattern}}},
		"else": map[string]any{"properties": map[string]any{"guidance": map[string]any{"type": "null"}}},
	}
	out, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("layer %q: build moderator schema: %w", layer.ID, err)
	}
	return out, nil
}

// guidancePattern is the effective schema's rule for GUIDE guidance: at
// least one non-space character (blank guidance guides nobody).
const guidancePattern = `\S`

// guidanceIssue checks the engine-owned guidance rule on a parsed
// decision and returns the problem, or "" when it holds: GUIDE needs
// guidance that is not blank after strings.TrimSpace; CONTINUE and STOP
// need it null. It is the Go form of the rule the effective schema
// carries (guidancePattern), for parseDecision.
func guidanceIssue(d *Decision) string {
	switch d.Decision {
	case DecisionGuide:
		if d.Guidance == nil || strings.TrimSpace(*d.Guidance) == "" {
			return "decision GUIDE requires nonblank guidance"
		}
	case DecisionContinue, DecisionStop:
		if d.Guidance != nil {
			return fmt.Sprintf("decision %s requires guidance to be null", d.Decision)
		}
	}
	return ""
}

// assessmentSchemaID is the base URI given to an embedded assessment
// schema that has none, so its internal references stay internal.
const assessmentSchemaID = "forum:///assessment.json"
