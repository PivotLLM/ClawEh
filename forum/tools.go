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
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/PivotLLM/toolspec"
)

// Seam (e): the tool definitions (spec §9). Names are bare; the host's
// aggregator mounts them under the "forum" namespace (forum_models,
// forum_launch, ...) and gates the whole suite on the calling agent's
// `forum` permission. The wiring into ClawEh (a tools/forum provider that
// builds the ToolHost from tools.ToolDeps) is done separately: it calls
// Tools(svc, host) with the process's one *Service and its ToolHost.

// ToolHost is what the tool layer needs from ClawEh for one call.
type ToolHost interface {
	// Scope returns the calling agent's scope: its ID and
	// <workspace>/forums as an absolute path. It fails for an agent
	// without the `forum` permission, as defence in depth behind the
	// aggregator's gating.
	//
	// It must also refuse every call that could come from inside a forum
	// turn: a call at the maximum sub-agent depth (every forum Ask runs
	// there) with an error wrapping ErrForumDepth, and a call by a
	// temporary agent a forum created (a clone or fresh participant) with
	// one wrapping ErrForumTurn. A forum can then never
	// launch, control or read forums, whatever its participants' tools
	// allow. The tool returns that refusal to the agent as a tool error.
	Scope(call *toolspec.ToolCall) (Scope, error)
	// ResolveFile resolves a configuration file reference for the agent to
	// an absolute path it may read, or fails with a message for the agent.
	ResolveFile(agentID, ref string) (absPath string, err error)
	// ReadAllowed reports whether the agent may read an absolute path.
	ReadAllowed(agentID, absPath string) error
	// Workspace is the agent's workspace, the ConfigDir of an inline
	// configuration.
	Workspace(agentID string) (string, error)
}

// Tools returns the ten forum tools over svc. Every handler resolves the
// caller's Scope first: ErrForumTurn and ErrForumDepth are tool errors, any other Scope
// failure is returned as the error. Every failure of the operation itself
// is a Result with IsError and one plain sentence naming the forum (when
// there is one): an unknown forum, a refused state, a forum locked by
// another process, an invalid configuration, unavailable schemas, a
// damaged forum, temporary agents that could not be deleted, or an
// internal failure. Error chains and paths never reach the agent; they
// are logged.
//
// Parameters and results:
//
//	readme(template?)               -> the guide and the template list, or one
//	                                   template's configuration verbatim
//	models()                        -> JSON array of ModelInfo
//	validate(config | config_file)  -> "The configuration is valid." or the issues, one per line
//	launch(config | config_file)    -> "Forum <id> launched." Exactly one of config
//	                                   (a JSON object) and config_file (a file
//	                                   reference the agent may read)
//	status(id?)                     -> one Summary as JSON, or a JSON array of every forum's Summary
//	pause(id), resume(id), cancel(id), delete(id) -> a one-line confirmation
//	results(id)                     -> a ResultsView as JSON: each result output's
//	                                   author, layer, round, size and file, and its
//	                                   text within MaxResultInlineChars and
//	                                   MaxResultInlineTotalChars; paths are
//	                                   relative to the agent's workspace
func Tools(svc *Service, host ToolHost) []toolspec.ToolDefinition {
	t := &toolSuite{svc: svc, host: host}
	id := toolspec.Parameter{Name: "id", Type: "string", Required: true, Description: "Forum ID (UUID) returned by launch"}
	configParams := []toolspec.Parameter{
		{Name: "config", Type: "object", Description: "The forum configuration as a JSON object (use this or config_file)"},
		{Name: "config_file", Type: "string", Description: "Path of a forum configuration file the agent may read (use this or config)"},
	}
	defs := []toolspec.ToolDefinition{
		{Name: "readme", Description: "How forums work, with built-in configuration templates; give template to get one", Handler: t.readme, Category: "forum", Parameters: []toolspec.Parameter{
			{Name: "template", Type: "string", Description: "Name of a built-in template; omit for the guide and the list of templates"},
		}},
		{Name: "models", Description: "List the models the calling agent may give to fresh forum participants", Handler: t.models, Category: "forum"},
		{Name: "validate", Description: "Validate a forum configuration without creating anything", Handler: t.validate, Category: "forum", Parameters: configParams},
		{Name: "launch", Description: "Validate and launch a forum; returns its ID and runs it in the background", Handler: t.launch, Category: "forum", Parameters: configParams},
		{Name: "status", Description: "Progress of one forum, or summaries of all the agent's forums", Handler: t.status, Category: "forum", Parameters: []toolspec.Parameter{
			{Name: "id", Type: "string", Description: "Forum ID; omit for all forums"},
		}},
		{Name: "pause", Description: "Stop new dispatch and pause the forum once active turns finish", Handler: t.pause, Category: "forum", Parameters: []toolspec.Parameter{id}},
		{Name: "resume", Description: "Resume a paused forum", Handler: t.resume, Category: "forum", Parameters: []toolspec.Parameter{id}},
		{Name: "cancel", Description: "Stop the forum, keep its partial work and finalize it as cancelled", Handler: t.cancel, Category: "forum", Parameters: []toolspec.Parameter{id}},
		{Name: "results", Description: "Results of a forum: each final output's author, layer, round, size, file and text (long text is cut; the file has it all), and the transcript's path", Handler: t.results, Category: "forum", Parameters: []toolspec.Parameter{id}},
		{Name: "delete", Description: "Delete a paused or finished forum's directory", Handler: t.delete, Category: "forum", Parameters: []toolspec.Parameter{id}},
	}
	for i := range defs {
		defs[i].Handler = t.refuseUnknownArgs(defs[i].Parameters, defs[i].Handler)
		if defs[i].Name != "readme" {
			defs[i].Description += ". " + readFirst
		}
	}
	return defs
}

