// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

// The transport-neutral tool-definition layer now lives in its own standalone,
// dependency-free module, github.com/PivotLLM/toolspec, so tool packages and
// external hosts (Maestro, MCPFusion) can comply with the ClawEh tool interface
// without import cycles. This file re-exports it under the historical pkg/global
// names via type aliases, so existing call sites (global.ToolDefinition, etc.)
// keep working unchanged. Add new code against either name — they are identical
// types.

package global

import "github.com/PivotLLM/toolspec"

// Tool-contract types — aliases of the toolspec module (identical types).
type (
	Parameter      = toolspec.Parameter
	ToolHints      = toolspec.ToolHints
	Result         = toolspec.Result
	ToolCall       = toolspec.ToolCall
	ToolHandler    = toolspec.ToolHandler
	ToolDefinition = toolspec.ToolDefinition
	Deps           = toolspec.Deps
	ToolProvider   = toolspec.ToolProvider
	HostMeta       = toolspec.HostMeta
)

// Helper functions — re-exported from toolspec.
var (
	// Allow is a convenience for setting DefaultAllow in a literal:
	// DefaultAllow: global.Allow(true).
	Allow = toolspec.Allow
	// ParametersToSchema renders []Parameter as a JSON Schema object.
	ParametersToSchema = toolspec.ParametersToSchema
)
