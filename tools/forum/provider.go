// ClawEh
// License: MIT

// Package forum mounts the forum package's tools under the "forum" namespace
// (forum_models, forum_launch, ...), gated by the agent's `forum` switch, and
// supplies the per-agent forum.ToolHost: the caller's scope
// (<workspace>/forums), the refusal inside a forum turn, and the agent's own
// file-read permissions for configuration and source files.
package forum

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/constants"
	forumpkg "github.com/PivotLLM/ClawEh/forum"
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/tools"
	toolsagents "github.com/PivotLLM/ClawEh/tools/agents"
	"github.com/PivotLLM/ClawEh/tools/files"
)

// Suite is the forum suite's name: the per-agent switch (`forum`) and the
// tool-name prefix.
const Suite = "forum"

// BaseDirName is the directory under an agent's workspace that holds its
// forums.
const BaseDirName = "forums"

// service is the process's one forum service (SetService).
var service atomic.Pointer[forumpkg.Service]

// SetService installs the forum service the tools run on. The gateway calls
// it once, before the agent loop builds any agent's tools; without it no
// agent gets forum tools.
func SetService(svc *forumpkg.Service) { service.Store(svc) }

// GlobalProvider mounts the forum tools. It is an all-or-nothing suite gated
// by the per-agent `forum` switch (default off).
var GlobalProvider globalForumProvider

type globalForumProvider struct{}

func (globalForumProvider) Namespace() string { return Suite }
func (globalForumProvider) Description() string {
	return "Forums: set up and run structured discussions among agents"
}
func (globalForumProvider) Available(any) (bool, string) { return true, "" }

// Suite marks the forum tools as a suite gated by the agent's `forum` switch.
func (globalForumProvider) Suite() string { return Suite }

func (globalForumProvider) RegisterTools(deps global.Deps) []global.ToolDefinition {
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
		// A forum participant never gets forum tools (Scope refuses them
		// too, should one be called through another path).
		return nil
	}
	svc := service.Load()
	if svc == nil {
		// Only the gateway runs forums (`claw agent` has no service).
		logger.DebugCF("forum", "no forum service in this process; no forum tools",
			map[string]any{"agent": deps.AgentID})
		return nil
	}
	if host.Workspace == "" {
		logger.WarnCF("forum", "no workspace for agent; forum tools disabled",
			map[string]any{"agent": deps.AgentID})
		return nil
	}
	return forumpkg.Tools(svc, &toolHost{
		cfg:       c,
		agentID:   deps.AgentID,
		workspace: host.Workspace,
		purpose:   host.TempPurpose,
	})
}

// toolHost is forum.ToolHost for one agent instance. agentID is the identity
// its tools act as (a clone's is its source's); purpose marks a temporary
// agent a forum created.
type toolHost struct {
	cfg       *config.Config
	agentID   string
	workspace string
	purpose   string
}

var _ forumpkg.ToolHost = (*toolHost)(nil)

// Scope refuses every call from inside a forum turn (a forum participant
// created by a forum, or a turn at the maximum sub-agent depth, where every
// forum ask runs) with forum.ErrForumTurn, and an agent without the switch;
// otherwise it is the agent and its <workspace>/forums.
func (h *toolHost) Scope(call *global.ToolCall) (forumpkg.Scope, error) {
	if h.purpose == tools.TempPurposeForum {
		return forumpkg.Scope{}, fmt.Errorf("agent %s is a forum participant: %w", h.agentID, forumpkg.ErrForumTurn)
	}
	if call != nil && call.Ctx != nil {
		if depth, limit := toolsagents.SpawnDepth(call.Ctx), h.cfg.Agents.Defaults.GetMaxSubagentDepth(); depth >= limit {
			return forumpkg.Scope{}, fmt.Errorf("agent %s at sub-agent depth %d of %d: %w", h.agentID, depth, limit, forumpkg.ErrForumDepth)
		}
	}
	if !h.cfg.AgentSuiteEnabled(h.agentID, Suite) {
		return forumpkg.Scope{}, fmt.Errorf("forum tools are not enabled for agent %s", h.agentID)
	}
	base, err := filepath.Abs(filepath.Join(h.workspace, BaseDirName))
	if err != nil {
		return forumpkg.Scope{}, fmt.Errorf("forum directory of agent %s: %w", h.agentID, err)
	}
	return forumpkg.Scope{AgentID: h.agentID, BaseDirectory: base}, nil
}

// ResolveFile resolves ref the way the agent's file tools would read it.
func (h *toolHost) ResolveFile(agentID, ref string) (string, error) {
	if err := h.same(agentID); err != nil {
		return "", err
	}
	abs, err := files.NewReader(h.cfg, h.workspace).Resolve(ref)
	if err != nil {
		return "", errors.New("the agent may not read it or it does not exist")
	}
	return abs, nil
}

// ReadAllowed reports whether the agent's file tools may read absPath.
func (h *toolHost) ReadAllowed(agentID, absPath string) error {
	if err := h.same(agentID); err != nil {
		return err
	}
	err := files.NewReader(h.cfg, h.workspace).Allowed(absPath)
	switch {
	case err == nil, errors.Is(err, fs.ErrNotExist):
		// Permission holds; a missing file is the caller's to report.
		return nil
	default:
		return errors.New("its file permissions do not allow it")
	}
}

// Remote reports whether the call is part of work that began on a remote
// chat: the turn carries the remote-origin mark, or the call came on a
// channel that is not internal (an empty channel counts as remote).
func (h *toolHost) Remote(call *global.ToolCall) bool {
	if call == nil {
		return true
	}
	if call.Ctx != nil && tools.RemoteOrigin(call.Ctx) {
		return true
	}
	return !constants.IsInternalChannel(strings.TrimSpace(call.Channel))
}

// Workspace is the agent's workspace.
func (h *toolHost) Workspace(agentID string) (string, error) {
	if err := h.same(agentID); err != nil {
		return "", err
	}
	return h.workspace, nil
}

// same refuses a question about any agent but the one the host is for.
func (h *toolHost) same(agentID string) error {
	if !strings.EqualFold(strings.TrimSpace(agentID), h.agentID) {
		return fmt.Errorf("agent %s is not agent %s", agentID, h.agentID)
	}
	return nil
}
