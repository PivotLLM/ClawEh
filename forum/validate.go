// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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

// Error lists the issues, one per line, as "<path>: <message>" (just the
// message for an issue about the whole document).
func (e *ValidationError) Error() string {
	lines := make([]string, 0, len(e.Issues))
	for _, is := range e.Issues {
		if is.Path == "" {
			lines = append(lines, is.Message)
			continue
		}
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
//     result_layers likewise unique;
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
//   - a non-optional route from a disabled layer is an error when the
//     consuming layer is enabled;
//   - select, view and distribute are known values or empty;
//   - select, authors, view and same_participant are rejected on a source
//     route (a source contributes one record, authorless);
//   - view full requires a nonempty to (a moderator route names its one
//     recipient by being the moderator's, so it may use view full);
//   - authors name participants of the producing layer; to names
//     participants of the consuming layer and is rejected on a moderator
//     route (the moderator is the only recipient); neither repeats an ID;
//   - a moderator route's distribute is all (one recipient);
//   - paths passes CheckProjection and is rejected unless the producer is
//     json (a json source or a json output layer); with view published and
//     a producer `share`, each path must lie within what share publishes
//     (it would otherwise always resolve to nothing at run time);
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
	v := &staticValidator{cfg: cfg, layerIndex: map[string]int{}}
	v.run()
	if len(v.issues) > 0 {
		return &ValidationError{Issues: v.issues}
	}
	return nil
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
	if env.Agents == nil || env.Launcher == "" {
		return nil, errors.New("preflight: launcher and Agents are required")
	}
	if len(cfg.Schemas) > 0 && env.Schemas == nil {
		return nil, fmt.Errorf("%w (the configuration names schemas: %s)", ErrSchemasUnavailable, strings.Join(sortedKeys(cfg.Schemas), ", "))
	}
	p := &preflight{cfg: cfg, env: env, models: map[string][]ModelInfo{}, res: &Resolved{
		Models:           map[string]string{},
		Schemas:          map[string]CompiledSchema{},
		ModeratorSchemas: map[string]json.RawMessage{},
		SourceFiles:      map[string]string{},
	}}
	if err := p.participants(ctx); err != nil {
		return nil, err
	}
	p.schemas()
	if err := p.sources(); err != nil {
		return nil, err
	}
	p.hostLimits()
	if len(p.issues) > 0 {
		return nil, &ValidationError{Issues: p.issues}
	}
	return p.res, nil
}

// staticValidator accumulates the issues of ValidateStatic.
type staticValidator struct {
	cfg    *Config
	issues []Issue
	// layerIndex maps a layer ID to its first position in cfg.Layers.
	layerIndex map[string]int
}

func (v *staticValidator) addf(path, format string, args ...any) {
	v.issues = append(v.issues, Issue{Path: path, Message: fmt.Sprintf(format, args...)})
}

func (v *staticValidator) run() {
	v.brief()
	v.sources()
	v.participants()
	v.schemas()
	v.limits()
	v.layers()
	v.resultLayers()
}

func (v *staticValidator) brief() {
	if strings.TrimSpace(v.cfg.Brief.Purpose) == "" {
		v.addf("brief.purpose", "is required")
	}
	if strings.TrimSpace(v.cfg.Brief.Task) == "" {
		v.addf("brief.task", "is required")
	}
}

func (v *staticValidator) sources() {
	for _, id := range sortedKeys(v.cfg.Sources) {
		src := v.cfg.Sources[id]
		path := "sources." + id
		if !ValidID(id) {
			v.addf(path, "source ID %q: %s", id, idRule)
		}
		if !validFormat(src.Decode) {
			v.addf(path+".decode", "%q is not one of text, markdown, json", src.Decode)
		}
		hasInline, hasFile := src.Inline != nil, src.File != ""
		switch {
		case hasInline && hasFile:
			v.addf(path, "source %q sets both inline and file; set exactly one", id)
		case !hasInline && !hasFile:
			v.addf(path, "source %q needs exactly one of inline or file", id)
		}
		if hasInline && (src.Decode == FormatText || src.Decode == FormatMarkdown) {
			var text string
			if json.Unmarshal(src.Inline, &text) != nil {
				v.addf(path+".inline", "must be a JSON string for decode %q", src.Decode)
			}
		}
		if hasFile {
			if msg := relativePathProblem(src.File); msg != "" {
				v.addf(path+".file", "%q %s", src.File, msg)
			}
		}
	}
}

// relativePathProblem says why a source file path is not acceptable, or
// returns "" for a relative path that stays below its base directory.
func relativePathProblem(p string) string {
	slashed := filepath.ToSlash(p)
	if filepath.IsAbs(p) || strings.HasPrefix(slashed, "/") || filepath.VolumeName(p) != "" {
		return "must be a relative path"
	}
	for elem := range strings.SplitSeq(slashed, "/") {
		if elem == ".." {
			return `must not contain ".."`
		}
	}
	return ""
}

func (v *staticValidator) participants() {
	if len(v.cfg.Participants) == 0 {
		v.addf("participants", "at least one participant is required")
	}
	for _, id := range sortedKeys(v.cfg.Participants) {
		p := v.cfg.Participants[id]
		path := "participants." + id
		if !ValidID(id) {
			v.addf(path, "participant ID %q: %s", id, idRule)
		}
		switch {
		case p.Agent != "" && p.Clone != "":
			v.addf(path, "participant %q sets both agent and clone; set exactly one of agent, clone or model", id)
		case p.Agent != "" && p.Model != "":
			v.addf(path+".model", "participant %q: an existing agent always runs on its own model; use clone to choose a model", id)
		case p.Agent == "" && p.Clone == "" && p.Model == "":
			v.addf(path, "participant %q needs exactly one of agent, clone or model", id)
		}
		if p.Form() != FormFresh {
			if p.SystemPrompt != "" {
				v.addf(path+".system_prompt", "participant %q: system_prompt applies only to a fresh participant (model)", id)
			}
			if p.Mode != "" {
				v.addf(path+".mode", "participant %q: mode applies only to a fresh participant (model)", id)
			}
		} else if p.Mode != "" && !validMode(p.Mode) {
			v.addf(path+".mode", "participant %q: %q is not one of memory, context, single_shot", id, p.Mode)
		}
	}
}

func (v *staticValidator) schemas() {
	for _, id := range sortedKeys(v.cfg.Schemas) {
		path := "schemas." + id
		if !ValidID(id) {
			v.addf(path, "schema ID %q: %s", id, idRule)
		}
		if !isJSONObject(v.cfg.Schemas[id]) {
			v.addf(path, "schema %q must be a JSON object", id)
		}
	}
}

func (v *staticValidator) limits() {
	l := v.cfg.Limits
	for _, f := range []struct {
		name  string
		value int
	}{
		{"max_calls", l.MaxCalls},
		{"max_duration_seconds", l.MaxDurationSeconds},
		{"call_timeout_seconds", l.CallTimeoutSeconds},
		{"max_attempts_per_turn", l.MaxAttemptsPerTurn},
		{"max_parallel_calls", l.MaxParallelCalls},
	} {
		if f.value <= 0 {
			v.addf("limits."+f.name, "must be a positive integer, got %d", f.value)
		}
	}
}

func (v *staticValidator) layers() {
	if len(v.cfg.Layers) == 0 {
		v.addf("layers", "at least one layer is required")
		return
	}
	enabled := 0
	for i, l := range v.cfg.Layers {
		path := layerPath(i)
		if !ValidID(l.ID) {
			v.addf(path+".id", "layer ID %q: %s", l.ID, idRule)
		}
		if prev, dup := v.layerIndex[l.ID]; dup {
			v.addf(path+".id", "layer ID %q is already used by %s", l.ID, layerPath(prev))
		} else {
			v.layerIndex[l.ID] = i
		}
		if l.IsEnabled() {
			enabled++
		}
	}
	if enabled == 0 {
		v.addf("layers", "every layer is disabled; enable at least one")
	}
	for i, l := range v.cfg.Layers {
		v.layer(i, l)
	}
}

func (v *staticValidator) layer(i int, l Layer) {
	path := layerPath(i)
	if len(l.Participants) == 0 {
		v.addf(path+".participants", "layer %q needs at least one participant", l.ID)
	}
	v.participantRefs(path+".participants", l.Participants, "layer "+strconv.Quote(l.ID))
	if strings.TrimSpace(l.Instructions) == "" {
		v.addf(path+".instructions", "layer %q: instructions are required", l.ID)
	}
	if l.Delivery != DeliveryAfterRound && l.Delivery != DeliveryPerTurn {
		v.addf(path+".delivery", "layer %q: %q is not one of after_round, per_turn", l.ID, l.Delivery)
	}
	if l.MaxRounds <= 0 {
		v.addf(path+".max_rounds", "layer %q: must be a positive integer, got %d", l.ID, l.MaxRounds)
	}
	switch {
	case l.MaxCalls < 0:
		v.addf(path+".max_calls", "layer %q: must be a positive integer, got %d", l.ID, l.MaxCalls)
	case l.MaxCalls > 0 && v.cfg.Limits.MaxCalls > 0 && l.MaxCalls > v.cfg.Limits.MaxCalls:
		v.addf(path+".max_calls", "layer %q: %d is above the forum's limits.max_calls (%d)", l.ID, l.MaxCalls, v.cfg.Limits.MaxCalls)
	}
	v.output(path+".output", l)
	for r, route := range l.Inputs {
		v.route(fmt.Sprintf("%s.inputs[%d]", path, r), i, l, route, false)
	}
	if l.Moderator != nil {
		v.moderator(path+".moderator", i, l)
	}
}

// participantRefs checks a list of participant IDs: each configured, none
// repeated.
func (v *staticValidator) participantRefs(path string, ids []string, owner string) {
	seen := map[string]bool{}
	for k, id := range ids {
		if _, ok := v.cfg.Participants[id]; !ok {
			v.addf(fmt.Sprintf("%s[%d]", path, k), "%s: participant %q is not configured", owner, id)
		}
		if seen[id] {
			v.addf(fmt.Sprintf("%s[%d]", path, k), "%s: participant %q is listed twice", owner, id)
		}
		seen[id] = true
	}
}

func (v *staticValidator) output(path string, l Layer) {
	o := l.Output
	if !validFormat(o.Format) {
		v.addf(path+".format", "layer %q: %q is not one of text, markdown, json", l.ID, o.Format)
	}
	if o.Format != FormatJSON {
		if o.Schema != "" {
			v.addf(path+".schema", "layer %q: schema applies only to format json", l.ID)
		}
		if o.Share != nil {
			v.addf(path+".share", "layer %q: share applies only to format json", l.ID)
		}
		return
	}
	if o.Schema != "" {
		if _, ok := v.cfg.Schemas[o.Schema]; !ok {
			v.addf(path+".schema", "layer %q: schema %q is not configured", l.ID, o.Schema)
		}
	}
	if o.Share != nil {
		for _, msg := range CheckProjection(*o.Share) {
			v.addf(path+".share", "layer %q: %s", l.ID, msg)
		}
	}
}

// route checks one input route of the layer at index li. moderator marks a
// moderator input, whose only recipient is the moderator.
func (v *staticValidator) route(path string, li int, l Layer, r Route, moderator bool) {
	kind, id, err := r.Producer()
	if err != nil {
		v.addf(path+".from", "%v", err)
		return
	}
	if r.Select != "" && r.Select != SelectAll && r.Select != SelectLastPerParticipant {
		v.addf(path+".select", "%q is not one of all, last_per_participant", r.Select)
	}
	if r.View != "" && r.View != ViewPublished && r.View != ViewFull {
		v.addf(path+".view", "%q is not one of published, full", r.View)
	}
	if r.Distribute != "" && r.Distribute != DistributeAll && r.Distribute != DistributeSameParticipant && r.Distribute != DistributeRandom {
		v.addf(path+".distribute", "%q is not one of all, same_participant, random", r.Distribute)
	}

	// Recipients: the moderator alone, or `to`, or the layer's participants.
	recipients := l.Participants
	if moderator {
		recipients = []string{l.Moderator.Participant}
		if len(r.To) > 0 {
			v.addf(path+".to", "a moderator input goes to the moderator only; remove to")
		}
		if r.Distribute == DistributeSameParticipant || r.Distribute == DistributeRandom {
			v.addf(path+".distribute", "a moderator input has one recipient; distribute must be all")
		}
	} else if len(r.To) > 0 {
		recipients = r.To
		v.recipientRefs(path+".to", r.To, l)
	}
	if r.View == ViewFull && !moderator && len(r.To) == 0 {
		v.addf(path+".view", "view full requires the recipients to be named in to")
	}

	var producerJSON, known bool
	var share *[]string
	switch kind {
	case RouteFromSource:
		src, ok := v.cfg.Sources[id]
		if !ok {
			v.addf(path+".from", "source %q is not configured", id)
		}
		known, producerJSON = ok, src.Decode == FormatJSON
		if r.Select != "" {
			v.addf(path+".select", "select applies only to layer inputs (a source is one record)")
		}
		if len(r.Authors) > 0 {
			v.addf(path+".authors", "authors applies only to layer inputs (a source has no author)")
		}
		if r.View != "" {
			v.addf(path+".view", "view applies only to layer inputs")
		}
		if r.Distribute == DistributeSameParticipant {
			v.addf(path+".distribute", "same_participant applies only to layer inputs (a source has no author)")
		}
		if r.Distribute == DistributeRandom && len(recipients) > 1 && !r.Optional {
			v.addf(path+".distribute", "random deals source %q (one record) to %d recipients, so some get nothing; mark the input optional", id, len(recipients))
		}
	case RouteFromLayer:
		pi, ok := v.layerIndex[id]
		if !ok {
			v.addf(path+".from", "layer %q is not configured", id)
			break
		}
		if pi >= li {
			v.addf(path+".from", "layer %q must come before layer %q (inputs refer backward only)", id, l.ID)
			break
		}
		producer := v.cfg.Layers[pi]
		known, producerJSON = true, producer.Output.Format == FormatJSON
		if r.View != ViewFull {
			share = producer.Output.Share
		}
		if !producer.IsEnabled() && !r.Optional && l.IsEnabled() {
			v.addf(path+".from", "layer %q is disabled; enable it or mark the input optional", id)
		}
		v.authorRefs(path+".authors", r.Authors, producer)
		if r.Distribute == DistributeSameParticipant {
			for _, to := range recipients {
				if !slices.Contains(producer.Participants, to) {
					v.addf(path+".distribute", "same_participant: recipient %q is not a participant of layer %q", to, id)
				}
			}
		}
	}
	if len(r.Paths) > 0 && known {
		if !producerJSON {
			v.addf(path+".paths", "paths apply only to JSON content; %q is not JSON", r.From)
		}
		for _, msg := range CheckProjection(r.Paths) {
			v.addf(path+".paths", "%s", msg)
		}
		if share != nil {
			for _, p := range r.Paths {
				if !withinShare(p, *share) {
					v.addf(path+".paths", "path %q is outside what layer %q publishes (share); use view full or widen share", p, id)
				}
			}
		}
	}
}

// recipientRefs checks a route's `to`: participants of the consuming
// layer, none repeated.
func (v *staticValidator) recipientRefs(path string, to []string, l Layer) {
	seen := map[string]bool{}
	for k, id := range to {
		if !slices.Contains(l.Participants, id) {
			v.addf(fmt.Sprintf("%s[%d]", path, k), "%q is not a participant of layer %q", id, l.ID)
		}
		if seen[id] {
			v.addf(fmt.Sprintf("%s[%d]", path, k), "%q is listed twice", id)
		}
		seen[id] = true
	}
}

// authorRefs checks a route's `authors`: participants of the producing
// layer, none repeated.
func (v *staticValidator) authorRefs(path string, authors []string, producer Layer) {
	seen := map[string]bool{}
	for k, id := range authors {
		if !slices.Contains(producer.Participants, id) {
			v.addf(fmt.Sprintf("%s[%d]", path, k), "%q is not a participant of layer %q", id, producer.ID)
		}
		if seen[id] {
			v.addf(fmt.Sprintf("%s[%d]", path, k), "%q is listed twice", id)
		}
		seen[id] = true
	}
}

// withinShare reports whether pointer p can resolve inside a projection
// built from share: p equals, lies under, or encloses a shared member.
// Pointers that are not "/"-prefixed are left to CheckProjection.
func withinShare(p string, share []string) bool {
	if !strings.HasPrefix(p, "/") {
		return true
	}
	pt := strings.Split(p[1:], "/")
	for _, s := range share {
		if !strings.HasPrefix(s, "/") {
			return true
		}
		st := strings.Split(s[1:], "/")
		n := min(len(pt), len(st))
		if slices.Equal(pt[:n], st[:n]) {
			return true
		}
	}
	return false
}

func (v *staticValidator) moderator(path string, li int, l Layer) {
	m := l.Moderator
	switch {
	case m.Participant == "":
		v.addf(path+".participant", "layer %q: the moderator's participant is required", l.ID)
	case !hasKey(v.cfg.Participants, m.Participant):
		v.addf(path+".participant", "layer %q: participant %q is not configured", l.ID, m.Participant)
	case slices.Contains(l.Participants, m.Participant):
		v.addf(path+".participant", "layer %q: %q is a participant of the layer and cannot also moderate it", l.ID, m.Participant)
	}
	if m.AfterRound <= 0 {
		v.addf(path+".after_round", "layer %q: must be a positive integer, got %d", l.ID, m.AfterRound)
	} else if l.MaxRounds > 0 && m.AfterRound >= l.MaxRounds {
		v.addf(path+".after_round", "layer %q: %d must be below max_rounds (%d); the moderator is consulted only between rounds", l.ID, m.AfterRound, l.MaxRounds)
	}
	if m.EveryRounds <= 0 {
		v.addf(path+".every_rounds", "layer %q: must be a positive integer, got %d", l.ID, m.EveryRounds)
	}
	if m.ConversationView != "" && m.ConversationView != ConversationViewPublished && m.ConversationView != ConversationViewFull {
		v.addf(path+".conversation_view", "layer %q: %q is not one of published, full", l.ID, m.ConversationView)
	}
	if m.Schema != "" && !hasKey(v.cfg.Schemas, m.Schema) {
		v.addf(path+".schema", "layer %q: schema %q is not configured", l.ID, m.Schema)
	}
	if m.Participant == "" {
		return // recipients of the moderator's inputs are unknown
	}
	for r, route := range m.Inputs {
		v.route(fmt.Sprintf("%s.inputs[%d]", path, r), li, l, route, true)
	}
}

func (v *staticValidator) resultLayers() {
	seen := map[string]bool{}
	for k, id := range v.cfg.ResultLayers {
		path := fmt.Sprintf("result_layers[%d]", k)
		i, ok := v.layerIndex[id]
		switch {
		case !ok:
			v.addf(path, "layer %q is not configured", id)
		case !v.cfg.Layers[i].IsEnabled():
			v.addf(path, "layer %q is disabled; a result layer must be enabled", id)
		}
		if seen[id] {
			v.addf(path, "layer %q is listed twice", id)
		}
		seen[id] = true
	}
}

// idRule is the configuration ID syntax, for messages.
const idRule = "use letters, digits, underscore and hyphen, not starting with a digit"

func layerPath(i int) string { return fmt.Sprintf("layers[%d]", i) }

func validFormat(f Format) bool {
	return f == FormatText || f == FormatMarkdown || f == FormatJSON
}

func validMode(m FreshMode) bool {
	return m == FreshModeMemory || m == FreshModeContext || m == FreshModeSingleShot
}

func isJSONObject(raw json.RawMessage) bool {
	var obj map[string]json.RawMessage
	return json.Unmarshal(raw, &obj) == nil && obj != nil
}

func hasKey[V any](m map[string]V, k string) bool {
	_, ok := m[k]
	return ok
}

// sortedKeys returns m's keys in order, so issues come out deterministically.
func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}

