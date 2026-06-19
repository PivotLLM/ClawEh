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

	defs := []global.ToolDefinition{
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
			"List memory domains (id, name, summary, status; sticky ones are marked). Optionally filter by status. To see everything you're tracking, list with no filter.",
			[]global.Parameter{
				{Name: "status", Type: "string", Required: false, Description: "Filter by status.",
					Enum: []any{"active", "review", "archived"}},
			}, true, listDomains),

		def("explain",
			"Summarize the status, source, and evidence of a domain or memory id.",
			[]global.Parameter{
				{Name: "id", Type: "string", Required: true, Description: "A domain id (d…) or memory id (h…)."},
			}, true, explain),

		def("memory_create",
			"Record a durable memory (a fact, preference, or rule). With NO domain_id and NO domain_hint it records to your sticky 'General' domain (global rules/preferences/facts always in context). Give a domain_hint to use (or create) a topic domain by name, or a domain_id to target a specific one.",
			[]global.Parameter{
				{Name: "domain_id", Type: "string", Required: false, Description: "Target domain id. If omitted and no domain_hint is given, records to the sticky General domain."},
				{Name: "domain_hint", Type: "string", Required: false, Description: "A domain name: an existing domain with that name is reused, otherwise a new (non-sticky) one is created. Omit to use General."},
				{Name: "type", Type: "string", Required: true, Description: "Memory type: fact (something true), preference (how the user likes things done), or rule (a hard directive).",
					Enum: []any{"fact", "preference", "rule"}},
				{Name: "text", Type: "string", Required: true, Description: "The memory content to store."},
				{Name: "confidence", Type: "number", Required: false, Description: "Confidence 0..1 (default 0.9)."},
				{Name: "status", Type: "string", Required: false, Description: "active (default), or review to hold it as pending (unconfirmed) until the user confirms.",
					Enum: []any{"active", "review"}},
			}, true, remember),

		def("domain_update",
			"Update a domain — a patch: pass only the fields you want to change (rename, summary, state, sticky, triggers). No version needed.",
			[]global.Parameter{
				{Name: "id", Type: "string", Required: true, Description: "Domain id."},
				{Name: "set_name", Type: "string", Required: false, Description: "Rename the domain. Names must be unique; rejected if another domain already uses it."},
				{Name: "set_sticky", Type: "boolean", Required: false, Description: "true = always inject this domain into context every turn; false = routed only when relevant. Use sticky sparingly."},
				{Name: "set_summary", Type: "string", Required: false, Description: "Replace the domain summary."},
				{Name: "set_blockers", Type: "array", Items: "string", Required: false, Description: "Replace the blockers list."},
				{Name: "set_next_actions", Type: "array", Items: "string", Required: false, Description: "Replace the next-actions list."},
				{Name: "set_constraints", Type: "array", Items: "string", Required: false, Description: "Replace the constraints list."},
				{Name: "set_triggers", Type: "string", Required: false, Description: "Replace tool triggers: comma-separated name fragments that auto-load this domain whenever you use a tool whose name contains one (substring, case-insensitive). E.g. \"mail,github\"; for an MCP server use its name (\"github\" matches mcp_github_*). Empty string clears."},
				{Name: "set_keyword_triggers", Type: "array", Items: "string", Required: false, Description: "Replace keyword triggers: words/phrases that load this domain when they appear in the incoming message (whole-phrase, word-boundary). Prefer multi-word phrases like \"morning routine\" over common single words. Empty list clears."},
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
			"Create a new memory domain and return its assigned id. A domain groups related memories; register each ongoing project/topic as its own domain. Names must be unique — creating one with an existing name returns an error (reuse or rename instead).",
			[]global.Parameter{
				{Name: "name", Type: "string", Required: true, Description: "Domain name (must be unique)."},
				{Name: "sticky", Type: "boolean", Required: false, Description: "true = always inject this domain into context every turn (use sparingly). Default false: loaded only when relevant."},
				{Name: "summary", Type: "string", Required: false, Description: "Optional one-line summary."},
				{Name: "triggers", Type: "string", Required: false, Description: "Optional tool triggers: comma-separated name fragments that auto-load this domain whenever you use a tool whose name contains one (substring, case-insensitive). E.g. \"mail,github\"; for an MCP server use its name (\"github\" matches mcp_github_*)."},
				{Name: "keyword_triggers", Type: "array", Items: "string", Required: false, Description: "Optional keyword triggers: words/phrases that load this domain when they appear in the incoming message (whole-phrase, word-boundary). Prefer multi-word phrases like \"morning routine\". Use this to pull a workflow's context up when a cron job fires."},
			}, true, createDomain),

		def("domain_archive",
			"Archive a domain so it is no longer used in prompting.",
			[]global.Parameter{
				{Name: "id", Type: "string", Required: true, Description: "Domain id."},
			}, true, archiveDomain),

		def("domain_migrate",
			"Merge two domains: move every memory from the 'from' domain into the 'to' domain, then permanently delete the 'from' domain. Use this to consolidate duplicate/overlapping domains into one.",
			[]global.Parameter{
				{Name: "from", Type: "string", Required: true, Description: "Source domain id — its memories are moved out and the domain is deleted."},
				{Name: "to", Type: "string", Required: true, Description: "Destination domain id — receives the moved memories."},
			}, true, migrateDomain),

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

	// Sub-agents get READ-ONLY cognitive memory: they share the primary's memory
	// for background but cannot mutate it. Mark the write tools primary-only so a
	// sub-agent's registry excludes them (the store is also opened read-only for
	// sub-agents, so a stray write still fails). Read tools (domain_get,
	// memory_search, domain_list, explain, export, status) remain available.
	writeTools := map[string]bool{
		"memory_create": true, "domain_update": true, "memory_retire": true,
		"memory_confirm": true, "domain_create": true, "domain_archive": true,
		"domain_migrate": true, "memory_forget": true, "consolidate": true,
	}
	for i := range defs {
		if writeTools[defs[i].Name] {
			defs[i].PrimaryOnly = true
		}
	}
	return defs
}