// readFirst ends every forum tool's description but the readme's own.
const readFirst = "Call forum_readme first if you have not."

// refuseUnknownArgs wraps a handler so a call with an argument the tool does
// not declare is refused in one sentence naming it, rather than the argument
// being ignored (forum_status with forum_id would otherwise list every forum).
// A scope refusal (a call from inside a forum turn) still comes first.
func (t *toolSuite) refuseUnknownArgs(params []toolspec.Parameter, next toolspec.ToolHandler) toolspec.ToolHandler {
	known := make(map[string]bool, len(params))
	names := make([]string, 0, len(params))
	for _, p := range params {
		known[p.Name] = true
		names = append(names, p.Name)
	}
	return func(call *toolspec.ToolCall) (*toolspec.Result, error) {
		var unknown []string
		for name := range call.Args {
			if !known[name] {
				unknown = append(unknown, name)
			}
		}
		if len(unknown) == 0 {
			return next(call)
		}
		if _, err := t.host.Scope(call); err != nil {
			return scopeFailure(err)
		}
		slices.Sort(unknown)
		msg := "Unknown argument " + unknown[0]
		if len(unknown) > 1 {
			msg = "Unknown arguments " + strings.Join(unknown, ", ")
		}
		if len(names) == 0 {
			msg += "; this tool takes no arguments."
		} else {
			msg += "; use " + strings.Join(names, " or ") + "."
		}
		return &toolspec.Result{ForLLM: msg, IsError: true}, nil
	}
}

// toolSuite holds what the handlers share.
type toolSuite struct {
	svc  *Service
	host ToolHost
}

func (t *toolSuite) readme(call *toolspec.ToolCall) (*toolspec.Result, error) {
	if _, err := t.host.Scope(call); err != nil {
		return scopeFailure(err)
	}
	name, present, err := stringArg(call, "template")
	if err != nil {
		return t.fail(err, "", "readme")
	}
	if !present {
		return &toolspec.Result{ForLLM: guide()}, nil
	}
	config, ok := templateConfig(name)
	if !ok {
		return &toolspec.Result{ForLLM: fmt.Sprintf("There is no template %q; use %s.", name, templateNames()), IsError: true}, nil
	}
	return &toolspec.Result{ForLLM: config}, nil
}

func (t *toolSuite) models(call *toolspec.ToolCall) (*toolspec.Result, error) {
	scope, err := t.host.Scope(call)
	if err != nil {
		return scopeFailure(err)
	}
	list, err := t.svc.Models(call.Ctx, scope.AgentID)
	if err != nil {
		t.svc.host.Logger.Errorf("forum tool models (agent %s): %v", scope.AgentID, err)
		return &toolspec.Result{ForLLM: "The models could not be listed because of an internal error.", IsError: true, Err: err}, nil
	}
	if list == nil {
		list = []ModelInfo{}
	}
	return jsonResult(list)
}

