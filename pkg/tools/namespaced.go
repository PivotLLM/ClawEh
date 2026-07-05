// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

// This file bridges the transport-neutral pkg/global tool layer into Claw's
// existing tools.Tool / tools.ToolProvider world. A tool package implements
// global.ToolProvider with BARE tool names; NamespacedProvider mounts it under a
// namespace, so the published tool name is "<namespace>_<bare>". The wrappers
// satisfy the legacy interfaces (Tool, AsyncExecutor, SessionScoped) so the
// registry, MCP host, and agent loop need no changes — global-based packages and
// legacy packages coexist during the migration.

package tools

import (
	"context"

	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/global"
)

// ---- Result conversion -----------------------------------------------------

func resultFromGlobal(r *global.Result, err error) *ToolResult {
	if r == nil {
		if err != nil {
			return ErrorResult(err.Error()).WithError(err)
		}
		return ErrorResult("tool returned no result")
	}
	out := &ToolResult{
		ForLLM:  r.ForLLM,
		ForUser: r.ForUser,
		Silent:  r.Silent,
		IsError: r.IsError,
		Async:   r.Async,
		Media:   r.Media,
		Err:     r.Err,
	}
	if err != nil {
		out.IsError = true
		if out.Err == nil {
			out.Err = err
		}
		if out.ForLLM == "" {
			out.ForLLM = err.Error()
		}
	}
	return out
}

// ResultToGlobal converts a legacy *ToolResult into a *global.Result. It is used
// by packages migrating to global handlers that still call into existing
// tools.Tool logic during the transition.
func ResultToGlobal(r *ToolResult) *global.Result {
	if r == nil {
		return &global.Result{IsError: true, ForLLM: "tool returned no result"}
	}
	return &global.Result{
		ForLLM:  r.ForLLM,
		ForUser: r.ForUser,
		Silent:  r.Silent,
		IsError: r.IsError,
		Async:   r.Async,
		Media:   r.Media,
		Err:     r.Err,
	}
}

// callFromCtx assembles a global.ToolCall from the execution context + args.
// Channel/chatID/session are read from the values the registry and MCP dispatcher
// inject (WithToolContext / WithSessionKey), so global handlers read them off the
// call instead of digging through context.
func callFromCtx(ctx context.Context, args map[string]any, notify func(*global.Result)) *global.ToolCall {
	return &global.ToolCall{
		Ctx:     ctx,
		Args:    args,
		Session: ToolSessionKey(ctx),
		Channel: ToolChannel(ctx),
		ChatID:  ToolChatID(ctx),
		Notify:  notify,
	}
}

// ---- Tool wrappers ---------------------------------------------------------
//
// Distinct concrete types are required so the registry's optional-interface type
// assertions (tool.(AsyncExecutor) / tool.(SessionScoped)) reflect the
// definition's flags rather than always succeeding.

type nsBase struct {
	ns  string
	def global.ToolDefinition
}

func (t nsBase) Name() string {
	// An empty namespace publishes the tool under its own (already-unique) name,
	// with no "<ns>_" prefix — see BareNamespacedProvider.
	if t.ns == "" {
		return t.def.Name
	}
	return t.ns + "_" + t.def.Name
}
func (t nsBase) Description() string { return t.def.Description }
func (t nsBase) Parameters() map[string]any {
	return t.def.Schema()
}
func (t nsBase) Execute(ctx context.Context, args map[string]any) *ToolResult {
	return resultFromGlobal(t.def.Handler(callFromCtx(ctx, args, nil)))
}

// IsPrimaryOnly propagates ToolDefinition.PrimaryOnly to all wrapper variants
// (they embed nsBase), so sub-agent registries can exclude these tools.
func (t nsBase) IsPrimaryOnly() bool { return t.def.PrimaryOnly }

type nsAsync struct{ nsBase }

func (t nsAsync) ExecuteAsync(ctx context.Context, args map[string]any, cb AsyncCallback) *ToolResult {
	notify := func(r *global.Result) {
		if cb != nil {
			cb(ctx, resultFromGlobal(r, nil))
		}
	}
	return resultFromGlobal(t.def.Handler(callFromCtx(ctx, args, notify)))
}

type nsSession struct{ nsBase }

func (t nsSession) IsSessionScoped() bool { return true }

type nsAsyncSession struct{ nsAsync }

func (t nsAsyncSession) IsSessionScoped() bool { return true }

// wrapGlobalTool selects the concrete wrapper matching the definition's flags.
func wrapGlobalTool(ns string, def global.ToolDefinition) Tool {
	b := nsBase{ns: ns, def: def}
	switch {
	case def.Async && def.SessionScoped:
		return nsAsyncSession{nsAsync{b}}
	case def.Async:
		return nsAsync{b}
	case def.SessionScoped:
		return nsSession{b}
	default:
		return b
	}
}

// ---- Provider adapter ------------------------------------------------------

// namespacedProvider adapts a global.ToolProvider into a tools.ToolProvider,
// applying the namespace prefix to every tool it produces.
type namespacedProvider struct {
	ns string
	p  global.ToolProvider
	// bare suppresses the "<ns>_" tool-name prefix while keeping ns as the
	// suite/catalog identity — for providers whose tool names are already
	// globally unique (e.g. fusion's service-prefixed names). See toolName.
	bare bool
}

