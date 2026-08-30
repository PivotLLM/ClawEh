// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

// Package app holds the application's identity — its name, tagline, copyright
// and version — and is the only place any of them is defined.
//
// Everything here is unexported and reached through an accessor. That is a
// deliberate reversal of the usual "export the constant" habit: the identity is
// read from roughly forty places, and when it was a bare exported constant the
// same binary ended up rendering its version two different ways depending on
// which symbol a call site happened to reach for. An accessor is the only way to
// make one rendering the default.
//
// Build metadata is injected at link time. The version itself never is: it is a
// property of the source, not of the machine that compiled it, so it stays a
// const the linker cannot reach. Deriving a version from `git describe` produces
// a binary that disagrees with its own source.
package app

import "runtime"

const (
	name      = "ClawEh"
	tagLine   = "Personal AI Assistant"
	copyright = "Copyright (c) 2026 Tenebris Technologies Inc.\n" +
		"Some code Copyright (c) 2026 PicoClaw contributors."

	// version is the release number, bare semver. Bump it here; nothing else
	// defines a version. Build tooling reads this line, so keep it a single
	// `const`-style assignment on one line.
	version = "0.4.70"
)

// Build metadata, injected via ldflags by the Makefile:
//
//	-X github.com/PivotLLM/ClawEh/app.gitCommit=<sha8>
//	-X github.com/PivotLLM/ClawEh/app.buildTime=<rfc3339>
//	-X github.com/PivotLLM/ClawEh/app.goVersion=<go version>
//
// Unexported: `go tool link -X` sets package-level string vars by symbol name
// and does not care about case, so privacy costs nothing here. All three are
// empty under a plain `go build`, which is fine — they only decorate the
// version, never replace it.
var (
	gitCommit string
	buildTime string
	goVersion string
)

// Name returns the product name.
func Name() string { return name }

// TagLine returns the one-line product description.
func TagLine() string { return tagLine }

// Copyright returns the copyright notice.
func Copyright() string { return copyright }

// Version returns the build's full identity: the release number followed by the
// build commit as SemVer build metadata — "0.4.69+27691883" — or bare "0.4.69"
// when nothing was stamped in.
//
// This is the form for anywhere a human or a model reads it: startup banner,
// version command, logs, diagnostics, the system prompt. It is one unbroken
// token on purpose. Rendered as "0.4.69 (git: 27691883)" it gets copied into a
// bug report as "0.4.69" — the space reads as the end of the value — so the half
// that identifies the exact source is the half that gets dropped.
//
// The separator is "+", not "-", because SemVer gives the two different
// meanings. "+" introduces build metadata, which the spec requires be IGNORED
// when comparing versions, so "0.4.69+27691883" compares equal to "0.4.69" — it
// is that release, built from that commit. "-" would introduce a PRE-RELEASE
// identifier, making it compare LOWER than "0.4.69" and claiming to be something
// that came before the release rather than an instance of it.
func Version() string {
	if gitCommit == "" {
		return version
	}
	return version + "+" + gitCommit
}

// SemVer returns the release number alone — "0.4.69" — with no build metadata.
//
// Named for why you would want it rather than for its shape: this is the
// parseable form, for protocol handshakes another program may compare (MCP
// serverInfo, ACP Implementation, the device gateway's ServerVersion) and for
// checking a version against a constraint. Reach for Version() everywhere else.
func SemVer() string { return version }

// BuildInfo returns the build timestamp and the Go toolchain version. The
// toolchain falls back to the running binary's own, which is knowable even in an
// unstamped build; the timestamp has no such fallback and stays empty.
func BuildInfo() (string, string) {
	if goVersion == "" {
		return buildTime, runtime.Version()
	}
	return buildTime, goVersion
}
