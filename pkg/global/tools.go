// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

// This file defines the shared, transport-neutral tool-definition layer. It is
// deliberately dependency-free (only the standard library) so it can be imported
// by every tool package, by the registry/bridge in pkg/tools, and — eventually —
// by Maestro and MCPFusion as a standalone module without import cycles.
//
// A tool package implements ToolProvider and returns ToolDefinitions with BARE
// names (e.g. "read"). The aggregator that pulls a provider in supplies the
// namespace (e.g. "file"), and the published tool name is "<namespace>_<bare>"
// (e.g. "file_read"). Namespacing lives in the aggregator, not the tool package,
// so packages never collide and a package can be remounted under a new namespace
// without touching its code.

package global

import "context"

// Parameter describes one tool input with rich, declarative metadata. It is the
// single source of truth for a parameter; hosts derive their JSON Schema (and any
// SDK-specific tool options) from it via ParametersToSchema or a host adapter.
type Parameter struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Required    bool           `json:"required"`
	Type        string         `json:"type"` // string|number|integer|boolean|array|object
	Items       string         `json:"items,omitempty"`
	Default     any            `json:"default,omitempty"`
	Enum        []any          `json:"enum,omitempty"`
	Pattern     string         `json:"pattern,omitempty"`
	Minimum     *float64       `json:"minimum,omitempty"`
	Maximum     *float64       `json:"maximum,omitempty"`
	MinLength   *int           `json:"minLength,omitempty"`
	MaxLength   *int           `json:"maxLength,omitempty"`
	Format      string         `json:"format,omitempty"`
	Examples    []any          `json:"examples,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// ToolHints carries the MCP tool annotations. A nil pointer means "unspecified".
type ToolHints struct {
	ReadOnly    *bool
	Destructive *bool
	Idempotent  *bool
	OpenWorld   *bool
}

// Result is the structured return of a tool. It is a field-for-field superset of
// the legacy pkg/tools.ToolResult so nothing is lost as Claw adopts it; hosts
// that only want text read ForLLM.
type Result struct {
	ForLLM  string   `json:"for_llm"`            // required: model-facing content
	ForUser string   `json:"for_user,omitempty"` // optional: routed to the human by the host
	Silent  bool     `json:"silent,omitempty"`
	IsError bool     `json:"is_error,omitempty"`
	Async   bool     `json:"async,omitempty"`
	Media   []string `json:"media,omitempty"` // media-store refs (host resolves)
	Err     error    `json:"-"`               // internal detail; not the control-flow signal
}

// ToolCall bundles everything one invocation needs. It is per-call and never
// retained, so carrying Ctx in the struct follows the net/http.Request precedent
// (the accepted exception to "don't store context in a struct").
type ToolCall struct {
	Ctx     context.Context
	Args    map[string]any
	AgentID string
	Session string // resolved session key ("" if none)
	Channel string
	ChatID  string
	// Notify delivers a late result for async tools. nil ⇒ the host has no async
	// delivery path; an async tool must check for nil and degrade gracefully.
	Notify func(*Result)
}

// ToolHandler runs one invocation. Ctx is on the call; error is returned
// separately (idiomatic — keeps errors.Is/As working). Result.IsError/Err
// describe the user-facing outcome and are distinct from the returned error.
type ToolHandler func(call *ToolCall) (*Result, error)

// ToolDefinition is everything needed to expose one tool. Name is BARE (no
// namespace); the aggregator applies the namespace prefix.
type ToolDefinition struct {
	Name        string
	Description string
	Parameters  []Parameter
	// RawSchema is an optional pre-built JSON Schema (object). When set it is used
	// verbatim instead of deriving the schema from Parameters — a migration aid for
	// tools that already carry a hand-written schema. Prefer Parameters for new tools.
	RawSchema map[string]any
	Hints     *ToolHints
	Handler   ToolHandler

	// SessionScoped declares the tool needs ToolCall.Session populated.
	SessionScoped bool
	// Async declares the tool may use ToolCall.Notify and return Result.Async.
	Async bool

	// PrimaryOnly declares the tool is available only to a primary (top-level)
	// agent, never to a spawned sub-agent. Sub-agent tool registries exclude these
	// regardless of the per-agent allowlist, and execution rejects them as
	// defense-in-depth. Use for capabilities a worker must not have — e.g.
	// agent_spawn (prevents recursion), cron_schedule, and the cognitive-memory
	// WRITE tools (sub-agents get read-only memory).
	PrimaryOnly bool

	// DefaultAllow controls whether the tool is exposed to clients by default
	// (the default per-agent allowlist and the default MCP-host allowlist). It is
	// an optional bool so the safe default is **deny**: a tool that does not set
	// it (nil) — or sets it false — is NOT available to clients unless explicitly
	// allowed. A tool must opt in with a pointer to true to be on by default.
	DefaultAllow *bool

	// Host metadata (Claw uses these for GUI grouping + config gating; other
	// hosts may ignore them).
	Category  string
	ConfigKey string
}

// Schema returns the tool's JSON Schema: RawSchema verbatim when set, otherwise
// derived from Parameters.
func (d ToolDefinition) Schema() map[string]any {
	if d.RawSchema != nil {
		return d.RawSchema
	}
	return ParametersToSchema(d.Parameters)
}

// DefaultAllowed reports the effective default-allow decision: true only when
// DefaultAllow is explicitly set to true. nil or false ⇒ denied by default.
func (d ToolDefinition) DefaultAllowed() bool {
	return d.DefaultAllow != nil && *d.DefaultAllow
}

// Allow is a convenience for setting DefaultAllow in a literal: DefaultAllow: global.Allow(true).
func Allow(v bool) *bool { return &v }

// Deps is dependency injection handed to a provider at construction time.
// Capabilities a host may not offer are nilable; a provider whose tools need a
// nil capability should omit those tools.
type Deps struct {
	Cfg       any // *config.Config in Claw; hosts type-assert
	AgentID   string
	Workspace string
	// Spawn launches sub-agent workers. When non-nil it holds a global.Spawner;
	// recover it with sp, ok := deps.Spawn.(global.Spawner). nil ⇒ this host
	// cannot launch sub-agents. The host injects one robust spawner here so any
	// tool package (internal or external/MCP) can launch workers by DI.
	Spawn any
	// Host carries host-specific, strongly-typed dependencies (Claw passes its
	// rich tools.ToolDeps here; providers type-assert).
	Host any
}

// ToolProvider is the portable core: a package returns its tools (bare-named).
type ToolProvider interface {
	RegisterTools(deps Deps) []ToolDefinition
}

// HostMeta is optional provider metadata a host may use for config gating and
// GUI grouping. Claw implements it; minimal hosts can skip it.
type HostMeta interface {
	Namespace() string
	Description() string
	Available(cfg any) (ok bool, reason string)
}

// ParametersToSchema renders []Parameter as a JSON Schema object
// ({type, properties, required}). Hosts that take a raw schema (Claw's MCP host,
// LLM tool definitions) use this; SDK-specific hosts may build options instead.
func ParametersToSchema(params []Parameter) map[string]any {
	properties := make(map[string]any, len(params))
	var required []string
	for _, p := range params {
		prop := map[string]any{}
		if p.Type != "" {
			prop["type"] = p.Type
		} else {
			prop["type"] = "string"
		}
		if p.Description != "" {
			prop["description"] = p.Description
		}
		if len(p.Enum) > 0 {
			prop["enum"] = p.Enum
		}
		if p.Default != nil {
			prop["default"] = p.Default
		}
		if p.Pattern != "" {
			prop["pattern"] = p.Pattern
		}
		if p.Format != "" {
			prop["format"] = p.Format
		}
		if p.Minimum != nil {
			prop["minimum"] = *p.Minimum
		}
		if p.Maximum != nil {
			prop["maximum"] = *p.Maximum
		}
		if p.MinLength != nil {
			prop["minLength"] = *p.MinLength
		}
		if p.MaxLength != nil {
			prop["maxLength"] = *p.MaxLength
		}
		if len(p.Examples) > 0 {
			prop["examples"] = p.Examples
		}
		if p.Type == "array" {
			itemType := p.Items
			if itemType == "" {
				itemType = "string"
			}
			prop["items"] = map[string]any{"type": itemType}
		}
		properties[p.Name] = prop
		if p.Required {
			required = append(required, p.Name)
		}
	}
	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}
