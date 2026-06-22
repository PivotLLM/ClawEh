// ClawEh
// License: MIT
//
// Package servicetoken owns the on-disk state for long-lived, per-agent MCP
// service tokens (see docs/service-tokens.md). It is intentionally free of any
// MCP-server dependency so both the gateway (which loads tokens at boot) and the
// `claw token` CLI (which mints/revokes them) can use it.
package servicetoken

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/PivotLLM/ClawEh/pkg/fileutil"
)

// prefix is the magic literal at the start of every session/service token. It
// matches the MCP server's SST session-token format so the same record works on
// both endpoints and is covered by token redaction.
const prefix = "SST"

// fileName is the state file under the data dir's state/ directory.
const fileName = "service-tokens.json"

// Path returns the absolute path to the service-token state file for the given
// data directory (e.g. $CLAW_HOME or ~/.claw).
func Path(dataDir string) string {
	return filepath.Join(dataDir, "state", fileName)
}

// Generate returns a fresh service token: "SST" + 64 lowercase hex characters
// (32 random bytes).
func Generate() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("servicetoken: crypto/rand read: %w", err)
	}
	return prefix + hex.EncodeToString(raw), nil
}

// Load reads the agentID→token map from path. A missing file is not an error —
// it returns an empty map so callers can treat "no service tokens" uniformly.
func Load(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("servicetoken: read %s: %w", path, err)
	}
	var out map[string]string
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("servicetoken: parse %s: %w", path, err)
	}
	if out == nil {
		out = map[string]string{}
	}
	return out, nil
}

// Save atomically writes the agentID→token map to path (0o600), creating the
// parent state/ directory if needed.
func Save(path string, tokens map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("servicetoken: mkdir %s: %w", filepath.Dir(path), err)
	}
	data, err := json.MarshalIndent(tokens, "", "  ")
	if err != nil {
		return fmt.Errorf("servicetoken: marshal: %w", err)
	}
	if err := fileutil.WriteFileAtomic(path, data, 0o600); err != nil {
		return fmt.Errorf("servicetoken: write %s: %w", path, err)
	}
	return nil
}

// Agents returns the sorted list of agent IDs that have a service token. Tokens
// themselves are never returned by this helper (the CLI `list` must not print
// secrets).
func Agents(tokens map[string]string) []string {
	ids := make([]string, 0, len(tokens))
	for id := range tokens {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
