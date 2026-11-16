// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package global

// Fixed identifiers: the names of real files, services and protocol keys that
// installs, scripts and other programs already depend on. They are not the
// product's label — that is app.Name / app.ShortName — and they do not change
// when the product is renamed. Each is defined here once; the product-name
// guard (app/productname_guard_test.go) exempts this file and nothing else.
const (
	// InstalledBinaryName is the file name of the installed binary and of the
	// binary inside a release archive. It is not what the user typed; that is
	// app.BinaryName.
	InstalledBinaryName = "claw"

	// AuthHelperBinaryName is the companion OAuth helper shipped beside the
	// main binary.
	AuthHelperBinaryName = "claw-auth"

	// ServiceName is the systemd unit name `install` registers.
	ServiceName = "claw"

	// SystemdUnitFile is the unit file name for ServiceName.
	SystemdUnitFile = ServiceName + ".service"

	// LaunchdLabel is the launchd job label `install` registers on macOS.
	LaunchdLabel = "com.pivotllm.claweh"

	// SystemDataDir is the data directory of a system-wide install.
	SystemDataDir = "/opt/claw"

	// LogFileName is the main log file under <CLAW_HOME>/logs.
	LogFileName = "claw.log"

	// LockFileName is the single-instance lock file in the data directory.
	LockFileName = "claw.lock"

	// HostMCPToolPrefix is the prefix CLI agents see on this host's MCP tools:
	// the MCP server is registered under the key "claw", and the CLIs name its
	// tools mcp__<key>__<tool>. Changing it would rename every host tool.
	HostMCPToolPrefix = "mcp__claw__"

	// ReleaseRepoName is the GitHub repository `upgrade` downloads releases
	// from (owner PivotLLM).
	ReleaseRepoName = "ClawEh"

	// MaestroHostModel is the model label Maestro records for a task the host
	// ran without naming a model; Maestro stores it and hands it back as a
	// task's llm_model_id.
	MaestroHostModel = "claw"

	// FolderWatchMarkerHeader is the first line of a folder-watch marker file.
	// The watcher parses it; a different header discards existing markers.
	FolderWatchMarkerHeader = "# claw seen files v1"
)
