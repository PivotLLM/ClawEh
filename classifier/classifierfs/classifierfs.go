// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

// Package classifierfs names the classifier store's on-disk entries for the
// packages that must recognise them without importing the classifier package
// (config, the backup, the file tools, Check Up). It imports nothing, so
// config can use it and the classifier package keeps importing no other
// ClawEh package but its allowed leaves.
package classifierfs

const (
	// BaseDirName is the directory under an agent's workspace that holds its
	// classifier definitions and state (the base directory).
	BaseDirName = "classifiers"
	// StateFile is the SQLite database in the base directory holding the
	// processed item ids, dry-run decisions, the activity log and the last
	// run of every classifier there.
	StateFile = "state.db"
	// DefinitionExt ends every definition file: <id>.json.
	DefinitionExt = ".json"
	// TempPrefix starts every temporary file the store creates; readers skip
	// such entries (they are what a crash leaves behind and were never
	// published by a rename).
	TempPrefix = ".tmp-"
)
