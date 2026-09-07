package api

import (
	"os"
	"os/exec"

	"github.com/PivotLLM/ClawEh/config"
)

// providerReady reports whether a provider is usable as configured — the single
// rule behind both the green dot on the Providers page and the "Providers
// configured" figure on the Status page, so the two cannot disagree.
//
// The meaning differs by kind but the question does not: will this work?
//
//   - HTTP providers authenticate with a key, so a key is the test.
//   - CLI providers authenticate out of band, so the test is whether the
//     binary can actually be found.
//
// That second case used to be `command != ""`, which was wrong in both
// directions: a provider pinned to a path that no longer exists showed as
// ready, and one leaving the path blank — the way the seeded config ships, and
// the more robust choice — showed as not ready even though it runs perfectly.
//
// Resolution deliberately happens in this process. A blank command is looked up
// on PATH, and this is the PATH the CLI subprocess will actually be launched
// with, which no other vantage point can know: one host may run several
// instances under different users with different PATHs baked into their units.
func providerReady(p *config.Provider) bool {
	if !config.IsCLIProtocol(p.Protocol) {
		return p.APIKey != ""
	}
	_, ok := resolveCLIBinary(p.Protocol, p.Command)
	return ok
}

// resolvedCLICommand returns where a CLI provider's binary was found, and "" for
// an HTTP provider or a CLI one that could not be resolved. It lets the card
// show which binary a blank command will run.
func resolvedCLICommand(p *config.Provider) string {
	if !config.IsCLIProtocol(p.Protocol) {
		return ""
	}
	path, ok := resolveCLIBinary(p.Protocol, p.Command)
	if !ok {
		return ""
	}
	return path
}

// resolveCLIBinary resolves a CLI provider's binary the way the provider
// factory will. An explicit command is taken as given — a path if it contains a
// separator, otherwise a name looked up on PATH — and an empty one falls back
// to the protocol's default binary.
func resolveCLIBinary(protocol, command string) (string, bool) {
	if command == "" {
		command = defaultCLIBinary(protocol)
		if command == "" {
			return "", false
		}
	}
	if path, err := exec.LookPath(command); err == nil {
		return path, true
	}
	// LookPath rejects a path that exists but is not marked executable. That is
	// a permissions problem, not a missing binary, and reporting it as "not
	// found" would send the operator looking for the wrong thing.
	if info, err := os.Stat(command); err == nil && !info.IsDir() {
		return command, true
	}
	return "", false
}

// defaultCLIBinary maps a CLI protocol to the binary it runs when no command is
// configured, sourced from knownCLIs so the Providers page, the Status page and
// the setup wizard's installed-CLI list cannot drift apart.
func defaultCLIBinary(protocol string) string {
	for _, c := range knownCLIs {
		if c.Protocol == protocol {
			return c.Binary
		}
	}
	// gemini-cli is an accepted alias for antigravity-cli and so is absent from
	// knownCLIs; resolve it to the same binary rather than reporting a provider
	// an upgraded install still names as unusable.
	if protocol == "gemini-cli" {
		return defaultCLIBinary("antigravity-cli")
	}
	return ""
}
