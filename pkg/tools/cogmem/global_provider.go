// ClawEh - Cognitive Memory
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

// Package cogmem exposes the cognitive-memory store (pkg/cogmem/store) as a
// transport-neutral tool provider with BARE names ("domain_get", "memory_create",
// ...). The aggregator mounts the provider under the "cogmem" namespace, so the
// published tool names are "cogmem_domain_get", "cogmem_memory_create", etc.
// Names follow object_verb: domain_* (containers), memory_* (items), plus the
// subsystem tools status/explain/consolidate.
//
// Every tool is session-scoped: it operates on the per-session .cogmem.db
// resolved from the workspace and ToolCall.Session. Cognitive memory is ON by
// default (DefaultAllow true): every agent gets these tools unless its tool
// allowlist explicitly excludes them — i.e. cognitive memory is opt-OUT.
package cogmem

import (
	"github.com/PivotLLM/ClawEh/pkg/global"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// GlobalProvider exposes the cognitive-memory tools through the global layer.
var GlobalProvider globalCogmemProvider

type globalCogmemProvider struct{}

// Namespace/Description/Available satisfy global.HostMeta. The tools are always
// available (the store is created on demand); access is gated per-agent via the
// tool allowlist.
func (globalCogmemProvider) Namespace() string { return "cogmem" }
func (globalCogmemProvider) Description() string {
	return "Cognitive memory: durable domains and hooks"
}

func (globalCogmemProvider) Available(cfg any) (bool, string) { return true, "" }

// consolidateTrigger is an optional package-level hook the Phase 3 consolidation
// worker can install via SetConsolidateTrigger. When nil, the consolidate tool
// reports that the worker is not yet running.
var consolidateTrigger func(agentID, sessionKey string)

// SetConsolidateTrigger installs the (non-blocking) consolidation trigger. Phase
// 3 wires the worker here; until then the consolidate tool degrades gracefully.
func SetConsolidateTrigger(fn func(agentID, sessionKey string)) { consolidateTrigger = fn }

func (globalCogmemProvider) RegisterTools(deps global.Deps) []global.ToolDefinition {
	// Recover Claw's rich, strongly-typed dependencies. A deps-free enumeration
	// (Describe) passes a zero Deps, so cd is the zero ToolDeps and workspace is
	// empty — handlers guard on an empty session/workspace and never touch disk
	// during cataloguing.
	cd, _ := deps.Host.(tools.ToolDeps)
	workspace := cd.Workspace

	def := func(name, desc string, params []global.Parameter, allow bool, h handlerFunc) global.ToolDefinition {
		return global.ToolDefinition{
			Name:          name,
			Description:   desc,
			Parameters:    params,
			Category:      "memory",
			SessionScoped: true,
			DefaultAllow:  global.Allow(allow),
			Handler:       wrap(workspace, h),
		}
	}

	return []global.ToolDefinition{
		def("domain_get",
			"Load a domain by id, with its active memories as readable text (each memory's id included).",
			[]global.Parameter{
				{Name: "id", Type: "string", Required: true, Description: "Domain id (e.g. d3K9P)."},
			}, true, getDomain),

		def("memory_search",
			"Search your active memories by a word or phrase in their text (case-insensitive). Use it to look something up when the answer may be in memory but is not currently shown in your context.",
			[]global.Parameter{
				{Name: "query", Type: "string", Required: true, Description: "Word or phrase to match against memory text."},
				{Name: "limit", Type: "integer", Required: false, Description: "Max results (default 20)."},
			}, true, search),

		def("domain_list",
			"List memory domains (id, name, summary, type, status). Optionally filter by status and/or type. To answer \"what am I working on?\", list type=project.",
			[]global.Parameter{
				{Name: "status", Type: "string", Required: false, Description: "Filter by status.",
					Enum: []any{"active", "review", "archived"}},
				{Name: "type", Type: "string", Required: false, Description: "Filter by domain type.",
					Enum: []any{"project", "workflow", "general"}},
			}, true, listDomains),

		def("explain",
			"Summarize the status, source, and evidence of a domain or memory id.",
			[]global.Parameter{
				{Name: "id", Type: "string", Required: true, Description: "A domain id (d…) or memory id (h…)."},
			}, true, explain),

		def("memory_create",
			"Record a durable memory (a fact, preference, or rule). With NO domain_id and NO domain_hint it records to your always-on 'general' domain (global rules/preferences/facts that should always be in context). Give a domain_hint to create/use a project domain, or a domain_id to target a specific one.",
			[]global.Parameter{
				{Name: "domain_id", Type: "string", Required: false, Description: "Target domain id. If omitted and no domain_hint is given, records to the always-on general domain."},
				{Name: "domain_hint", Type: "string", Required: false, Description: "Name for a new project domain when domain_id is not given (omit to use the general domain)."},
				{Name: "type", Type: "string", Required: true, Description: "Memory type: fact (something true), preference (how the user likes things done), or rule (a hard directive).",
					Enum: []any{"fact", "preference", "rule"}},
				{Name: "text", Type: "string", Required: true, Description: "The memory content to store."},
				{Name: "confidence", Type: "number", Required: false, Description: "Confidence 0..1 (default 0.9)."},
				{Name: "status", Type: "string", Required: false, Description: "active (default), or review to hold it as pending (unconfirmed) until the user confirms.",
					Enum: []any{"active", "review"}},
			}, true, remember),

		def("domain_update",
			"Update a domain's summary, state (blockers / next actions / constraints), or triggers. Pass the current expected_version (from domain_get or domain_list) so you do not overwrite newer changes.",
			[]global.Parameter{
				{Name: "id", Type: "string", Required: true, Description: "Domain id."},
				{Name: "expected_version", Type: "integer", Required: true, Description: "The version you last read (from domain_get or domain_list); rejected if it is stale."},
				{Name: "set_summary", Type: "string", Required: false, Description: "Replace the domain summary."},
				{Name: "set_blockers", Type: "array", Items: "string", Required: false, Description: "Replace the blockers list."},
				{Name: "set_next_actions", Type: "array", Items: "string", Required: false, Description: "Replace the next-actions list."},
				{Name: "set_constraints", Type: "array", Items: "string", Required: false, Description: "Replace the constraints list."},
				{Name: "set_triggers", Type: "string", Required: false, Description: "Replace the tool triggers: a comma-separated list of patterns. This domain auto-loads whenever you use a tool whose name contains one of them — use short distinctive words wrapped in *, e.g. \"*mail*\" or \"*github*,*calendar*\". (The * are optional; \"mail\" and \"*mail*\" behave the same — matching is always \"contains\", case- and underscore-insensitive.) MCP tools work too: their names look like mcp_<server>_<tool>, so \"*github*\" matches that whole server. Empty string clears triggers."},
				{Name: "set_keyword_triggers", Type: "array", Items: "string", Required: false, Description: "Replace the keyword triggers: a list of distinctive words/phrases that load this domain when one appears in the incoming message text (e.g. a scheduled reminder or a user message). Matched as a whole phrase on word boundaries, so prefer multi-word phrases — [\"morning routine\",\"weekly report\"] — over common single words like \"morning\", which would match too often. Empty list clears them."},
			}, true, updateDomain),

		def("memory_retire",
			"Retire a memory so it is no longer used (it stays in the audit history). To change a memory, retire the old one and create a new one. Also use this to reject a pending (unconfirmed) memory when the user declines it.",
			[]global.Parameter{
				{Name: "id", Type: "string", Required: true, Description: "Memory id."},
				{Name: "reason", Type: "string", Required: true, Description: "Why it is being retired."},
			}, true, retireHook),

		def("memory_confirm",
			"Confirm a pending (unconfirmed) memory, promoting it from review to active so it is used in prompting. Call this when the user confirms a memory from the pending digest.",
			[]global.Parameter{
				{Name: "id", Type: "string", Required: true, Description: "Pending memory id (from the pending digest)."},
			}, true, confirmHook),

		def("domain_create",
			"Create a new memory domain and return its assigned id. Register each ongoing project as a 'project' domain so your project list stays complete.",
			[]global.Parameter{
				{Name: "type", Type: "string", Required: true, Description: "Domain type.",
					Enum: []any{"project", "workflow"}},
				{Name: "name", Type: "string", Required: true, Description: "Domain name."},
				{Name: "summary", Type: "string", Required: false, Description: "Optional one-line summary."},
				{Name: "triggers", Type: "string", Required: false, Description: "Optional tool triggers: a comma-separated list of patterns. This domain auto-loads whenever you use a tool whose name contains one of them — use short distinctive words wrapped in *, e.g. \"*mail*\" or \"*github*,*calendar*\". (The * are optional wildcards; \"mail\" and \"*mail*\" behave the same — matching is always \"contains\".) MCP tools work too: their names look like mcp_<server>_<tool>, so \"*github*\" matches every tool from the github server. Matching ignores case and treats _ and __ the same."},
				{Name: "keyword_triggers", Type: "array", Items: "string", Required: false, Description: "Optional keyword triggers: a list of distinctive words/phrases that load this domain when one appears in the incoming message text (e.g. a scheduled reminder, or what the user says). Matched as a whole phrase on word boundaries, so prefer multi-word phrases — [\"morning routine\",\"weekly report\"] — over common single words like \"morning\", which would match too often. Use this (not tool triggers) to have a workflow's context pulled up when a cron job fires."},
			}, true, createDomain),

		def("domain_archive",
			"Archive a domain so it is no longer used in prompting.",
			[]global.Parameter{
				{Name: "id", Type: "string", Required: true, Description: "Domain id."},
			}, true, archiveDomain),

		def("memory_forget",
			"Retire all active memories matching a query (optionally limited to one domain). Reports how many were retired.",
			[]global.Parameter{
				{Name: "query", Type: "string", Required: true, Description: "Substring to match against active memory text."},
				{Name: "domain_id", Type: "string", Required: false, Description: "Restrict to this domain."},
			}, true, forget),

		def("consolidate",
			"Ask the background process to review the recent conversation and update memory now. Returns immediately; the work runs in the background.",
			[]global.Parameter{
				{Name: "scope", Type: "string", Required: false, Description: "Optional scope hint (currently advisory)."},
			}, true, consolidate),

		def("export",
			"Dump the agent's entire active memory (all domains and their memories, plus pending items) to a single Markdown file at files/MEMORY_EXPORT.md, and report the path and counts.",
			nil, true, exportMemory),

		def("status",
			"Report memory status: database path, the last background update, and the number of pending (unconfirmed) memories.",
			nil, true, status),
	}
}
