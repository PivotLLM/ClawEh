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

// AppVersion is the current release version of ClawEh, and the single source of
// truth for it. Bump it here; nothing else defines a version.
//
// It is deliberately a const, so it cannot be overwritten at link time. The
// build stamps BUILD METADATA (commit, timestamp, toolchain) via ldflags and
// GetVersion appends it — the version itself is a property of the source, not
// of the machine that compiled it.
//
// Exported because external tooling (the build/release scripts) reads it from
// this file. In Go code prefer GetVersion or GetVersionShort: reading the const
// directly is what let one binary render its version two different ways.
const AppVersion = "0.4.68"

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
// session_info. It is the release version joined to the build commit with a
// hyphen — "0.4.68-c331081b" — or bare "0.4.68" from a plain `go build` with
// nothing stamped in.
//
// The format is a single unbroken token on purpose. A version rendered as
// "0.4.68 (git: c331081b)" gets copied into a bug report as "0.4.68", because
// the space reads as the end of the value and the parenthetical as an aside.
// The commit is the half that identifies the exact source, so it must travel
// with the number rather than beside it.
//
// Use this or GetVersionShort rather than the AppVersion const. Mixing
// them is how the app came to report a bare number in some places and a
// decorated one in others, making two log lines from one binary look like two
// builds.
func GetVersion() string {
	if GitCommit == "" {
		return AppVersion
	}
	return AppVersion + "-" + GitCommit
}

// GetVersionShort returns just the release number — "0.4.68" — with no build
// metadata attached.
//
// It exists for PROTOCOL handshakes: the MCP serverInfo, the ACP Implementation
// fields, the device gateway's ServerVersion. Those identify the build to
// another program rather than to a person, and a client is entitled to compare
// or parse them, so they must stay a plain semver. The device gateway is the
// concrete case — paired R1 and Android clients read that field, and software
// we do not control could break on a suffix.
func GetVersionShort() string {
	return AppVersion
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
