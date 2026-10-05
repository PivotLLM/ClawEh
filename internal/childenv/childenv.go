// ClawEh - allowlisted environment for child processes
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

// Package childenv builds the environment handed to processes ClawEh starts:
// shell_exec commands, stdio MCP servers and CLI providers. The service
// process carries its own configuration in CLAW_* variables and the alerter's
// delivery credentials in ALERTER_*; a child that inherits the whole
// environment reads all of them. Base returns only what a child needs to run,
// so everything else, and in particular CLAW_* and ALERTER_*, stays in the
// parent.
package childenv

import (
	"os"
	"runtime"
	"slices"
	"sort"
	"strings"
)

// The allowlist. A variable is kept when its name is in names or begins with
// one of prefixes; anything else is dropped.
//
//   - PATH, HOME, USER, LOGNAME, SHELL, LANG, LC_*, TERM, TMPDIR, TZ: the
//     POSIX basics a program needs to find binaries, its own dotfiles and its
//     locale.
//   - XDG_*: the config/cache/data directories many CLIs store state under.
//   - SSL_CERT_FILE, SSL_CERT_DIR: a custom CA bundle.
//   - http_proxy, https_proxy, no_proxy and their upper-case forms: outbound
//     proxy settings.
//   - NODE_*, NVM_*, NPM_CONFIG_*/npm_config_*: stdio MCP servers are commonly
//     started through npx, which needs the Node and nvm settings to resolve
//     the toolchain and its registry configuration.
//   - macOS only: __CF_USER_TEXT_ENCODING, XPC_FLAGS and XPC_SERVICE_NAME,
//     which launchd sets and Foundation-based tools read at startup.
//   - Windows only: the system variables the shell and any executable rely on
//     (SYSTEMROOT, COMSPEC, PATHEXT, the profile and temp directories).
var (
	names = []string{
		"PATH", "HOME", "USER", "LOGNAME", "SHELL", "LANG", "TERM", "TMPDIR", "TZ",
		"SSL_CERT_FILE", "SSL_CERT_DIR",
		"http_proxy", "https_proxy", "no_proxy",
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY",
	}
	prefixes = []string{"LC_", "XDG_", "NODE_", "NVM_", "NPM_CONFIG_", "npm_config_"}

	darwinNames  = []string{"__CF_USER_TEXT_ENCODING", "XPC_FLAGS", "XPC_SERVICE_NAME"}
	windowsNames = []string{
		"SYSTEMROOT", "SYSTEMDRIVE", "WINDIR", "COMSPEC", "PATHEXT", "TEMP", "TMP",
		"USERPROFILE", "USERNAME", "HOMEDRIVE", "HOMEPATH", "APPDATA", "LOCALAPPDATA",
		"PROGRAMDATA", "PROGRAMFILES", "PROGRAMFILES(X86)",
	}

	// cliPrefixes are the variables a CLI provider (claude, codex, agy, cursor)
	// reads for its own login state and API keys. They are added by CLI only,
	// so shell_exec commands and MCP servers never see the operator's keys.
	cliPrefixes = []string{
		"ANTHROPIC_", "OPENAI_", "GOOGLE_", "GEMINI_", "CLAUDE_", "CODEX_", "CURSOR_",
		"AGY_", "ANTIGRAVITY_",
	}
)

// Base returns the allowlisted subset of the current process environment, in
// "K=V" form, sorted by name.
func Base() []string {
	return filter(nil)
}

// CLI returns Base plus the variables a CLI provider needs for its own
// authentication (see cliPrefixes).
func CLI() []string {
	return filter(cliPrefixes)
}

// Merge overlays extra on base: a key in extra replaces the same key in base,
// and new keys are appended in sorted order. base is not modified.
func Merge(base []string, extra map[string]string) []string {
	out := make([]string, 0, len(base)+len(extra))
	for _, kv := range base {
		if _, replaced := extra[key(kv)]; !replaced {
			out = append(out, kv)
		}
	}
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, k+"="+extra[k])
	}
	return out
}

func filter(morePrefixes []string) []string {
	var out []string
	for _, kv := range os.Environ() {
		k := key(kv)
		if keep(k) || hasPrefix(k, morePrefixes) {
			out = append(out, kv)
		}
	}
	sort.Strings(out)
	return out
}

func keep(k string) bool {
	switch runtime.GOOS {
	case "darwin":
		if inList(k, darwinNames) {
			return true
		}
	case "windows":
		// Windows environment names are case-insensitive ("Path").
		k = strings.ToUpper(k)
		if inList(k, windowsNames) {
			return true
		}
	}
	return hasPrefix(k, prefixes) || inList(k, names)
}

func inList(k string, list []string) bool {
	return slices.Contains(list, k)
}

func hasPrefix(k string, list []string) bool {
	for _, p := range list {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}

// key returns the name part of a "K=V" entry.
func key(kv string) string {
	k, _, _ := strings.Cut(kv, "=")
	return k
}
