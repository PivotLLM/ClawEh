// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package classifier

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"

	"github.com/PivotLLM/toolspec"
)

// The tool definitions. Names are bare; the host's aggregator mounts them
// under the "classifier" namespace (classifier_list, classifier_create, ...)
// and gates the whole suite on the calling agent's `classifiers` switch.

// ToolHost is what the tool layer needs from ClawEh for one call.
type ToolHost interface {
	// Scope returns the calling agent's scope: the owning agent (a clone
	// acts as its source) and <workspace>/classifiers as an absolute path.
	// It fails for an agent without the classifiers switch.
	Scope(call *toolspec.ToolCall) (Scope, error)
}

// readFirst ends every classifier tool's description but the readme's own.
const readFirst = "Call classifier_readme first if you have not."

// Tools returns the eleven classifier tools over svc. Every failure of an
// operation is a Result with IsError and plain sentences: an unknown
// classifier, a refused definition (one issue per line), an item the source
// does not list, or an internal failure (logged; no path or error chain
// reaches the agent).
func Tools(svc *Service, host ToolHost) []toolspec.ToolDefinition {
	t := &toolSuite{svc: svc, host: host}
	id := toolspec.Parameter{Name: "id", Type: "string", Required: true, Description: "Classifier ID"}
	definition := toolspec.Parameter{Name: "definition", Type: "object", Required: true, Description: "The classifier definition as a JSON object"}
	defs := []toolspec.ToolDefinition{
		{Name: "readme", Description: "How classifiers work: the definition format, paths, substitution and examples", Handler: t.readme},
		{Name: "list", Description: "List your classifiers", Handler: t.list},
		{Name: "get", Description: "Return a classifier's definition", Handler: t.get, Parameters: []toolspec.Parameter{id}},
		{Name: "create", Description: "Create a classifier; it is checked against your tools, its source tool is called once, and it runs at once unless dry_run is set", Handler: t.create, Parameters: []toolspec.Parameter{definition}},
		{Name: "update", Description: "Change a classifier with a JSON merge patch; items already handled are not reprocessed. To change rules kept in Markdown (instructions_file, instructions_dir), edit those files with your file tools instead", Handler: t.update, Parameters: []toolspec.Parameter{
			id,
			{Name: "changes", Type: "object", Required: true, Description: "JSON merge patch (RFC 7386): objects merge, null deletes a key, arrays such as classify.rules are replaced whole"},
		}},
		{Name: "delete", Description: "Delete a classifier and its state", Handler: t.delete, Parameters: []toolspec.Parameter{id}},
		{Name: "validate", Description: "Check a definition without saving it, including your tools and one call of its source tool", Handler: t.validate, Parameters: []toolspec.Parameter{definition}},
		{Name: "status", Description: "A classifier's last run, counts, recent decisions, last error and whether its error notices are on, off or muted; omit id for all your classifiers", Handler: t.status, Parameters: []toolspec.Parameter{
			{Name: "id", Type: "string", Description: "Classifier ID; omit for all"},
		}},
		{Name: "test", Description: "Classify one item without acting or storing anything: the category, reason, the action that would run and the system prompt with your Markdown instructions loaded", Handler: t.test, Parameters: []toolspec.Parameter{
			id,
			{Name: "item_id", Type: "string", Description: "The item's ID; omit for the first item the source lists"},
		}},
		{Name: "rerun", Description: "Classify one item again with the current definition and run its action", Handler: t.rerun, Parameters: []toolspec.Parameter{
			id,
			{Name: "item_id", Type: "string", Required: true, Description: "The item's ID"},
		}},
		{Name: "activity", Description: "A classifier's activity log, newest first: each item handled with its fields, category, reason and action, and each failed run with its error (the same error in a row is one entry with count, at and last_at)", Handler: t.activity, Parameters: []toolspec.Parameter{
			id,
			{Name: "limit", Type: "integer", Description: fmt.Sprintf("Entries to return; %d when omitted, at most %d", DefaultActivityLimit, MaxActivityLimit)},
			{Name: "offset", Type: "integer", Description: "Entries to skip"},
			{Name: "search", Type: "string", Description: "Only entries whose fields, item ID, category, reason, action arguments or error contain this text (case-insensitive)"},
			{Name: "outcome", Type: "string", Description: "Only entries with this outcome: " + strings.Join(ActivityOutcomes, ", ") + " (run_failed: failed runs)"},
		}},
	}
	for i := range defs {
		defs[i].Category = "classifier"
		switch defs[i].Name {
		case "create", "validate":
			defs[i].RawSchema = withArgSchema(defs[i].Parameters, "definition", DefinitionSchema())
		case "update":
			defs[i].RawSchema = withArgSchema(defs[i].Parameters, "changes", PatchSchema())
		}
		defs[i].Handler = t.refuseUnknownArgs(defs[i].Parameters, defs[i].Handler)
		if defs[i].Name != "readme" {
			defs[i].Description += ". " + readFirst
		}
	}
	return defs
}

