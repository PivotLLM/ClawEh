// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

// Package session mounts the session tools from the sessiontools package as a
// ClawEh tool provider. The package defines the tools with BARE names
// ("messages", "search", ...); the aggregator publishes them under the
// "session" namespace as "session_messages" and so on.
//
// This file is the only ClawEh-specific glue: it recovers the agent loop's
// closures and workspace from tools.ToolDeps, builds a sessiontools.Host, and
// routes the tools' diagnostics into ClawEh's logger.
package session

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"

	sessiontools "github.com/PivotLLM/ctxengine/tools"

	"github.com/PivotLLM/ClawEh/app"
	"github.com/PivotLLM/ClawEh/constants"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/tools"
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
	// (Describe) passes a zero Deps, so cd is the zero ToolDeps: the sessions
	// directory is empty and the closures are nil, and every handler reports
	// itself unavailable instead of touching disk or the agent loop.
	var cd tools.ToolDeps
	if v, ok := deps.Host.(tools.ToolDeps); ok {
		cd = v
	}

	host := sessiontools.Host{
		Compact: cd.CompactFn,
		Clear:   refuseClearInAsk(cd.ClearFn),
		Log:     logToClaw,
	}
	if dir := cd.EffectiveStateDir(); dir != "" {
		host.SessionsDir = filepath.Join(dir, "sessions")
	}
	if cd.SessionInfoFn != nil {
		host.SessionInfo = func(ctx context.Context, sessionKey string) (*sessiontools.SessionInfo, error) {
			info, err := cd.SessionInfoFn(ctx, sessionKey)
			if err != nil || info == nil {
				return info, err
			}
			// Static server identity so "connection info" reports the running build.
			info.Server = app.Name() + " " + app.Version()
			info.OS = runtime.GOOS + "/" + runtime.GOARCH
			return info, nil
		}
	}
	return sessiontools.Definitions(host)
}

// errClearInAsk refuses session_clear in an asked turn: the conversation is
// the agent's one conversation, and clearing it would hand the asker's message
// to a fresh turn the asker is not waiting for.
var errClearInAsk = errors.New("session_clear is not available while answering another agent's message")

// refuseClearInAsk wraps clear so it is refused in an asked turn (its tool
// channel is the ask), in-process and over MCP alike. nil stays nil.
func refuseClearInAsk(clearFn func(ctx context.Context, sessionKey, message string) error) func(ctx context.Context, sessionKey, message string) error {
	if clearFn == nil {
		return nil
	}
	return func(ctx context.Context, sessionKey, message string) error {
		if tools.ToolChannel(ctx) == constants.AgentMessageChannel {
			return errClearInAsk
		}
		return clearFn(ctx, sessionKey, message)
	}
}

// logToClaw routes sessiontools diagnostics into ClawEh's logger.
func logToClaw(level, message string, fields map[string]any) {
	const component = "session-tools"
	switch level {
	case "debug":
		logger.DebugCF(component, message, fields)
	case "warn":
		logger.WarnCF(component, message, fields)
	case "error":
		logger.ErrorCF(component, message, fields)
	default:
		logger.InfoCF(component, message, fields)
	}
}