// preflight accumulates the issues and results of Preflight.
type preflight struct {
	cfg    *Config
	env    PreflightEnv
	issues []Issue
	res    *Resolved
	// models caches Agents.Models per agent ID.
	models map[string][]ModelInfo
}

func (p *preflight) addf(path, format string, args ...any) {
	p.issues = append(p.issues, Issue{Path: path, Message: fmt.Sprintf(format, args...)})
}

// usedParticipants returns the IDs of participants that take part in an
// enabled layer, as participants or moderators, sorted.
func (p *preflight) usedParticipants() []string {
	used := map[string]bool{}
	for _, l := range p.cfg.EnabledLayers() {
		for _, id := range l.Participants {
			used[id] = true
		}
		if l.Moderator != nil {
			used[l.Moderator.Participant] = true
		}
	}
	return slices.Sorted(maps.Keys(used))
}

func (p *preflight) participants(ctx context.Context) error {
	for _, id := range p.usedParticipants() {
		part, ok := p.cfg.Participants[id]
		if !ok {
			continue // reported by ValidateStatic
		}
		path := "participants." + id
		switch part.Form() {
		case FormExisting:
			if _, err := p.target(ctx, path+".agent", part.Agent); err != nil {
				return err
			}
		case FormClone:
			ok, err := p.target(ctx, path+".clone", part.Clone)
			if err != nil {
				return err
			}
			if ok && part.Model != "" {
				found, names, err := p.hasModel(ctx, part.Clone, part.Model)
				if err != nil {
					return err
				}
				if found {
					p.res.Models[id] = part.Model
				} else {
					p.addf(path+".model", "model %q is not one of agent %q's models (%s)", part.Model, part.Clone, names)
				}
			}
		case FormFresh:
			found, names, err := p.hasModel(ctx, p.env.Launcher, part.Model)
			if err != nil {
				return err
			}
			if found {
				p.res.Models[id] = part.Model
			} else {
				p.addf(path+".model", "model %q is not one of the launching agent's models (%s)", part.Model, names)
			}
		}
	}
	return nil
}

