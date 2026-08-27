// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package global

import "runtime"

// AppName is the canonical application name.
const AppName = "ClawEh"

// AppTagLine
const AppTagLine = "Personal AI Assistant"

// AppCopyright is displayed at start
const AppCopyright = "Copyright (c) 2026 Tenebris Technologies Inc.\nSome code Copyright (c) 2026 PicoClaw contributors."

// Version is the current release version of ClawEh, and the single source of
// truth for it. Bump it here; nothing else defines a version.
//
// It is deliberately a const, so it cannot be overwritten at link time. The
// build stamps BUILD METADATA (commit, timestamp, toolchain) via ldflags and
// GetVersion appends it — the version itself is a property of the source,
// not of the machine that compiled it.
const Version = "0.4.68"

// Build-time metadata, injected via ldflags by the Makefile:
//
//	-X github.com/PivotLLM/ClawEh/pkg/global.GitCommit=<sha>
//	-X github.com/PivotLLM/ClawEh/pkg/global.BuildTime=<rfc3339>
//	-X github.com/PivotLLM/ClawEh/pkg/global.GoVersion=<go version>
//
// All are empty in a plain `go build`, which is fine: they only decorate the
// version, never replace it.
var (
	GitCommit string // Git commit SHA (short)
	BuildTime string // Build timestamp
	GoVersion string // Go toolchain used for the build
)

// GetVersion returns the one version string the application shows anywhere a
// human or a model will read it: logs, the CLI, the WebUI, the system prompt,
// session_info. It is the release version with the build commit appended when
// one was stamped in — "0.4.68 (git: c331081b)", or bare "0.4.68" from a plain
// `go build`.
//
// Use this rather than reading Version directly. Mixing the two is how the app
// came to report a bare number in some places and a decorated one in others,
// which makes two log lines from the same binary look like two builds.
//
// The exception is a PROTOCOL handshake — the MCP serverInfo, the ACP
// Implementation, the device gateway's ServerVersion. Those identify the build
// to another program rather than to a person, and a client is entitled to
// compare or parse them, so they send the bare Version const.
func GetVersion() string {
	if GitCommit == "" {
		return Version
	}
	return Version + " (git: " + GitCommit + ")"
}

// FormatBuildInfo returns the build timestamp and the Go toolchain version.
// The toolchain falls back to the running binary's own, which is always
// available even in an unstamped build.
func FormatBuildInfo() (string, string) {
	goVer := GoVersion
	if goVer == "" {
		goVer = runtime.Version()
	}
	return BuildTime, goVer
}
