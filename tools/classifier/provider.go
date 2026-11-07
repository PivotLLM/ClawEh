// ClawEh
// License: MIT

// Package classifier mounts the classifier package's tools under the
// "classifier" namespace (classifier_list, classifier_create, ...), gated by
// the agent's `classifiers` switch, and supplies the per-agent
// classifier.ToolHost: the caller's scope (<workspace>/classifiers), which a
// clone shares with the agent it copies.
package classifier

import (
	"fmt"
	"path/filepath"
	"sync/atomic"

	classifierpkg "github.com/PivotLLM/ClawEh/classifier"
	"github.com/PivotLLM/ClawEh/classifier/classifierfs"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/tools"
)

// Suite is the per-agent switch that gates the classifier tools
// (`classifiers`).
const Suite = "classifiers"

// Namespace is the classifier tools' name prefix (classifier_list, ...).
const Namespace = "classifier"

// service is the process's one classifier service (SetService).
var service atomic.Pointer[classifierpkg.Service]

// SetService installs the classifier service the tools run on. The gateway
// calls it once, before the agent loop builds any agent's tools; without it
// no agent gets classifier tools.
func SetService(svc *classifierpkg.Service) { service.Store(svc) }

// GlobalProvider mounts the classifier tools. It is an all-or-nothing suite
// gated by the per-agent `classifiers` switch (default off).
var GlobalProvider globalClassifierProvider

type globalClassifierProvider struct{}

func (globalClassifierProvider) Namespace() string { return Namespace }
func (globalClassifierProvider) Description() string {
	return "Classifiers: sort items from a tool with one model call each"
}
func (globalClassifierProvider) Available(any) (bool, string) { return true, "" }

// Suite marks the classifier tools as a suite gated by the agent's
// `classifiers` switch.
func (globalClassifierProvider) Suite() string { return Suite }

func (globalClassifierProvider) RegisterTools(deps global.Deps) []global.ToolDefinition {
	c, ok := deps.Cfg.(*config.Config)
	if !ok || c == nil {
		// Enumeration pass: the suite is one toggle, not catalogue entries.
		return nil
	}
	if !c.AgentSuiteEnabled(deps.AgentID, Suite) {
		return nil
	}
	host, ok := deps.Host.(tools.ToolDeps)
	if !ok || host.TempPurpose == tools.TempPurposeForum {
		// A forum participant never gets classifier tools.
		return nil
	}
	svc := service.Load()
	if svc == nil {
		// Only the gateway runs classifiers (`claw agent` has no service).
		logger.DebugCF("classifier", "no classifier service in this process; no classifier tools",
			map[string]any{"agent": deps.AgentID})
		return nil
	}
	if host.Workspace == "" {
		logger.WarnCF("classifier", "no workspace for agent; classifier tools disabled",
			map[string]any{"agent": deps.AgentID})
		return nil
	}
	return classifierpkg.Tools(svc, &toolHost{
		cfg:       c,
		agentID:   deps.AgentID,
		workspace: host.Workspace,
		purpose:   host.TempPurpose,
	})
}

// toolHost is classifier.ToolHost for one agent instance. agentID is the
// identity its tools act as (a clone's is its source's), workspace the
// agent's (a clone shares its source's); purpose marks a temporary agent a
// forum created.
type toolHost struct {
	cfg       *config.Config
	agentID   string
	workspace string
	purpose   string
}

var _ classifierpkg.ToolHost = (*toolHost)(nil)

// Scope refuses a call by a forum participant and by an agent without the
// `classifiers` switch; otherwise it is the agent and its
// <workspace>/classifiers.
func (h *toolHost) Scope(*global.ToolCall) (classifierpkg.Scope, error) {
	if h.purpose == tools.TempPurposeForum {
		return classifierpkg.Scope{}, fmt.Errorf("agent %s is a forum participant and has no classifiers", h.agentID)
	}
	if !h.cfg.AgentSuiteEnabled(h.agentID, Suite) {
		return classifierpkg.Scope{}, fmt.Errorf("classifier tools are not enabled for agent %s", h.agentID)
	}
	base, err := filepath.Abs(filepath.Join(h.workspace, classifierfs.BaseDirName))
	if err != nil {
		return classifierpkg.Scope{}, fmt.Errorf("classifier directory of agent %s: %w", h.agentID, err)
	}
	return classifierpkg.Scope{AgentID: h.agentID, BaseDirectory: base}, nil
}