// target checks that the launcher may name agentID and that it exists.
// Existence is not checked (nor revealed) for an agent the launcher may
// not name.
func (p *preflight) target(ctx context.Context, path, agentID string) (bool, error) {
	allowed, err := p.env.Agents.MayTarget(ctx, p.env.Launcher, agentID)
	if err != nil {
		return false, fmt.Errorf("preflight: may %q target %q: %w", p.env.Launcher, agentID, err)
	}
	if !allowed {
		p.addf(path, "the launching agent may not use agent %q (not in its allowed agents)", agentID)
		return false, nil
	}
	exists, err := p.env.Agents.Exists(ctx, agentID)
	if err != nil {
		return false, fmt.Errorf("preflight: does agent %q exist: %w", agentID, err)
	}
	if !exists {
		p.addf(path, "agent %q does not exist", agentID)
		return false, nil
	}
	return true, nil
}

// hasModel reports whether model is in agentID's model list, and the list
// of names for the message.
func (p *preflight) hasModel(ctx context.Context, agentID, model string) (bool, string, error) {
	list, ok := p.models[agentID]
	if !ok {
		var err error
		if list, err = p.env.Agents.Models(ctx, agentID); err != nil {
			return false, "", fmt.Errorf("preflight: models of agent %q: %w", agentID, err)
		}
		p.models[agentID] = list
	}
	names := make([]string, 0, len(list))
	found := false
	for _, m := range list {
		names = append(names, m.Name)
		found = found || m.Name == model
	}
	if len(names) == 0 {
		return found, "none", nil
	}
	return found, strings.Join(names, ", "), nil
}

