package api

import (
	"os"
	"runtime"
	"strconv"
	"strings"
)

// osReleasePath is where a Linux host names itself. A variable so the test can
// point it elsewhere.
var osReleasePath = "/etc/os-release"

// osName returns a human name for the host operating system — "Ubuntu 24.04.4
// LTS", "macOS", "Windows" — or "" when nothing better than runtime.GOOS is
// available and the caller should fall back to that.
//
// Deliberately shallow: one file read on Linux, a constant elsewhere. Pinning
// down a macOS point release or a Linux distro that ships no os-release would
// mean running subprocesses on every page load to answer a question the status
// page only asks in passing.
func osName() string {
	switch runtime.GOOS {
	case "linux":
		return prettyName(osReleasePath)
	case "darwin":
		return "macOS"
	case "windows":
		return "Windows"
	}
	return ""
}

// prettyName reads PRETTY_NAME out of an os-release file. Values may be quoted;
// anything unreadable or absent yields "".
func prettyName(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for line := range strings.Lines(string(data)) {
		key, value, found := strings.Cut(strings.TrimSpace(line), "=")
		if !found || key != "PRETTY_NAME" {
			continue
		}
		if unquoted, err := strconv.Unquote(value); err == nil {
			return unquoted
		}
		return value
	}
	return ""
}
