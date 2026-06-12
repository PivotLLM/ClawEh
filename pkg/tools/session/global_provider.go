// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

// This file exposes the session tools through the transport-neutral pkg/global
// layer with BARE names ("messages", "search", ...). The aggregator mounts the
// provider under the "session" namespace, so the published tool names are
// "session_messages", "session_search", etc. It reuses the existing
// SessionHistoryTool / SessionCompactTool / ... logic and converts the result at
// the boundary via tools.ResultToGlobal, so behaviour is unchanged.
//
// All session tools are session-scoped (they need ToolCall.Session populated),
// so every definition sets SessionScoped: true.

package session

import (
	"path/filepath"

	"github.com/PivotLLM/ClawEh/pkg/global"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// GlobalProvider exposes the session tools through the global layer.
var GlobalProvider globalSessionProvider

type globalSessionProvider struct{}

// Namespace/Description/Available satisfy global.HostMeta. Session tools are
// always available; per-tool gating happens via the config-driven enabled map.
func (globalSessionProvider) Namespace() string   { return "session" }
func (globalSessionProvider) Description() string { return "Session history and management tools" }

func (globalSessionProvider) Available(cfg any) (bool, string) { return true, "" }

func (globalSessionProvider) RegisterTools(deps global.Deps) []global.ToolDefinition {
	// Recover Claw's rich, strongly-typed dependencies. A deps-free enumeration
	// (Describe) passes a zero Deps, so cd is the zero ToolDeps and the closures
	// are nil — handlers guard on a nil instance and never run during cataloguing.
	cd, _ := deps.Host.(tools.ToolDeps)

	sessionsDir := filepath.Join(cd.Workspace, "sessions")

	// Archive-based tools: always available (only need the sessions dir).
	messages := NewSessionHistoryTool(sessionsDir)
	search := NewSessionHistorySearchTool(sessionsDir)
	summaryList := NewSessionSummaryListTool(sessionsDir)
	summaryGet := NewSessionSummaryGetTool(sessionsDir)

	// Closure-based tools: construct a real instance only when its closure is
	// present; otherwise leave nil and let the handler return a not-available
	// error. Static metadata (Description/Parameters) is read from a zero-value
	// instance, whose methods touch no fields.
	var compact *SessionCompactTool
	if cd.CompactFn != nil {
		compact = NewSessionCompactTool(cd.CompactFn)
	}
	var info *SessionInfoTool
	if cd.SessionInfoFn != nil {
		info = NewSessionInfoTool(SessionInfoFunc(cd.SessionInfoFn))
	}
	var clear *SessionClearTool
	if cd.ClearFn != nil {
		clear = NewSessionClearTool(cd.ClearFn)
	}

	return []global.ToolDefinition{
		{
			Name:          "messages",
			Description:   (&SessionHistoryTool{}).Description(),
			RawSchema:     (&SessionHistoryTool{}).Parameters(),
			SessionScoped: true,
			DefaultAllow:  global.Allow(true),
			Category:      "context",
			Handler: func(call *global.ToolCall) (*global.Result, error) {
				return tools.ResultToGlobal(messages.Execute(call.Ctx, call.Args)), nil
			},
		},
		{
			Name:          "search",
			Description:   (&SessionHistorySearchTool{}).Description(),
			RawSchema:     (&SessionHistorySearchTool{}).Parameters(),
			SessionScoped: true,
			DefaultAllow:  global.Allow(true),
			Category:      "context",
			Handler: func(call *global.ToolCall) (*global.Result, error) {
				return tools.ResultToGlobal(search.Execute(call.Ctx, call.Args)), nil
			},
		},
		{
			Name:          "summary_list",
			Description:   (&SessionSummaryListTool{}).Description(),
			RawSchema:     (&SessionSummaryListTool{}).Parameters(),
			SessionScoped: true,
			DefaultAllow:  global.Allow(true),
			Category:      "context",
			Handler: func(call *global.ToolCall) (*global.Result, error) {
				return tools.ResultToGlobal(summaryList.Execute(call.Ctx, call.Args)), nil
			},
		},
		{
			Name:          "summary_get",
			Description:   (&SessionSummaryGetTool{}).Description(),
			RawSchema:     (&SessionSummaryGetTool{}).Parameters(),
			SessionScoped: true,
			DefaultAllow:  global.Allow(true),
			Category:      "context",
			Handler: func(call *global.ToolCall) (*global.Result, error) {
				return tools.ResultToGlobal(summaryGet.Execute(call.Ctx, call.Args)), nil
			},
		},
		{
			Name:          "compact",
			Description:   (&SessionCompactTool{}).Description(),
			RawSchema:     (&SessionCompactTool{}).Parameters(),
			SessionScoped: true,
			DefaultAllow:  global.Allow(true),
			Category:      "context",
			Handler: func(call *global.ToolCall) (*global.Result, error) {
				if compact == nil {
					return &global.Result{IsError: true, ForLLM: "tool not available"}, nil
				}
				return tools.ResultToGlobal(compact.Execute(call.Ctx, call.Args)), nil
			},
		},
		{
			Name:          "info",
			Description:   (&SessionInfoTool{}).Description(),
			RawSchema:     (&SessionInfoTool{}).Parameters(),
			SessionScoped: true,
			DefaultAllow:  global.Allow(true),
			Category:      "context",
			Handler: func(call *global.ToolCall) (*global.Result, error) {
				if info == nil {
					return &global.Result{IsError: true, ForLLM: "tool not available"}, nil
				}
				return tools.ResultToGlobal(info.Execute(call.Ctx, call.Args)), nil
			},
		},
		{
			Name:          "clear",
			Description:   (&SessionClearTool{}).Description(),
			RawSchema:     (&SessionClearTool{}).Parameters(),
			SessionScoped: true,
			DefaultAllow:  nil, // denied by default — opt-in only
			Category:      "context",
			Handler: func(call *global.ToolCall) (*global.Result, error) {
				if clear == nil {
					return &global.Result{IsError: true, ForLLM: "tool not available"}, nil
				}
				return tools.ResultToGlobal(clear.Execute(call.Ctx, call.Args)), nil
			},
		},
	}
}