// NamespacedProvider mounts a global.ToolProvider under ns. The resulting
// provider plugs into RegisterProvider/GetProviders exactly like a legacy one.
func NamespacedProvider(ns string, p global.ToolProvider) ToolProvider {
	return namespacedProvider{ns: ns, p: p}
}

// BareNamespacedProvider is like NamespacedProvider but publishes each tool under
// its own bare name (no "<ns>_" prefix). ns still groups the tools as a suite and
// names the catalog/config entry; the caller guarantees the tool names are
// globally unique.
func BareNamespacedProvider(ns string, p global.ToolProvider) ToolProvider {
	return namespacedProvider{ns: ns, p: p, bare: true}
}

// toolName is the published name of a bare tool: bare mode uses it verbatim,
// otherwise it gets the "<ns>_" prefix.
func (a namespacedProvider) toolName(bare string) string {
	if a.bare {
		return bare
	}
	return a.ns + "_" + bare
}

// namePrefix is the "<ns>" passed to wrapGlobalTool ("" in bare mode).
func (a namespacedProvider) namePrefix() string {
	if a.bare {
		return ""
	}
	return a.ns
}

func (a namespacedProvider) Namespace() string { return a.ns }

func (a namespacedProvider) Description() string {
	if m, ok := a.p.(global.HostMeta); ok {
		return m.Description()
	}
	return a.ns
}

func (a namespacedProvider) Category() string  { return a.ns }
func (a namespacedProvider) ConfigKey() string { return a.ns }

// Suite reports the all-or-nothing suite name when the wrapped provider declares
// itself one (cogmem, maestro); "" otherwise.
func (a namespacedProvider) Suite() string {
	if sp, ok := a.p.(SuiteProvider); ok {
		return sp.Suite()
	}
	return ""
}

func (a namespacedProvider) Available(cfg *config.Config) (bool, string) {
	if m, ok := a.p.(global.HostMeta); ok {
		return m.Available(cfg)
	}
	return true, ""
}

func (a namespacedProvider) Build(deps ToolDeps) []Tool {
	suite := a.Suite()
	// All-or-nothing suite: gated as a unit by the per-agent flag. When enabled,
	// every tool is registered (bypassing the per-tool allow/deny gate — the flag
	// IS the allow decision); when disabled, none are. The provider's RegisterTools
	// already returns nothing for a disabled agent, but gate here too so the suite
	// is never subject to the per-tool ToolEnabled filter.
	if suite != "" {
		if deps.Cfg != nil && !deps.Cfg.AgentSuiteEnabled(deps.AgentID, suite) {
			return nil
		}
		defs := a.p.RegisterTools(a.toGlobalDeps(deps))
		out := make([]Tool, 0, len(defs))
		for _, d := range defs {
			out = append(out, wrapGlobalTool(a.namePrefix(), d))
		}
		return out
	}

	defs := a.p.RegisterTools(a.toGlobalDeps(deps))
	out := make([]Tool, 0, len(defs))
	for _, d := range defs {
		// Per-tool enabled gate: an explicit override wins, else the tool's own
		// default-allow. Default-deny tools are not registered unless opted in.
		if deps.Cfg != nil && !deps.Cfg.Tools.ToolEnabled(a.toolName(d.Name), d.DefaultAllowed()) {
			continue
		}
		out = append(out, wrapGlobalTool(a.namePrefix(), d))
	}
	return out
}

func (a namespacedProvider) Describe() []ToolDescriptor {
	// All-or-nothing suites collapse to a single GUI entry (managed by the agent
	// toggle), so the per-tool catalog is not cluttered with the whole suite.
	if suite := a.Suite(); suite != "" {
		desc := a.ns
		if m, ok := a.p.(global.HostMeta); ok {
			desc = m.Description()
		}
		return []ToolDescriptor{{
			Name:        a.ns,
			Description: desc,
			Category:    a.ns,
			ConfigKey:   a.ns,
			Suite:       suite,
		}}
	}

	// Enumeration is deps-free: global providers must return their full tool set
	// from RegisterTools and build handler closures lazily (no eager deref of
	// deps), so a zero Deps is safe for cataloguing.
	defs := a.p.RegisterTools(global.Deps{})
	out := make([]ToolDescriptor, 0, len(defs))
	for _, d := range defs {
		category := d.Category
		if category == "" {
			category = a.ns
		}
		configKey := d.ConfigKey
		if configKey == "" {
			configKey = a.ns + "_" + d.Name
		}
		out = append(out, ToolDescriptor{
			Name:           a.ns + "_" + d.Name,
			Description:    d.Description,
			Category:       category,
			ConfigKey:      configKey,
			DefaultEnabled: d.DefaultAllowed(), // default-deny: only explicit DefaultAllow(true)
		})
	}
	return out
}

// toGlobalDeps converts Claw's rich ToolDeps into the portable global.Deps,
// passing the full ToolDeps through Host so converted packages can recover their
// strongly-typed dependencies via deps.Host.(tools.ToolDeps).
func (a namespacedProvider) toGlobalDeps(deps ToolDeps) global.Deps {
	return global.Deps{
		Cfg:       deps.Cfg,
		AgentID:   deps.AgentID,
		Workspace: deps.Workspace,
		Spawn:     deps.Spawn,
		Host:      deps,
	}
}
