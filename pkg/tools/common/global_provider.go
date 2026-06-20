// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

// Package common exposes a shared "common" directory that agents can read from
// and write to. The directory is global (one per deployment, resolved by
// Config.ResolveCommonDir); the tools copy files between it and the calling
// agent's workspace. Per-agent access is gated by AgentConfig.SharesCommon.
package common

import (
	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/global"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// GlobalProvider exposes the shared-directory tools through the transport-neutral
// global layer with BARE names ("list", "get", "put", "delete"). The aggregator
// mounts it under the "common" namespace, so the published names are
// "common_list" / "common_get" / "common_put" / "common_delete".
var GlobalProvider globalCommonProvider

type globalCommonProvider struct{}

// Namespace/Description/Available satisfy global.HostMeta.
func (globalCommonProvider) Namespace() string { return "common" }
func (globalCommonProvider) Description() string {
	return "Shared common-directory read/write operations"
}

func (globalCommonProvider) Available(cfg any) (bool, string) { return true, "" }

func (globalCommonProvider) RegisterTools(deps global.Deps) []global.ToolDefinition {
	cd, _ := deps.Host.(tools.ToolDeps)
	c, _ := deps.Cfg.(*config.Config)

	// Per-agent gate: an agent that does not share the common directory gets no
	// common tools at all. cd.AgentCfg is nil for the default agent (shares).
	if cd.AgentCfg != nil && !cd.AgentCfg.SharesCommon() {
		return nil
	}

	var commonDir, workspace string
	if c != nil {
		commonDir = c.ResolveCommonDir()
		workspace = cd.Workspace
	}

	return []global.ToolDefinition{
		{
			Name:         "list",
			Description:  "List files in the shared common directory (optionally under a subdir).",
			RawSchema:    listSchema,
			Category:     "common",
			DefaultAllow: global.Allow(true),
			Handler: func(call *global.ToolCall) (*global.Result, error) {
				return listCommon(commonDir, call.Args), nil
			},
		},
		{
			Name:         "get",
			Description:  "Copy a file from the shared common directory into this agent's workspace files/ directory.",
			RawSchema:    getSchema,
			Category:     "common",
			DefaultAllow: global.Allow(true),
			Handler: func(call *global.ToolCall) (*global.Result, error) {
				return getCommon(commonDir, workspace, call.Args), nil
			},
		},
		{
			Name:         "put",
			Description:  "Copy a file from this agent's workspace into the shared common directory.",
			RawSchema:    putSchema,
			Category:     "common",
			DefaultAllow: global.Allow(true),
			Handler: func(call *global.ToolCall) (*global.Result, error) {
				return putCommon(commonDir, workspace, call.Args), nil
			},
		},
		{
			Name:         "delete",
			Description:  "Delete a file from the shared common directory.",
			RawSchema:    deleteSchema,
			Category:     "common",
			DefaultAllow: global.Allow(true),
			Handler: func(call *global.ToolCall) (*global.Result, error) {
				return deleteCommon(commonDir, call.Args), nil
			},
		},
	}
}

var listSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"subdir": map[string]any{
			"type":        "string",
			"description": "Optional subdirectory under the common directory to list.",
		},
	},
}

var getSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"name": map[string]any{
			"type":        "string",
			"description": "File name (or relative path) within the common directory to copy.",
		},
		"as": map[string]any{
			"type":        "string",
			"description": "Optional destination name (relative path) under the workspace files/ directory. Defaults to the source basename.",
		},
	},
	"required": []string{"name"},
}

var putSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"path": map[string]any{
			"type":        "string",
			"description": "Relative path within the agent workspace to copy into the common directory.",
		},
		"as": map[string]any{
			"type":        "string",
			"description": "Optional destination name (relative path) within the common directory. Defaults to the source basename.",
		},
	},
	"required": []string{"path"},
}

var deleteSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"name": map[string]any{
			"type":        "string",
			"description": "File name (or relative path) within the common directory to delete.",
		},
	},
	"required": []string{"name"},
}
