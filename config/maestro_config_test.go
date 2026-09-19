// ClawEh
// License: MIT

package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/logger"
)

func TestMaestroConfig_UnmarshalObject(t *testing.T) {
	var a AgentConfig
	if err := json.Unmarshal([]byte(`{"id":"x","maestro":{"enabled":true,"max_concurrent":2,"rate_limit_requests":5,"rate_limit_period":30,"allow_parallel":false}}`), &a); err != nil {
		t.Fatal(err)
	}
	m := a.Maestro
	if m == nil || !m.Enabled || m.MaxConcurrent != 2 || m.RateLimitRequests != 5 || m.RateLimitPeriod != 30 || m.ParallelAllowed() || m.LegacyBoolean() {
		t.Fatalf("maestro = %+v", m)
	}
	if !a.MaestroEnabled() {
		t.Error("MaestroEnabled() = false")
	}
}

func TestMaestroConfig_Defaults(t *testing.T) {
	var a AgentConfig
	if err := json.Unmarshal([]byte(`{"id":"x","maestro":{"enabled":true}}`), &a); err != nil {
		t.Fatal(err)
	}
	if !a.Maestro.ParallelAllowed() || a.Maestro.MaxConcurrent != 0 {
		t.Errorf("defaults: %+v", a.Maestro)
	}
	var none AgentConfig
	if err := json.Unmarshal([]byte(`{"id":"x"}`), &none); err != nil {
		t.Fatal(err)
	}
	if none.Maestro != nil || none.MaestroEnabled() || !none.Maestro.ParallelAllowed() {
		t.Errorf("absent block: %+v enabled=%v", none.Maestro, none.MaestroEnabled())
	}
}

// TestMaestroConfig_LegacyBooleanNotHonoured: the retired `"maestro": true`
// parses without error but leaves Maestro disabled and is reported.
func TestMaestroConfig_LegacyBooleanNotHonoured(t *testing.T) {
	for _, raw := range []string{`true`, `false`, `"yes"`, `1`} {
		var a AgentConfig
		if err := json.Unmarshal([]byte(`{"id":"x","maestro":`+raw+`}`), &a); err != nil {
			t.Fatalf("maestro=%s: %v", raw, err)
		}
		if a.MaestroEnabled() {
			t.Errorf("maestro=%s must not enable Maestro", raw)
		}
		if !a.Maestro.LegacyBoolean() {
			t.Errorf("maestro=%s must be flagged as the retired form", raw)
		}
	}
	var a AgentConfig
	if err := json.Unmarshal([]byte(`{"id":"x","maestro":null}`), &a); err != nil {
		t.Fatal(err)
	}
	if a.Maestro != nil && (a.Maestro.Enabled || a.Maestro.LegacyBoolean()) {
		t.Errorf("null: %+v", a.Maestro)
	}
	if err := json.Unmarshal([]byte(`{"id":"x","maestro":{"enabled":"yes"}}`), &a); err == nil {
		t.Error("a malformed object must still be a parse error")
	}
}

func TestMaestroConfig_MarshalOmitsAbsentBlock(t *testing.T) {
	b, _ := json.Marshal(AgentConfig{ID: "x"})
	if strings.Contains(string(b), "maestro") {
		t.Errorf("absent block serialised: %s", b)
	}
	b, _ = json.Marshal(AgentConfig{ID: "x", Maestro: &MaestroConfig{Enabled: true, MaxConcurrent: 3}})
	if !strings.Contains(string(b), `"maestro":{"enabled":true,"max_concurrent":3}`) {
		t.Errorf("block serialised as %s", b)
	}
}

func TestConfig_AgentMaestroLookups(t *testing.T) {
	c := &Config{Agents: AgentsConfig{List: []AgentConfig{
		{ID: "Alice", Maestro: &MaestroConfig{Enabled: true, MaxConcurrent: 7}},
		{ID: "bob"},
	}}}
	if !c.AgentHasMaestro("alice") || c.AgentHasMaestro("bob") || c.AgentHasMaestro("nobody") {
		t.Error("AgentHasMaestro")
	}
	if m := c.AgentMaestro(" alice "); m == nil || m.MaxConcurrent != 7 {
		t.Errorf("AgentMaestro = %+v", m)
	}
	if c.AgentMaestro("bob") != nil || c.AgentMaestro("nobody") != nil {
		t.Error("AgentMaestro must be nil without a block")
	}
	if a := c.AgentByID("BOB"); a == nil || a.ID != "bob" {
		t.Errorf("AgentByID = %+v", a)
	}
	if !c.AgentSuiteEnabled("alice", "maestro") || c.AgentSuiteEnabled("bob", "maestro") {
		t.Error("AgentSuiteEnabled maestro")
	}
}

// TestLoadConfig_LegacyMaestroBoolean_WarnsAndStarts: a saved config with the
// retired boolean loads (the gateway starts), disables Maestro for that agent
// and logs a warning naming it.
func TestLoadConfig_LegacyMaestroBoolean_WarnsAndStarts(t *testing.T) {
	var buf bytes.Buffer
	restore := logger.RedirectForTest(&buf)
	defer restore()

	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"agents":{"list":[{"id":"alice","maestro":true},{"id":"bob","maestro":{"enabled":true}}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.AgentHasMaestro("alice") {
		t.Error("legacy boolean must not enable Maestro")
	}
	if !cfg.AgentHasMaestro("bob") {
		t.Error("object form must enable Maestro")
	}
	if out := buf.String(); !strings.Contains(out, "retired boolean") || !strings.Contains(out, "alice") || strings.Contains(out, "bob") {
		t.Errorf("warning not logged for alice only:\n%s", out)
	}
}
