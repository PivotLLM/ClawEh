// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

// Package forumfs names the forum store's on-disk entries and its launch tool
// for the packages that must recognise them without importing the forum
// package (config, the backup, the file tools, Check Up). It imports
// nothing, so config can use it and the forum package keeps importing no
// other ClawEh package.
package forumfs

const (
	// BaseDirName is the directory under an agent's workspace that holds
	// its forums (the base directory).
	BaseDirName = "forums"
	// LocksDir is the directory under the base directory that holds the
	// forums' run locks.
	LocksDir = ".locks"
	// TempPrefix starts every temporary file or directory the store
	// creates; readers skip such entries (they are what a crash leaves
	// behind and were never published by a rename).
	TempPrefix = ".tmp-"
	// LaunchTool is the published name of the tool that starts a forum run
	// (the "launch" tool of forum.Tools under the "forum" namespace).
	LaunchTool = "forum_launch"
)
