// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"maps"
	"reflect"
	"strings"
	"sync"
)

// The configuration's JSON Schema, published (as the tool's RawSchema) for
// forum_config_import's `config` and forum_config_update's `changes` so a
// model sees every field. It is generated from the Go types by reflection
// (field names, types, closed objects) plus fieldDocs (descriptions), and
// its enums and minimums are the values ValidateStatic checks against
// (formatValues, ..., minPositive), so it cannot drift from what Decode and
// ValidateStatic accept; a test fails when fieldDocs and the types disagree.
//
// Nothing is required: a configuration is built step by step and may be
// incomplete until forum_validate and forum_launch check it.
//
// It keeps to the subset of JSON Schema every function-calling provider
// accepts: no $ref/$defs (everything is inlined), no oneOf/anyOf, no
// pattern or format, and enums on strings only.

// fieldDoc documents one configuration field, keyed "<GoType>.<json name>".
type fieldDoc struct {
	desc string
	enum []string
	// min and max bound an integer field; 0 means no bound.
	min, max int
	// maxLength bounds a string field, in characters; 0 means no bound.
	maxLength int
}

func enumOf[T ~string](values []T) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = string(v)
	}
	return out
}

var (
	formats = enumOf(formatValues)

	fieldDocs = map[string]fieldDoc{
		"Config.version":       {desc: "Configuration format version; always 1", min: ConfigVersion, max: ConfigVersion},
		"Config.name":          {desc: "Optional label for the forum in status, results and notices; one line of at most 100 characters; its ID when omitted", maxLength: MaxNameChars},
		"Config.brief":         {desc: "Shared with every participant"},
		"Config.sources":       {desc: "Material routed into layers, by source ID (letters, digits, _ and -, not starting with a digit)"},
		"Config.participants":  {desc: "Participants by ID (letters, digits, _ and -, not starting with a digit); each entry is one of three forms: agent, clone or model"},
		"Config.schemas":       {desc: "JSON Schemas by ID, for json outputs (output.schema) and moderator assessments (moderator.schema); internal references only"},
		"Config.layers":        {desc: "Layers, run in this order"},
		"Config.limits":        {desc: "Hard limits for each run; all five are required"},
		"Config.seed":          {desc: "Seed for random distribution; generated at launch when omitted"},
		"Config.result_layers": {desc: "IDs of the enabled layers whose outputs are the result, in order; the last enabled layer when omitted"},

		"Brief.purpose":          {desc: "Why the forum exists"},
		"Brief.task":             {desc: "What the participants must do"},
		"Brief.success_criteria": {desc: "What a good result looks like"},
		"Brief.constraints":      {desc: "Rules every participant must follow"},

		"Source.decode": {desc: "How the source is read", enum: formats},
		"Source.inline": {desc: "The source content: a string for text and markdown, any JSON value for json. Set exactly one of inline and file"},
		"Source.file":   {desc: "A path your file tools can read (relative to your workspace, or under a mount such as maestro/), read once at launch. Set exactly one of inline and file"},

		"Participant.agent":         {desc: "An existing agent itself, with its conversation, memory and tools; must be in your allowed agents"},
		"Participant.clone":         {desc: "A temporary copy of this agent, deleted when the forum ends; must be in your allowed agents"},
		"Participant.model":         {desc: "Without agent or clone: a fresh temporary agent with no tools on this model (forum_models lists them). With clone: optional override, one of that agent's models. Not allowed with agent"},
		"Participant.system_prompt": {desc: "Fresh form only: the participant's whole system prompt"},
		"Participant.mode":          {desc: "Fresh form only: memory (default) keeps its conversation with a new memory, context keeps its conversation without memory, single_shot sees only the current message", enum: enumOf(freshModeValues)},
		"Participant.instructions":  {desc: "Private instructions for this participant only"},
		"Participant.name":          {desc: "Name shown in the transcript; the participant ID when omitted"},

		"Layer.id":           {desc: "Layer ID (letters, digits, _ and -, not starting with a digit), unique"},
		"Layer.enabled":      {desc: "false skips the layer; true when omitted"},
		"Layer.participants": {desc: "Participant IDs taking part, also the turn order"},
		"Layer.instructions": {desc: "What the participants do in this layer"},
		"Layer.inputs":       {desc: "What the layer receives besides the brief and instructions"},
		"Layer.delivery":     {desc: "after_round: turns of a round do not see each other (independent opinions); per_turn: each turn sees the earlier ones (a debate)", enum: enumOf(deliveryValues)},
		"Layer.max_rounds":   {desc: "Number of rounds", min: minPositive},
		"Layer.max_calls":    {desc: "Optional budget of messages for this layer, repairs and moderator checks included; at most limits.max_calls", min: minPositive},
		"Layer.output":       {desc: "The format of each participant's output"},
		"Layer.moderator":    {desc: "Optional participant consulted between rounds; it continues, guides or stops the layer"},

		"Output.format": {desc: "Output format", enum: formats},
		"Output.schema": {desc: "json only: ID of an entry of schemas the output must match"},
		"Output.share":  {desc: "json only: JSON Pointers of the object members other participants see; omitted publishes the whole output, [] publishes nothing"},

		"Route.from":       {desc: "\"source:<id>\" or \"layer:<id>\" (an earlier layer only)"},
		"Route.select":     {desc: "Which records of a layer: all (default) or the newest of each author", enum: enumOf(selectValues)},
		"Route.authors":    {desc: "Layer inputs only: keep only outputs by these participant IDs"},
		"Route.view":       {desc: "published (default): what each author shares; full: whole outputs, which needs to on a layer input", enum: enumOf(viewValues)},
		"Route.paths":      {desc: "json producers only: JSON Pointers selecting the parts to pass"},
		"Route.to":         {desc: "Layer inputs only: recipient participant IDs; every participant of the layer when omitted"},
		"Route.distribute": {desc: "all (default): every recipient gets everything; same_participant: each gets its own outputs; random: records dealt out at random", enum: enumOf(distributeValues)},
		"Route.optional":   {desc: "Allow the input to be empty or its layer disabled"},
		"Route.anonymous":  {desc: "Layer inputs only: show the outputs as Response A, B, ... without authors, leaving out each reader's own; the reading layer must be after_round with one round and no moderator"},

		"Moderator.participant":       {desc: "Participant ID of the moderator; not one of the layer's participants"},
		"Moderator.after_round":       {desc: "First round after which the moderator is consulted; below max_rounds", min: minPositive},
		"Moderator.every_rounds":      {desc: "Consult again every this many rounds", min: minPositive},
		"Moderator.inputs":            {desc: "What the moderator receives besides the conversation"},
		"Moderator.conversation_view": {desc: "published (default) or full outputs of the layer's conversation", enum: enumOf(conversationViewValues)},
		"Moderator.schema":            {desc: "ID of an entry of schemas for the decision's assessment"},
		"Moderator.allow_directed":    {desc: "Let the moderator send private notes to individual participants"},

		"Limits.max_calls":             {desc: "Total messages for the run, repairs and moderator checks included", min: minPositive},
		"Limits.max_duration_seconds":  {desc: "Longest the run may take", min: minPositive},
		"Limits.call_timeout_seconds":  {desc: "Longest one message may take", min: minPositive},
		"Limits.max_attempts_per_turn": {desc: "Attempts per turn, the first included", min: minPositive},
		"Limits.max_parallel_calls":    {desc: "Messages sent at the same time", min: minPositive},
	}

	// rawSchemas gives the schema of a json.RawMessage field, which holds
	// JSON the type system does not describe.
	rawSchemas = map[string]map[string]any{
		"Config.schemas": {"type": "object", "additionalProperties": true}, // one JSON Schema document
		"Source.inline":  {},                                               // any JSON value
	}
)