func (t *toolSuite) validate(call *toolspec.ToolCall) (*toolspec.Result, error) {
	scope, err := t.host.Scope(call)
	if err != nil {
		return scopeFailure(err)
	}
	opts, raw, err := t.launchOptions(call, scope)
	if err == nil {
		err = t.svc.Validate(call.Ctx, raw, opts)
	}
	if err != nil {
		return t.fail(err, "", "validate")
	}
	return &toolspec.Result{ForLLM: "The configuration is valid."}, nil
}

func (t *toolSuite) launch(call *toolspec.ToolCall) (*toolspec.Result, error) {
	scope, err := t.host.Scope(call)
	if err != nil {
		return scopeFailure(err)
	}
	opts, raw, err := t.launchOptions(call, scope)
	if err != nil {
		return t.fail(err, "", "launch")
	}
	id, err := t.svc.Launch(call.Ctx, raw, opts)
	if err != nil {
		return t.fail(err, "", "launch")
	}
	return &toolspec.Result{ForLLM: fmt.Sprintf("Forum %s launched.", id)}, nil
}

func (t *toolSuite) status(call *toolspec.ToolCall) (*toolspec.Result, error) {
	scope, err := t.host.Scope(call)
	if err != nil {
		return scopeFailure(err)
	}
	id, present, err := stringArg(call, "id")
	if err != nil {
		return t.fail(err, "", "status")
	}
	if !present || id == "" {
		list, listErr := t.svc.List(call.Ctx, scope)
		if listErr != nil {
			return t.fail(listErr, "", "status")
		}
		return jsonResult(list)
	}
	sum, err := t.svc.Status(call.Ctx, scope, id)
	if err != nil {
		return t.fail(err, id, "status")
	}
	return jsonResult(sum)
}

func (t *toolSuite) pause(call *toolspec.ToolCall) (*toolspec.Result, error) {
	return t.control(call, "pause", (*Service).Pause, "Forum %s is pausing; it pauses once its active turns finish.")
}

func (t *toolSuite) resume(call *toolspec.ToolCall) (*toolspec.Result, error) {
	return t.control(call, "resume", (*Service).Resume, "Forum %s is running.")
}

func (t *toolSuite) cancel(call *toolspec.ToolCall) (*toolspec.Result, error) {
	return t.control(call, "cancel", (*Service).Cancel, "Forum %s is being cancelled; its partial work is kept.")
}

func (t *toolSuite) delete(call *toolspec.ToolCall) (*toolspec.Result, error) {
	return t.control(call, "delete", (*Service).Delete, "Forum %s is deleted.")
}

// control runs one of the ID-only lifecycle operations (named by tool)
// and confirms it with done (a format taking the ID).
func (t *toolSuite) control(call *toolspec.ToolCall, tool string, op func(*Service, context.Context, Scope, string) error, done string) (*toolspec.Result, error) {
	scope, err := t.host.Scope(call)
	if err != nil {
		return scopeFailure(err)
	}
	id, err := requiredID(call)
	if err != nil {
		return t.fail(err, "", tool)
	}
	if err := op(t.svc, call.Ctx, scope, id); err != nil {
		return t.fail(err, id, tool)
	}
	return &toolspec.Result{ForLLM: fmt.Sprintf(done, id)}, nil
}

func (t *toolSuite) results(call *toolspec.ToolCall) (*toolspec.Result, error) {
	scope, err := t.host.Scope(call)
	if err != nil {
		return scopeFailure(err)
	}
	id, err := requiredID(call)
	if err != nil {
		return t.fail(err, "", "results")
	}
	res, err := t.svc.Results(call.Ctx, scope, id)
	if err != nil {
		return t.fail(err, id, "results")
	}
	store, err := t.svc.open(scope, id)
	if err != nil {
		return t.fail(err, id, "results")
	}
	// Paths are relative to the agent's workspace, whose forums/ folder holds
	// the base directory.
	prefix := filepath.Join(filepath.Base(scope.BaseDirectory), id)
	return jsonResult(newResultsView(res, prefix, store.ReadPrefix))
}