// withArgSchema is the tool schema of params with the argument name
// described by schema, keeping that parameter's description.
func withArgSchema(params []toolspec.Parameter, name string, schema map[string]any) map[string]any {
	out := toolspec.ParametersToSchema(params)
	props, ok := out["properties"].(map[string]any)
	if !ok {
		panic("classifier: tool schema without properties")
	}
	arg := maps.Clone(schema)
	for _, p := range params {
		if p.Name == name {
			arg["description"] = p.Description
		}
	}
	props[name] = arg
	return out
}

type toolSuite struct {
	svc  *Service
	host ToolHost
}

// refuseUnknownArgs wraps a handler so a call with an argument the tool does
// not declare is refused in one sentence naming it.
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
		return errResult(msg), nil
	}
}

func errResult(msg string) *toolspec.Result { return &toolspec.Result{ForLLM: msg, IsError: true} }

// argError is a bad tool argument, shown to the agent as is.
type argError string

func (e argError) Error() string { return string(e) }

// fail renders an operation's error for the agent.
func (t *toolSuite) fail(scope Scope, id string, err error) (*toolspec.Result, error) {
	var ve *ValidationError
	var ae argError
	switch {
	case errors.As(err, &ae):
		return errResult(string(ae)), nil
	case errors.Is(err, ErrNotFound):
		return errResult(fmt.Sprintf("Classifier %s does not exist.", id)), nil
	case errors.Is(err, ErrExists):
		return errResult(fmt.Sprintf("Classifier %s already exists; use classifier_update to change it.", id)), nil
	case errors.Is(err, ErrItemNotFound):
		return errResult("The source does not list that item."), nil
	case errors.Is(err, ErrNoItems):
		return errResult("The source lists no items."), nil
	case errors.As(err, &ve):
		return errResult(ve.Error()), nil
	}
	t.svc.host.Logger.Errorf("classifier tool (agent %s, classifier %s): %v", scope.AgentID, id, err)
	return &toolspec.Result{ForLLM: "The operation failed because of an internal error.", IsError: true, Err: err}, nil
}

func jsonResult(v any) (*toolspec.Result, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return &toolspec.Result{ForLLM: string(data)}, nil
}

func stringArg(call *toolspec.ToolCall, name string, required bool) (string, error) {
	v, ok := call.Args[name]
	if !ok || v == nil {
		if required {
			return "", argError(name + " is required.")
		}
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", argError(name + " must be a string.")
	}
	s = strings.TrimSpace(s)
	if required && s == "" {
		return "", argError(name + " is required.")
	}
	return s, nil
}

func intArg(call *toolspec.ToolCall, name string) (int, error) {
	v, ok := call.Args[name]
	if !ok || v == nil {
		return 0, nil
	}
	f, ok := v.(float64)
	if !ok || f != math.Trunc(f) || f < 0 || f > math.MaxInt32 {
		return 0, argError(name + " must be a whole number, 0 or more.")
	}
	return int(f), nil
}

func objectArg(call *toolspec.ToolCall, name string) ([]byte, error) {
	v, ok := call.Args[name].(map[string]any)
	if !ok {
		return nil, argError(name + " must be a JSON object.")
	}
	return json.Marshal(v)
}

func (t *toolSuite) readme(call *toolspec.ToolCall) (*toolspec.Result, error) {
	if _, err := t.host.Scope(call); err != nil {
		return nil, err
	}
	return &toolspec.Result{ForLLM: guide(t.svc.host.ceilings())}, nil
}

func (t *toolSuite) list(call *toolspec.ToolCall) (*toolspec.Result, error) {
	scope, err := t.host.Scope(call)
	if err != nil {
		return nil, err
	}
	sums, err := t.svc.List(scope)
	if err != nil {
		return t.fail(scope, "", err)
	}
	return jsonResult(sums)
}

// withID runs op for a tool that names a classifier by its id argument.
func (t *toolSuite) withID(call *toolspec.ToolCall, op func(scope Scope, id string) (*toolspec.Result, error)) (*toolspec.Result, error) {
	scope, err := t.host.Scope(call)
	if err != nil {
		return nil, err
	}
	id, err := stringArg(call, "id", true)
	if err != nil {
		return t.fail(scope, "", err)
	}
	res, err := op(scope, id)
	if err != nil {
		return t.fail(scope, id, err)
	}
	return res, nil
}