// patchNullInTypes selects how the patch schema allows null (which deletes
// a member): true adds "null" to each member's type and enum; false leaves
// them as in the import schema, so null is only stated in the changes
// parameter's description (for a provider that refuses type arrays or a
// null enum value).
const patchNullInTypes = true

var (
	configSchema = sync.OnceValue(func() map[string]any { return structSchema(reflect.TypeFor[Config](), false) })
	patchSchema  = sync.OnceValue(func() map[string]any { return structSchema(reflect.TypeFor[Config](), true) })
)

// ConfigSchema is the JSON Schema of a configuration, the argument of
// forum_config_import: every field with its type, description, enum and
// minimum, objects closed, nothing required. The map is shared: do not
// modify it.
func ConfigSchema() map[string]any { return configSchema() }

// PatchSchema is the JSON Schema of a merge patch of the configuration, the
// argument of forum_config_update: ConfigSchema's structure with every
// member also allowed to be null (allowNull). Arrays are replaced whole, so
// their items are ConfigSchema's. The map is shared: do not modify it.
func PatchSchema() map[string]any { return patchSchema() }

// structSchema is the closed object schema of struct type t (Decode refuses
// unknown fields). In a patch every member may be null.
func structSchema(t reflect.Type, patch bool) map[string]any {
	props := map[string]any{}
	for f := range t.Fields() {
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		key := t.Name() + "." + name
		s := typeSchema(f.Type, key, patch)
		doc := fieldDocs[key]
		if doc.desc != "" {
			s["description"] = doc.desc
		}
		if len(doc.enum) > 0 {
			enum := make([]any, len(doc.enum))
			for i, v := range doc.enum {
				enum[i] = v
			}
			s["enum"] = enum
		}
		if doc.min != 0 {
			s["minimum"] = doc.min
		}
		if doc.max != 0 {
			s["maximum"] = doc.max
		}
		if doc.maxLength != 0 {
			s["maxLength"] = doc.maxLength
		}
		if patch {
			allowNull(s)
		}
		props[name] = s
	}
	return map[string]any{"type": "object", "properties": props, "additionalProperties": false}
}

// typeSchema is the schema of a value of type t, the type of the field key.
func typeSchema(t reflect.Type, key string, patch bool) map[string]any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == rawMessageType {
		raw, ok := rawSchemas[key]
		if !ok {
			panic("forum: no JSON Schema for raw JSON field " + key)
		}
		return maps.Clone(raw)
	}
	switch t.Kind() {
	case reflect.Struct:
		return structSchema(t, patch)
	case reflect.Map:
		value := typeSchema(t.Elem(), key, patch)
		if patch {
			allowNull(value)
		}
		return map[string]any{"type": "object", "additionalProperties": value}
	case reflect.Slice:
		// A patch replaces an array whole: its items are the import form.
		return map[string]any{"type": "array", "items": typeSchema(t.Elem(), key, false)}
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int64:
		return map[string]any{"type": "integer"}
	default:
		panic("forum: no JSON Schema for " + t.String() + " (" + key + ")")
	}
}

// allowNull lets a patch member be null, as patchNullInTypes selects.
func allowNull(s map[string]any) { allowNullIn(s, patchNullInTypes) }

// allowNullIn adds "null" to s's type and enum when inTypes is set; a
// schema without a type already allows null. With inTypes unset it leaves
// s unchanged.
func allowNullIn(s map[string]any, inTypes bool) {
	if !inTypes {
		return
	}
	if typ, ok := s["type"].(string); ok {
		s["type"] = []string{typ, "null"}
	}
	if enum, ok := s["enum"].([]any); ok {
		s["enum"] = append(enum, nil)
	}
}
