// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"github.com/PivotLLM/toolspec"
)

// Seam (e): the tool definitions (spec §9). Names are bare; the host's
// aggregator mounts them under the "forum" namespace (forum_models,
// forum_launch, ...) and gates the whole suite on the calling agent's
// `forum` permission. The wiring into ClawEh (a tools/forum provider that
// builds the ToolHost from tools.ToolDeps) is done separately.

// ToolHost is what the tool layer needs from ClawEh for one call.
type ToolHost interface {
	// Scope returns the calling agent's scope: its ID and
	// <workspace>/forums as an absolute path. It fails for an agent
	// without the `forum` permission, as defence in depth behind the
	// aggregator's gating.
	Scope(call *toolspec.ToolCall) (Scope, error)
	// ResolveFile resolves a configuration file reference for the agent to
	// an absolute path it may read, or fails.
	ResolveFile(agentID, ref string) (absPath string, err error)
	// ReadAllowed reports whether the agent may read an absolute path.
	ReadAllowed(agentID, absPath string) error
	// Workspace is the agent's workspace, the ConfigDir of an inline
	// configuration.
	Workspace(agentID string) (string, error)
}

// Tools returns the nine forum tools over svc. Every handler resolves the
// caller's Scope first and returns a tool error (Result.IsError) for
// ErrNotFound, ErrInvalidState, ErrLocked and validation errors, with the
// message written for the agent; transport failures are returned as the
// error.
//
// Parameters:
//
//	models()                        -> JSON array of ModelInfo
//	validate(config | config_file)  -> "valid" or the issues, one per line
//	launch(config | config_file)    -> the forum ID; exactly one of config
//	                                   (a JSON object) and config_file (a
//	                                   file reference the agent may read)
//	status(id?)                     -> one Summary, or every forum's Summary
//	pause(id), resume(id), cancel(id), delete(id) -> confirmation
//	results(id)                     -> the Result manifest
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
	return nil, errNotImplemented
}

func (t *toolSuite) validate(call *toolspec.ToolCall) (*toolspec.Result, error) {
	scope, err := t.host.Scope(call)
	if err != nil {
		return nil, err
	}
	opts, raw, err := t.launchOptions(call, scope)
	if err != nil {
		return toolOutcome(err)
	}
	return toolOutcome(t.svc.Validate(call.Ctx, raw, opts))
}

func (t *toolSuite) launch(call *toolspec.ToolCall) (*toolspec.Result, error) {
	scope, err := t.host.Scope(call)
	if err != nil {
		return nil, err
	}
	opts, raw, err := t.launchOptions(call, scope)
	if err != nil {
		return toolOutcome(err)
	}
	id, err := t.svc.Launch(call.Ctx, raw, opts)
	if err != nil {
		return toolOutcome(err)
	}
	return &toolspec.Result{ForLLM: id}, nil
}

func (t *toolSuite) status(call *toolspec.ToolCall) (*toolspec.Result, error) {
	return nil, errNotImplemented
}

func (t *toolSuite) pause(call *toolspec.ToolCall) (*toolspec.Result, error) {
	return nil, errNotImplemented
}

func (t *toolSuite) resume(call *toolspec.ToolCall) (*toolspec.Result, error) {
	return nil, errNotImplemented
}

func (t *toolSuite) cancel(call *toolspec.ToolCall) (*toolspec.Result, error) {
	return nil, errNotImplemented
}

func (t *toolSuite) results(call *toolspec.ToolCall) (*toolspec.Result, error) {
	return nil, errNotImplemented
}

func (t *toolSuite) delete(call *toolspec.ToolCall) (*toolspec.Result, error) {
	return nil, errNotImplemented
}

// launchOptions builds LaunchOptions for validate and launch from the call:
// Scope from the host, Origin from the call's agent, channel, chat and
// session, ReadAllowed bound to the agent, and the configuration bytes and
// ConfigDir from exactly one of the arguments: `config` (a JSON object,
// re-encoded verbatim, ConfigDir = the agent's workspace) or `config_file`
// (resolved with ToolHost.ResolveFile and read; ConfigDir = its directory).
// Both or neither is a *ValidationError.
func (t *toolSuite) launchOptions(call *toolspec.ToolCall, scope Scope) (LaunchOptions, []byte, error) {
	return LaunchOptions{}, nil, errNotImplemented
}

// toolOutcome renders a service error for the agent: nil is a plain
// confirmation; ErrNotFound, ErrInvalidState, ErrLocked, a
// *ValidationError and ErrSchemasUnavailable become Result.IsError with
// the message; anything else is returned as the error.
func toolOutcome(err error) (*toolspec.Result, error) {
	return nil, errNotImplemented
}