func (t *toolSuite) get(call *toolspec.ToolCall) (*toolspec.Result, error) {
	return t.withID(call, func(scope Scope, id string) (*toolspec.Result, error) {
		raw, err := t.svc.Get(scope, id)
		if err != nil {
			return nil, err
		}
		return &toolspec.Result{ForLLM: string(raw)}, nil
	})
}

func (t *toolSuite) create(call *toolspec.ToolCall) (*toolspec.Result, error) {
	scope, err := t.host.Scope(call)
	if err != nil {
		return nil, err
	}
	raw, err := objectArg(call, "definition")
	if err != nil {
		return t.fail(scope, "", err)
	}
	var head struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(raw, &head) != nil {
		head.ID = ""
	}
	id, err := t.svc.Create(call.Ctx, scope, raw)
	if err != nil {
		return t.fail(scope, head.ID, err)
	}
	return &toolspec.Result{ForLLM: fmt.Sprintf("Classifier %s created.", id)}, nil
}

func (t *toolSuite) update(call *toolspec.ToolCall) (*toolspec.Result, error) {
	return t.withID(call, func(scope Scope, id string) (*toolspec.Result, error) {
		patch, err := objectArg(call, "changes")
		if err != nil {
			return nil, err
		}
		if err := t.svc.Update(call.Ctx, scope, id, patch); err != nil {
			return nil, err
		}
		return &toolspec.Result{ForLLM: fmt.Sprintf("Classifier %s updated.", id)}, nil
	})
}

func (t *toolSuite) delete(call *toolspec.ToolCall) (*toolspec.Result, error) {
	return t.withID(call, func(scope Scope, id string) (*toolspec.Result, error) {
		if err := t.svc.Delete(call.Ctx, scope, id); err != nil {
			return nil, err
		}
		return &toolspec.Result{ForLLM: fmt.Sprintf("Classifier %s deleted.", id)}, nil
	})
}

func (t *toolSuite) validate(call *toolspec.ToolCall) (*toolspec.Result, error) {
	scope, err := t.host.Scope(call)
	if err != nil {
		return nil, err
	}
	raw, err := objectArg(call, "definition")
	if err != nil {
		return t.fail(scope, "", err)
	}
	if err := t.svc.Validate(call.Ctx, scope, raw, true); err != nil {
		return t.fail(scope, "", err)
	}
	return &toolspec.Result{ForLLM: "The definition is valid."}, nil
}

func (t *toolSuite) status(call *toolspec.ToolCall) (*toolspec.Result, error) {
	scope, err := t.host.Scope(call)
	if err != nil {
		return nil, err
	}
	id, err := stringArg(call, "id", false)
	if err != nil {
		return t.fail(scope, "", err)
	}
	statuses, err := t.svc.Status(scope, id)
	if err != nil {
		return t.fail(scope, id, err)
	}
	if id != "" && len(statuses) == 1 {
		return jsonResult(statuses[0])
	}
	return jsonResult(statuses)
}

func (t *toolSuite) test(call *toolspec.ToolCall) (*toolspec.Result, error) {
	return t.withID(call, func(scope Scope, id string) (*toolspec.Result, error) {
		itemID, err := stringArg(call, "item_id", false)
		if err != nil {
			return nil, err
		}
		d, err := t.svc.Test(call.Ctx, scope, id, itemID)
		if err != nil {
			return nil, err
		}
		return jsonResult(d)
	})
}

func (t *toolSuite) rerun(call *toolspec.ToolCall) (*toolspec.Result, error) {
	return t.withID(call, func(scope Scope, id string) (*toolspec.Result, error) {
		itemID, err := stringArg(call, "item_id", true)
		if err != nil {
			return nil, err
		}
		d, err := t.svc.Rerun(call.Ctx, scope, id, itemID)
		if err != nil {
			return nil, err
		}
		return jsonResult(d)
	})
}

func (t *toolSuite) activity(call *toolspec.ToolCall) (*toolspec.Result, error) {
	return t.withID(call, func(scope Scope, id string) (*toolspec.Result, error) {
		var q ActivityQuery
		var err error
		if q.Limit, err = intArg(call, "limit"); err != nil {
			return nil, err
		}
		if q.Offset, err = intArg(call, "offset"); err != nil {
			return nil, err
		}
		if q.Search, err = stringArg(call, "search", false); err != nil {
			return nil, err
		}
		if q.Outcome, err = stringArg(call, "outcome", false); err != nil {
			return nil, err
		}
		entries, err := t.svc.Activity(scope, id, q)
		if err != nil {
			return nil, err
		}
		return jsonResult(entries)
	})
}
