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

// Tools returns the nine forum tools over svc. Every handler resolves the
// caller's Scope first (a failure is returned as the error). Outcomes the
// agent can act on (an unknown forum, a refused state, a forum locked by
// another process, an invalid configuration, unavailable schemas, a
// damaged forum) are a Result with IsError and one plain message naming
// the forum; anything else (a host or I/O failure) is returned as the
// error.
//
// Parameters and results:
//
//	models()                        -> JSON array of ModelInfo
//	validate(config | config_file)  -> "The configuration is valid." or the issues, one per line
//	launch(config | config_file)    -> "Forum <id> launched." Exactly one of config
//	                                   (a JSON object) and config_file (a file
//	                                   reference the agent may read)
//	status(id?)                     -> one Summary as JSON, or a JSON array of every forum's Summary
//	pause(id), resume(id), cancel(id), delete(id) -> a one-line confirmation
//	results(id)                     -> the Result manifest as JSON, with "directory",
//	                                   the forum's absolute directory that its
//	                                   file names are relative to
func Tools(svc *Service, host ToolHost) []toolspec.ToolDefinition {
	t := &toolSuite{svc: svc, host: host}
	id := toolspec.Parameter{Name: "id", Type: "string", Required: true, Description: "Forum ID (UUID) returned by launch"}
	configParams := []toolspec.Parameter{
		{Name: "config", Type: "object", Description: "The forum configuration as a JSON object (use this or config_file)"},
		{Name: "config_file", Type: "string", Description: "Path of a forum configuration file the agent may read (use this or config)"},
	}
	return []toolspec.ToolDefinition{
		{Name: "models", Description: "List the models the calling agent may give to fresh forum participants", Handler: t.models, Category: "forum"},
		{Name: "validate", Description: "Validate a forum configuration without creating anything", Handler: t.validate, Category: "forum", Parameters: configParams},
		{Name: "launch", Description: "Validate and launch a forum; returns its ID and runs it in the background", Handler: t.launch, Category: "forum", Parameters: configParams},
		{Name: "status", Description: "Progress of one forum, or summaries of all the agent's forums", Handler: t.status, Category: "forum", Parameters: []toolspec.Parameter{
			{Name: "id", Type: "string", Description: "Forum ID; omit for all forums"},
		}},
		{Name: "pause", Description: "Stop new dispatch and pause the forum once active turns finish", Handler: t.pause, Category: "forum", Parameters: []toolspec.Parameter{id}},
		{Name: "resume", Description: "Resume a paused forum", Handler: t.resume, Category: "forum", Parameters: []toolspec.Parameter{id}},
		{Name: "cancel", Description: "Stop the forum, keep its partial work and finalize it as cancelled", Handler: t.cancel, Category: "forum", Parameters: []toolspec.Parameter{id}},
		{Name: "results", Description: "Result files and completeness of a forum", Handler: t.results, Category: "forum", Parameters: []toolspec.Parameter{id}},
		{Name: "delete", Description: "Delete a paused or finished forum's directory", Handler: t.delete, Category: "forum", Parameters: []toolspec.Parameter{id}},
	}
}

// toolSuite holds what the handlers share.
type toolSuite struct {
	svc  *Service
	host ToolHost
}

func (t *toolSuite) models(call *toolspec.ToolCall) (*toolspec.Result, error) {
	scope, err := t.host.Scope(call)
	if err != nil {
		return nil, err
	}
	list, err := t.svc.Models(call.Ctx, scope.AgentID)
	if err != nil {
		return nil, fmt.Errorf("models of agent %s: %w", scope.AgentID, err)
	}
	if list == nil {
		list = []ModelInfo{}
	}
	return jsonResult(list)
}

func (t *toolSuite) validate(call *toolspec.ToolCall) (*toolspec.Result, error) {
	scope, err := t.host.Scope(call)
	if err != nil {
		return nil, err
	}
	opts, raw, err := t.launchOptions(call, scope)
	if err == nil {
		err = t.svc.Validate(call.Ctx, raw, opts)
	}
	if err != nil {
		return toolError(err, "")
	}
	return &toolspec.Result{ForLLM: "The configuration is valid."}, nil
}

func (t *toolSuite) launch(call *toolspec.ToolCall) (*toolspec.Result, error) {
	scope, err := t.host.Scope(call)
	if err != nil {
		return nil, err
	}
	opts, raw, err := t.launchOptions(call, scope)
	if err != nil {
		return toolError(err, "")
	}
	id, err := t.svc.Launch(call.Ctx, raw, opts)
	if err != nil {
		return toolError(err, "")
	}
	return &toolspec.Result{ForLLM: fmt.Sprintf("Forum %s launched.", id)}, nil
}