// launchOptions builds LaunchOptions for validate and launch from the call:
// Scope from the host, Origin from the call's agent, channel, chat and
// session, ReadAllowed bound to the agent, and the configuration bytes and
// ConfigDir from exactly one of the arguments: `config` (a JSON object,
// re-encoded, ConfigDir = the agent's workspace) or `config_file`
// (resolved with ToolHost.ResolveFile and read; ConfigDir = its directory).
// Argument mistakes are a *ValidationError; host failures are returned
// as they are.
//
// A `config` object arrives decoded by the host, so its JSON numbers have
// passed through float64: an integer above 2^53 (a large seed) loses
// precision. config_file keeps the bytes exactly.
func (t *toolSuite) launchOptions(call *toolspec.ToolCall, scope Scope) (LaunchOptions, []byte, error) {
	opts := LaunchOptions{
		Scope: scope,
		Origin: Origin{
			AgentID: scope.AgentID, Channel: call.Channel, ChatID: call.ChatID, Session: call.Session,
		},
		ReadAllowed: func(abs string) error {
			return t.host.ReadAllowed(scope.AgentID, abs)
		},
	}
	inline, hasInline := call.Args["config"]
	hasInline = hasInline && inline != nil
	ref, hasFile, err := stringArg(call, "config_file")
	if err != nil {
		return opts, nil, err
	}
	switch {
	case hasInline && hasFile:
		return opts, nil, argIssue("give either config or config_file, not both")
	case hasFile:
		if ref == "" {
			return opts, nil, argIssue("the config_file argument is empty")
		}
		abs, err := t.host.ResolveFile(scope.AgentID, ref)
		if err != nil {
			return opts, nil, argIssue(fmt.Sprintf("the configuration file %q cannot be used: %v", ref, err))
		}
		raw, err := os.ReadFile(abs) //nolint:gosec // resolved by the host as readable by the calling agent
		if err != nil {
			t.svc.host.Logger.Warnf("forum tools: agent %s: reading configuration file %q: %v", scope.AgentID, ref, err)
			return opts, nil, argIssue(fmt.Sprintf("the configuration file %q cannot be read", ref))
		}
		opts.ConfigDir = filepath.Dir(abs)
		return opts, raw, nil
	case hasInline:
		raw, err := inlineConfig(inline)
		if err != nil {
			return opts, nil, err
		}
		if opts.ConfigDir, err = t.host.Workspace(scope.AgentID); err != nil {
			return opts, nil, fmt.Errorf("workspace of agent %s: %w", scope.AgentID, err)
		}
		return opts, raw, nil
	}
	return opts, nil, argIssue("give the configuration as config (a JSON object) or config_file (a file path)")
}

// inlineConfig encodes the `config` argument: a JSON object as the host
// decoded it, or raw JSON bytes holding an object.
func inlineConfig(v any) ([]byte, error) {
	switch c := v.(type) {
	case map[string]any:
		raw, err := json.Marshal(c)
		if err != nil {
			return nil, argIssue("the config argument cannot be encoded as JSON")
		}
		return raw, nil
	case json.RawMessage:
		if isJSONObject(c) {
			return c, nil
		}
	case string:
		return nil, argIssue("the config argument must be a JSON object, not a string; pass the object itself or use config_file")
	}
	return nil, argIssue("the config argument must be a JSON object")
}

// argIssue is a *ValidationError about the tool's arguments: one issue
// without a path, which fail renders as a sentence.
func argIssue(msg string) error {
	return &ValidationError{Issues: []Issue{{Message: msg}}}
}

// stringArg returns a string argument and whether it was given (a null
// counts as absent); any other type is a *ValidationError.
func stringArg(call *toolspec.ToolCall, name string) (string, bool, error) {
	v, ok := call.Args[name]
	if !ok || v == nil {
		return "", false, nil
	}
	s, ok := v.(string)
	if !ok {
		return "", false, argIssue("the " + name + " argument must be a string")
	}
	return s, true, nil
}

// requiredID returns the `id` argument, which must be a nonempty string.
func requiredID(call *toolspec.ToolCall) (string, error) {
	id, ok, err := stringArg(call, "id")
	if err != nil {
		return "", err
	}
	if !ok || strings.TrimSpace(id) == "" {
		return "", argIssue("the id argument is required: the forum ID returned by launch")
	}
	return strings.TrimSpace(id), nil
}

