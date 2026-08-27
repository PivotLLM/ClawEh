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
// FormatVersion appends it — the version itself is a property of the source,
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

// FormatVersion returns the release version with the build commit appended when
// one was stamped in, e.g. "0.4.68 (git: c331081b)".
func FormatVersion() string {
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
