// ClawEh
// License: MIT

package global

import (
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestVersion_IsTheOnlySourceOfTruth pins the property the whole arrangement
// exists for: the release version is a const in this file, so nothing — not
// ldflags, not another package — can report a different one. It used to be
// possible: pkg/config carried a second, ldflag-injected Version, so `claw
// version` reported the last git tag plus a commit count while the startup log,
// the MCP server identity and the system prompt reported this constant.
func TestVersion_IsTheOnlySourceOfTruth(t *testing.T) {
	assert.NotEmpty(t, Version)
	assert.NotEqual(t, "dev", Version, "Version must be a real release number, not a build-time placeholder")
	assert.True(t, strings.HasPrefix(FormatVersion(), Version),
		"FormatVersion must lead with the constant, decorating it at most")
}

// TestFormatVersion_NoGitCommit covers a plain `go build`, where nothing is
// stamped in: the bare version, with no empty parenthetical trailing it.
func TestFormatVersion_NoGitCommit(t *testing.T) {
	old := GitCommit
	t.Cleanup(func() { GitCommit = old })

	GitCommit = ""
	assert.Equal(t, Version, FormatVersion())
}

// TestFormatVersion_WithGitCommit covers a Makefile build, where the commit is
// appended to identify the exact source a binary came from.
func TestFormatVersion_WithGitCommit(t *testing.T) {
	old := GitCommit
	t.Cleanup(func() { GitCommit = old })

	GitCommit = "abc123"
	assert.Equal(t, Version+" (git: abc123)", FormatVersion())
}

func TestFormatBuildInfo_UsesStampedValues(t *testing.T) {
	oldBuild, oldGo := BuildTime, GoVersion
	t.Cleanup(func() { BuildTime, GoVersion = oldBuild, oldGo })

	BuildTime, GoVersion = "2026-02-20T00:00:00Z", "go1.23.0"

	build, goVer := FormatBuildInfo()
	assert.Equal(t, "2026-02-20T00:00:00Z", build)
	assert.Equal(t, "go1.23.0", goVer)
}

// TestFormatBuildInfo_EmptyBuildTimeStaysEmpty — an unstamped build has no
// timestamp to report, and inventing one would be worse than showing none.
func TestFormatBuildInfo_EmptyBuildTimeStaysEmpty(t *testing.T) {
	oldBuild, oldGo := BuildTime, GoVersion
	t.Cleanup(func() { BuildTime, GoVersion = oldBuild, oldGo })

	BuildTime, GoVersion = "", "go1.23.0"

	build, goVer := FormatBuildInfo()
	assert.Empty(t, build)
	assert.Equal(t, "go1.23.0", goVer)
}

// TestFormatBuildInfo_GoVersionFallsBackToRuntime — unlike the timestamp, the
// toolchain is always knowable from the running binary, so an unstamped build
// still reports it accurately.
func TestFormatBuildInfo_GoVersionFallsBackToRuntime(t *testing.T) {
	oldBuild, oldGo := BuildTime, GoVersion
	t.Cleanup(func() { BuildTime, GoVersion = oldBuild, oldGo })

	BuildTime, GoVersion = "x", ""

	build, goVer := FormatBuildInfo()
	assert.Equal(t, "x", build)
	assert.Equal(t, runtime.Version(), goVer)
}