// schemas compiles every named schema and builds (and, with a validator,
// compiles) every enabled layer's effective moderator schema.
func (p *preflight) schemas() {
	for _, id := range sortedKeys(p.cfg.Schemas) {
		compiled, err := p.env.Schemas.Compile(p.cfg.Schemas[id])
		if err != nil {
			p.addf("schemas."+id, "schema %q: %v", id, err)
			continue
		}
		p.res.Schemas[id] = compiled
	}
	for i, l := range p.cfg.Layers {
		if !l.IsEnabled() || l.Moderator == nil {
			continue
		}
		var assessment json.RawMessage
		if l.Moderator.Schema != "" {
			if _, ok := p.res.Schemas[l.Moderator.Schema]; !ok {
				continue // the named schema failed (reported above)
			}
			assessment = p.cfg.Schemas[l.Moderator.Schema]
		}
		path := layerPath(i) + ".moderator"
		eff, err := EffectiveModeratorSchema(l, assessment)
		if err != nil {
			p.addf(path, "%v", err)
			continue
		}
		if p.env.Schemas != nil {
			if _, err := p.env.Schemas.Compile(eff); err != nil {
				p.addf(path, "layer %q: the moderator's decision schema does not compile: %v", l.ID, err)
				continue
			}
		}
		p.res.ModeratorSchemas[l.ID] = eff
	}
}

