// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

// Package classifier sorts items a tool can list: for every new item it
// fetches the fields a model needs, asks the owning agent's classifier model
// one question with a fixed set of categories, and runs the action the
// definition names for the answer. The model never calls tools; every tool
// (source, fetch, actions) runs as the owning agent through the host.
//
// A classifier is a JSON definition, <workspace>/classifiers/<id>.json,
// owned by the agent that created it. Run state (processed item ids, dry-run
// decisions, the activity log and the last run) is one SQLite database per
// folder, classifiers/state.db. Every classifier runs on its own interval
// timer; one run at a time.
//
// The package imports no ClawEh package but its own classifierfs and the
// stdlib-only leaves internal/mergepatch and internal/dotpath, so the agent
// loop can host it (agent.ClassifierHost) without an import cycle and the
// engine is tested against fake hosts.
package classifier
