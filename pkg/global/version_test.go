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
	assert.NotEmpty(t, version)
	assert.NotEqual(t, "dev", version, "version must be a real release number, not a build-time placeholder")
	assert.True(t, strings.HasPrefix(GetVersion(), version),
		"GetVersion must lead with the constant, decorating it at most")
}

// TestGetVersion_NoGitCommit covers a plain `go build`, where nothing is
// stamped in: the bare version, with no empty parenthetical trailing it.
func TestGetVersion_NoGitCommit(t *testing.T) {
	old := GitCommit
	t.Cleanup(func() { GitCommit = old })

	GitCommit = ""
	assert.Equal(t, version, GetVersion())
}

// TestGetVersion_WithGitCommit covers a Makefile build, where the commit is
// appended to identify the exact source a binary came from.
func TestGetVersion_WithGitCommit(t *testing.T) {
	old := GitCommit
	t.Cleanup(func() { GitCommit = old })

	GitCommit = "abc123"
	assert.Equal(t, version+"-abc123", GetVersion())
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

// TestGetVersion_IsWhatDisplaySitesShow is the point of having one helper: every
// human- or model-facing surface must render the same string. Reading Version
// directly is what let two log lines from one binary look like two builds.
func TestGetVersion_IsWhatDisplaySitesShow(t *testing.T) {
	old := GitCommit
	t.Cleanup(func() { GitCommit = old })

	GitCommit = "deadbeef"
	want := GetVersion()

	assert.Equal(t, want, GetVersion(), "GetVersion must be stable across calls")
	assert.Contains(t, want, version, "the release version must always be present")
	assert.Contains(t, want, "deadbeef", "the stamped commit must be present when set")
}

// TestGetVersion_IsASingleToken is the reason for the hyphen. Rendered as
// "0.4.68 (git: abc)", a version gets pasted into a bug report as "0.4.68" —
// the space reads as the end of the value and the parenthetical as an aside,
// so the half that identifies the exact source is the half that gets dropped.
func TestGetVersion_IsASingleToken(t *testing.T) {
	old := GitCommit
	t.Cleanup(func() { GitCommit = old })

	GitCommit = "abc123"
	got := GetVersion()

	assert.NotContains(t, got, " ", "version must be one unbroken token")
	assert.NotContains(t, got, "(", "no parentheses — they invite truncation")
	assert.Equal(t, 1, len(strings.Fields(got)), "must survive being copied as a single word")
	assert.Equal(t, version+"-abc123", got)
}

// TestGetVersionShort_IsPlainSemver pins the protocol contract: no build
// metadata, ever. Paired R1 and Android clients read the device gateway's
// ServerVersion, and a suffix could break a comparison in software we do not
// control.
func TestGetVersionShort_IsPlainSemver(t *testing.T) {
	old := GitCommit
	t.Cleanup(func() { GitCommit = old })

	GitCommit = "abc123"

	assert.Equal(t, version, GetVersionShort())
	assert.NotContains(t, GetVersionShort(), "-", "no build suffix on the protocol form")
	assert.NotEqual(t, GetVersion(), GetVersionShort(), "the two forms differ once a commit is stamped")
}