// sources checks every file source and the content of json file sources.
func (p *preflight) sources() error {
	for _, id := range sortedKeys(p.cfg.Sources) {
		src := p.cfg.Sources[id]
		if src.File == "" {
			continue
		}
		if !filepath.IsAbs(p.env.ConfigDir) {
			return fmt.Errorf("preflight: source %q: the configuration directory %q is not absolute", id, p.env.ConfigDir)
		}
		path := "sources." + id + ".file"
		abs := filepath.Join(p.env.ConfigDir, filepath.FromSlash(src.File))
		resolvedPath, err := p.readable(abs)
		if err != nil {
			p.addf(path, "source %q: %q %v", id, src.File, err)
			continue
		}
		if src.Decode == FormatJSON {
			data, err := os.ReadFile(resolvedPath) //nolint:gosec // the path passed ReadAllowed for the launching agent
			if err != nil {
				p.addf(path, "source %q: %q cannot be read: %v", id, src.File, err)
				continue
			}
			if err := checkDuplicateKeys(data); err != nil {
				p.addf(path, "source %q: %q is not one valid JSON value: %v", id, src.File, issueText(err))
				continue
			}
		}
		p.res.SourceFiles[id] = resolvedPath
	}
	return nil
}

// readable resolves abs (following symbolic links) and checks that the
// launching agent may read both the named path and its target and that the
// target is a regular file. It returns the resolved path.
func (p *preflight) readable(abs string) (string, error) {
	if p.env.ReadAllowed == nil {
		return "", errors.New("is not readable by the launching agent")
	}
	if err := p.env.ReadAllowed(abs); err != nil {
		return "", fmt.Errorf("is not readable by the launching agent: %w", err)
	}
	target, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", errors.New("does not exist")
		}
		return "", fmt.Errorf("cannot be resolved: %w", err)
	}
	if target != abs {
		if terr := p.env.ReadAllowed(target); terr != nil {
			return "", fmt.Errorf("links to %q, which the launching agent may not read: %w", target, terr)
		}
	}
	info, err := os.Stat(target)
	if err != nil {
		return "", fmt.Errorf("cannot be read: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("is not a regular file")
	}
	return target, nil
}

