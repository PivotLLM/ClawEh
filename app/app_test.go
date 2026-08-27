// ClawEh
// License: MIT

package app

import (
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestVersion_IsTheOnlySourceOfTruth pins why the const is unexported: the
// release version cannot be overwritten at link time or runtime, so no build can
// report a version its source does not declare.
func TestVersion_IsTheOnlySourceOfTruth(t *testing.T) {
	assert.NotEmpty(t, version)
	assert.NotEqual(t, "dev", version, "version must be a real release number, not a build-time placeholder")
	assert.True(t, strings.HasPrefix(Version(), version),
		"Version must lead with the release number, decorating it at most")
	assert.Equal(t, version, SemVer())
}

// TestVersion_IsASingleToken is the reason for the hyphen. Rendered as
// "0.4.68 (git: abc)", a version gets pasted into a bug report as "0.4.68" — the
// space reads as the end of the value and the parenthetical as an aside, so the
// half identifying the exact source is the half that gets dropped.
func TestVersion_IsASingleToken(t *testing.T) {
	old := gitCommit
	t.Cleanup(func() { gitCommit = old })

	gitCommit = "abc12345"
	got := Version()

	assert.NotContains(t, got, " ", "version must be one unbroken token")
	assert.NotContains(t, got, "(", "no parentheses — they invite truncation")
	assert.Len(t, strings.Fields(got), 1, "must survive being copied as a single word")
	assert.Equal(t, version+"+abc12345", got)
}

// TestVersion_UsesBuildMetadataSeparator pins the separator against SemVer's
// two meanings. "+" is build metadata, which the spec requires be ignored when
// comparing versions, so 0.4.69+abc compares EQUAL to 0.4.69. "-" would be a
// pre-release identifier, making the stamped build compare LOWER than the plain
// release and claim to be something that came before it.
func TestVersion_UsesBuildMetadataSeparator(t *testing.T) {
	old := gitCommit
	t.Cleanup(func() { gitCommit = old })

	gitCommit = "abc12345"
	got := Version()

	assert.Contains(t, got, "+", "the commit must attach as SemVer build metadata")
	assert.NotContains(t, strings.TrimPrefix(got, version), "-",
		"a hyphen would make this a pre-release, sorting below the release itself")
	assert.Equal(t, version, strings.SplitN(got, "+", 2)[0],
		"everything before the + must be the untouched release number")
}

// TestVersion_UnstampedBuildIsBare covers a plain `go build`: no commit, so no
// trailing hyphen dangling off the number.
func TestVersion_UnstampedBuildIsBare(t *testing.T) {
	old := gitCommit
	t.Cleanup(func() { gitCommit = old })

	gitCommit = ""
	assert.Equal(t, version, Version())
	assert.NotContains(t, Version(), "+", "no dangling separator when nothing is stamped in")
}

// TestSemVer_IsPlainSemver pins the protocol contract: no build metadata, ever.
// Paired R1 and Android clients read the device gateway's ServerVersion, and a
// suffix could break a comparison in software we do not control.
func TestSemVer_IsPlainSemver(t *testing.T) {
	old := gitCommit
	t.Cleanup(func() { gitCommit = old })

	gitCommit = "abc12345"

	assert.Equal(t, version, SemVer())
	assert.NotContains(t, SemVer(), "+", "no build metadata on the protocol form")
	assert.NotEqual(t, Version(), SemVer(), "the two forms differ once a commit is stamped")
}

// TestIdentityAccessors covers the remaining identity, which has no interesting
// logic but must not return empty strings into a startup banner.
func TestIdentityAccessors(t *testing.T) {
	assert.Equal(t, "ClawEh", Name())
	assert.NotEmpty(t, TagLine())
	assert.Contains(t, Copyright(), "Tenebris Technologies")
}

func TestBuildInfo_UsesStampedValues(t *testing.T) {
	ob, og := buildTime, goVersion
	t.Cleanup(func() { buildTime, goVersion = ob, og })

	buildTime, goVersion = "2026-02-20T00:00:00Z", "go1.23.0"

	b, g := BuildInfo()
	assert.Equal(t, "2026-02-20T00:00:00Z", b)
	assert.Equal(t, "go1.23.0", g)
}

// TestBuildInfo_FallsBackForToolchainOnly — the toolchain is knowable from the
// running binary even unstamped; the timestamp is not, and inventing one would
// be worse than reporting none.
func TestBuildInfo_FallsBackForToolchainOnly(t *testing.T) {
	ob, og := buildTime, goVersion
	t.Cleanup(func() { buildTime, goVersion = ob, og })

	buildTime, goVersion = "", ""

	b, g := BuildInfo()
	assert.Empty(t, b)
	assert.Equal(t, runtime.Version(), g)
}