// jsonResult renders v as indented JSON.
func jsonResult(v any) (*toolspec.Result, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode result: %w", err)
	}
	return &toolspec.Result{ForLLM: string(data)}, nil
}

// scopeFailure renders a ToolHost.Scope failure: a call from inside a
// forum turn (ErrForumTurn, ErrForumDepth) is a tool error the agent sees; anything else
// is returned as the error, for the host to report.
func scopeFailure(err error) (*toolspec.Result, error) {
	if errors.Is(err, ErrForumTurn) {
		return &toolspec.Result{ForLLM: "Forum tools are not available inside a forum turn.", IsError: true, Err: refused{err}}, nil
	}
	if errors.Is(err, ErrForumDepth) {
		return &toolspec.Result{ForLLM: "Forum tools are not available at the maximum sub-agent depth.", IsError: true, Err: refused{err}}, nil
	}
	return nil, err
}

// refused marks a scope refusal as expected (the host's tools.IsRefusal
// recognises the Refusal method), so it is logged as a warning, not an error.
type refused struct{ error }

func (r refused) Unwrap() error { return r.error }
func (refused) Refusal() bool   { return true }

// toolVerbs is the past participle of each tool's operation, for the
// internal-failure message.
var toolVerbs = map[string]string{
	"validate": "validated", "launch": "launched", "status": "read", "results": "read",
	"pause": "paused", "resume": "resumed", "cancel": "cancelled", "delete": "deleted",
}

// fail renders a failed operation of tool for the agent as a Result with
// IsError and one plain sentence naming the forum id (when there is one).
// Error chains and paths are never shown: a damaged forum and an internal
// failure are logged at Error with the detail and reported in a fixed
// sentence.
func (t *toolSuite) fail(err error, id, tool string) (*toolspec.Result, error) {
	return &toolspec.Result{ForLLM: t.message(err, id, tool), IsError: true, Err: err}, nil
}

// message is fail's sentence.
func (t *toolSuite) message(err error, id, tool string) string {
	ve, isValidation := errors.AsType[*ValidationError](err)
	left, agentsLeft := errors.AsType[*agentsLeftError](err)
	state, isState := errors.AsType[*stateError](err)
	subject := "The forum"
	if id != "" {
		subject = "Forum " + id
	}
	switch {
	case isValidation && len(ve.Issues) == 1 && ve.Issues[0].Path == "":
		return sentence(ve.Issues[0].Message)
	case isValidation:
		return ve.Error()
	case agentsLeft:
		noun := "agent " + left.agents[0]
		if len(left.agents) > 1 {
			noun = "agents " + strings.Join(left.agents, ", ")
		}
		t.svc.host.Logger.Warnf("forum tool %s: %v", tool, err)
		return fmt.Sprintf("Forum %s was not deleted because its temporary %s could not be deleted; try again later.", left.forumID, noun)
	case isState:
		return sentence(state.msg)
	case errors.Is(err, ErrNotFound):
		return subject + " was not found."
	case errors.Is(err, ErrLocked):
		return subject + " is in use by another process; try again later."
	case errors.Is(err, ErrCorrupt):
		t.svc.host.Logger.Errorf("forum tool %s (forum %s): %v", tool, id, err)
		return subject + " is damaged and cannot be used."
	case errors.Is(err, ErrSchemasUnavailable):
		return sentence(err.Error())
	case errors.Is(err, errClosed):
		return "Forums cannot be started or changed while the service is shutting down."
	case errors.Is(err, ErrInvalidState):
		t.svc.host.Logger.Warnf("forum tool %s (forum %s): %v", tool, id, err)
		return fmt.Sprintf("%s cannot be %s in its current state.", subject, toolVerbs[tool])
	}
	t.svc.host.Logger.Errorf("forum tool %s (forum %s): %v", tool, id, err)
	return fmt.Sprintf("%s could not be %s because of an internal error.", subject, toolVerbs[tool])
}

// sentence capitalises msg and ends it with a full stop.
func sentence(msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return msg
	}
	r, size := utf8.DecodeRuneInString(msg)
	msg = string(unicode.ToUpper(r)) + msg[size:]
	if !strings.HasSuffix(msg, ".") {
		msg += "."
	}
	return msg
}
