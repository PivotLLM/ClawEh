// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

// Names other packages need to recognise the forum store on disk and its
// tools (the backup skips the lock files and temporary files; Check Up names
// the tool that starts other agents' turns).
const (
	// LocksDir is the directory, directly under the forums base directory,
	// that holds the store's lock files.
	LocksDir = ".locks"
	// TmpPrefix starts every temporary file or directory the store creates.
	TmpPrefix = ".tmp-"
	// LaunchTool is the published name of the tool that starts a forum run.
	LaunchTool = "forum_launch"
)
