// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"context"
	"encoding/json"
	"strings"
)

// Seam (a): validation. ValidateStatic needs nothing but the configuration;
// Preflight additionally consults the host (agents, models, schemas, source
// files). forum_validate runs both; forum_launch runs both and then uses the
// Resolved result.

// Issue is one validation finding. Path is a dotted JSON path into the
// configuration ("layers[2].inputs[0].to", "participants.alice.model");
// Message says what is wrong and, where there is one, the accepted value.
type Issue struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// ValidationError carries every issue found, so a caller can report them
// all at once rather than one per round trip.
type ValidationError struct {
	Issues []Issue
}

// Error lists the issues, one per line, as "<path>: <message>".
func (e *ValidationError) Error() string {
	lines := make([]string, 0, len(e.Issues))
	for _, is := range e.Issues {
		lines = append(lines, is.Path+": "+is.Message)
	}
	return "invalid configuration:\n" + strings.Join(lines, "\n")
}

// ValidateStatic checks everything that can be checked without the host and
// returns a *ValidationError listing every finding, or nil. The checks, by
// spec section:
//
// §3 IDs and references
//   - every participant, source, schema and layer ID matches ValidID; layer
//     IDs are unique (participant, source and schema duplicates are already
//     rejected by Decode);
//   - brief.purpose and brief.task are nonempty;
//   - each source has a valid decode and exactly one of inline / file; for
//     decode text or markdown, inline is a JSON string; for decode json,
//     inline is any JSON value; file is a relative path without "..";
//   - each schema is a JSON object;
//   - layers is nonempty and at least one layer is enabled;
//   - result_layers name existing, enabled, distinct layers;
//   - every limit is positive.
//
// §3.1 participants
//   - exactly one form: agent, clone, or model without agent/clone;
//   - `model` is rejected on the agent form (an existing agent always runs
//     on its own model); on the clone form it is an optional override;
//   - system_prompt and mode only on the fresh form; mode is one of
//     FreshMode's values; a fresh participant has a nonempty model;
//   - instructions and name are optional.
//
// §3.2 layers
//   - participants nonempty, unique, each naming a configured participant;
//   - instructions nonempty; delivery is after_round or per_turn;
//   - max_rounds positive; max_calls, when present, positive and not above
//     limits.max_calls;
//   - output.format is a Format; output.schema and output.share are
//     rejected unless format is json; output.schema names a configured
//     schema; output.share, when present, passes CheckProjection (an
//     empty array is valid and publishes nothing).
//
// Routes (rev 3 §4; layer inputs and moderator inputs)
//   - from parses (Route.Producer); a source producer exists; a layer
//     producer exists and precedes the consuming layer in the array;
//   - a non-optional route from a disabled layer is an error;
//   - select, view and distribute are known values or empty;
//   - select, authors, view and same_participant are rejected on a source
//     route (a source contributes one record, authorless);
//   - view full requires a nonempty to;
//   - authors name participants of the producing layer; to names
//     participants of the consuming layer and is rejected on a moderator
//     route (the moderator is the only recipient);
//   - paths passes CheckProjection and is rejected unless the producer is
//     json (a json source or a json output layer);
//   - same_participant requires every recipient to be a participant of the
//     producing layer;
//   - a random route from a source (one record) to more than one recipient
//     must be optional (rev 3: fewer records than recipients requires
//     optional). For layer producers the record count is not known
//     statically; the router enforces it at run time.
//
// §6 moderator
//   - participant names a configured participant that is not one of the
//     layer's participants;
//   - after_round and every_rounds positive, after_round < max_rounds
//     (checks happen only while below max_rounds);
//   - conversation_view is a ConversationView or empty;
//   - schema, when set, names a configured schema that is a JSON object
//     (it becomes the required `assessment`);
//   - allow_directed is a plain flag; nothing further to check statically.
func ValidateStatic(cfg *Config) error {
	return errNotImplemented
}

// PreflightEnv is what Preflight needs from the host.
type PreflightEnv struct {
	// Launcher is the launching agent's ID.
	Launcher string
	Agents   Agents
	// Schemas may be nil; a configuration naming any schema then fails with
	// ErrSchemasUnavailable.
	Schemas SchemaValidator
	// HostLimits are ceilings on Config.Limits; a zero field is no ceiling.
	// A limit above its ceiling is reported as an issue naming the ceiling
	// (it is never capped silently).
	HostLimits Limits
	// ConfigDir is the directory source `file` paths resolve against: the
	// configuration file's directory, or the launching agent's workspace
	// for an inline configuration.
	ConfigDir string
	// ReadAllowed reports whether the launching agent may read an absolute
	// path; a nil func allows nothing (every file source fails).
	ReadAllowed func(absPath string) error
}

// Resolved is what Preflight establishes and Launch records in the snapshot.
type Resolved struct {
	// Models maps a participant ID to the model it runs on for this forum:
	// every fresh participant, plus clones with a `model` override. Resume
	// never substitutes another model (§2.4).
	Models map[string]string
	// Schemas are the compiled named schemas.
	Schemas map[string]CompiledSchema
	// ModeratorSchemas maps a layer ID to its effective decision schema
	// (EffectiveModeratorSchema), for every enabled layer with a moderator;
	// Launch stores them in Snapshot.ModeratorSchemas.
	ModeratorSchemas map[string]json.RawMessage
	// SourceFiles maps a file source's ID to the absolute path that was
	// checked; Launch reads and copies it.
	SourceFiles map[string]string
}

// Preflight checks the configuration against the host without creating
// anything (§2.4, §3). It requires a configuration that passed
// ValidateStatic. It returns a *ValidationError listing every finding, or
// the Resolved result. Only participants used by enabled layers (as
// participants or moderators) are checked. Checks:
//
//   - existing and clone participants: Agents.MayTarget(launcher, id) is
//     true and Agents.Exists(id) is true;
//   - a clone's `model` override is in Agents.Models(source);
//   - a fresh participant's model is in Agents.Models(launcher);
//   - every named schema compiles (Schemas.Compile), and every enabled
//     layer's effective moderator schema (EffectiveModeratorSchema)
//     compiles too; a nil Schemas with any schema configured is
//     ErrSchemasUnavailable (returned directly, not as an issue);
//   - each file source resolves under ConfigDir to a path ReadAllowed
//     accepts and that exists as a regular file; a json source's content
//     (inline or file) parses as one JSON value;
//   - each limit is within HostLimits.
func Preflight(ctx context.Context, cfg *Config, env PreflightEnv) (*Resolved, error) {
	return nil, errNotImplemented
}