// issueText renders a *ValidationError's issues on one line.
func issueText(err error) string {
	ve, ok := errors.AsType[*ValidationError](err)
	if !ok {
		return err.Error()
	}
	parts := make([]string, 0, len(ve.Issues))
	for _, is := range ve.Issues {
		if is.Path != "" {
			parts = append(parts, is.Path+": "+is.Message)
		} else {
			parts = append(parts, is.Message)
		}
	}
	return strings.Join(parts, "; ")
}

// hostLimits rejects any limit above its host ceiling.
func (p *preflight) hostLimits() {
	l, h := p.cfg.Limits, p.env.HostLimits
	for _, f := range []struct {
		name           string
		value, ceiling int
	}{
		{"max_calls", l.MaxCalls, h.MaxCalls},
		{"max_duration_seconds", l.MaxDurationSeconds, h.MaxDurationSeconds},
		{"call_timeout_seconds", l.CallTimeoutSeconds, h.CallTimeoutSeconds},
		{"max_attempts_per_turn", l.MaxAttemptsPerTurn, h.MaxAttemptsPerTurn},
		{"max_parallel_calls", l.MaxParallelCalls, h.MaxParallelCalls},
	} {
		if f.ceiling > 0 && f.value > f.ceiling {
			p.addf("limits."+f.name, "%d is above the host ceiling of %d", f.value, f.ceiling)
		}
	}
}