func (t *toolSuite) status(call *toolspec.ToolCall) (*toolspec.Result, error) {
	scope, err := t.host.Scope(call)
	if err != nil {
		return nil, err
	}
	id, present, err := stringArg(call, "id")
	if err != nil {
		return toolError(err, "")
	}
	if !present || id == "" {
		list, listErr := t.svc.List(call.Ctx, scope)
		if listErr != nil {
			return nil, listErr
		}
		return jsonResult(list)
	}
	sum, err := t.svc.Status(call.Ctx, scope, id)
	if err != nil {
		return toolError(err, id)
	}
	return jsonResult(sum)
}

func (t *toolSuite) pause(call *toolspec.ToolCall) (*toolspec.Result, error) {
	return t.control(call, (*Service).Pause, "Forum %s is pausing; it pauses once its active turns finish.")
}

func (t *toolSuite) resume(call *toolspec.ToolCall) (*toolspec.Result, error) {
	return t.control(call, (*Service).Resume, "Forum %s is running.")
}

func (t *toolSuite) cancel(call *toolspec.ToolCall) (*toolspec.Result, error) {
	return t.control(call, (*Service).Cancel, "Forum %s is being cancelled; its partial work is kept.")
}

func (t *toolSuite) delete(call *toolspec.ToolCall) (*toolspec.Result, error) {
	return t.control(call, (*Service).Delete, "Forum %s is deleted.")
}

// control runs one of the ID-only lifecycle operations and confirms it
// with done (a format taking the ID).
func (t *toolSuite) control(call *toolspec.ToolCall, op func(*Service, context.Context, Scope, string) error, done string) (*toolspec.Result, error) {
	scope, err := t.host.Scope(call)
	if err != nil {
		return nil, err
	}
	id, err := requiredID(call)
	if err != nil {
		return toolError(err, "")
	}
	if err := op(t.svc, call.Ctx, scope, id); err != nil {
		return toolError(err, id)
	}
	return &toolspec.Result{ForLLM: fmt.Sprintf(done, id)}, nil
}

// resultsView is what the results tool returns: the manifest and the
// directory its file names are relative to.
type resultsView struct {
	Directory string `json:"directory"`
	*Result
}

func (t *toolSuite) results(call *toolspec.ToolCall) (*toolspec.Result, error) {
	scope, err := t.host.Scope(call)
	if err != nil {
		return nil, err
	}
	id, err := requiredID(call)
	if err != nil {
		return toolError(err, "")
	}
	res, err := t.svc.Results(call.Ctx, scope, id)
	if err != nil {
		return toolError(err, id)
	}
	return jsonResult(resultsView{Directory: filepath.Join(scope.BaseDirectory, id), Result: res})
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
		Scope:  scope,
		Origin: Origin{AgentID: scope.AgentID, Channel: call.Channel, ChatID: call.ChatID, Session: call.Session},
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
			return opts, nil, argIssue(fmt.Sprintf("the configuration file %q cannot be read: %v", ref, err))
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
			return nil, argIssue(fmt.Sprintf("the config argument cannot be encoded as JSON: %v", err))
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
// without a path, which toolError renders as a sentence.
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

// toolError renders a service error for the agent. ErrNotFound,
// ErrInvalidState, ErrLocked, ErrCorrupt, a *ValidationError and
// ErrSchemasUnavailable become Result.IsError with one plain message
// naming the forum id (when there is one); anything else is returned as
// the error.
func toolError(err error, id string) (*toolspec.Result, error) {
	var msg string
	ve, isValidation := errors.AsType[*ValidationError](err)
	switch {
	case isValidation && len(ve.Issues) == 1 && ve.Issues[0].Path == "":
		msg = sentence(ve.Issues[0].Message)
	case isValidation:
		msg = ve.Error()
	case errors.Is(err, ErrNotFound) && id != "":
		msg = fmt.Sprintf("Forum %s was not found.", id)
	case errors.Is(err, ErrLocked) && id != "":
		msg = fmt.Sprintf("Forum %s is in use by another process; try again later.", id)
	case errors.Is(err, ErrCorrupt) && id != "":
		msg = fmt.Sprintf("Forum %s is damaged and cannot be used: %v.", id, err)
	case errors.Is(err, ErrInvalidState), errors.Is(err, ErrSchemasUnavailable),
		errors.Is(err, ErrNotFound), errors.Is(err, ErrLocked), errors.Is(err, ErrCorrupt):
		msg = sentence(err.Error())
	default:
		return nil, err
	}
	return &toolspec.Result{ForLLM: msg, IsError: true, Err: err}, nil
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
