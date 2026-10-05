// ClawEh
// License: MIT

package gateway

import (
	"testing"
)

// humanConfigJSON has an ordinary agent alice and a human agent bob; extra
// makes bob list a model besides the person.
func humanConfigJSON(extra bool) string {
	models := `["Bob (human)"]`
	if extra {
		models = `["Bob (human)", "good"]`
	}
	return `{
  "providers": [
    {"name": "openai", "protocol": "openai-chat", "base_url": "https://api.openai.com/v1", "api_key": "sk-test"},
    {"name": "People", "protocol": "human"}
  ],
  "models": [
    {"model_name": "good", "model": "gpt-4o", "provider": "openai", "enabled": true},
    {"model_name": "Bob (human)", "model": "bob", "provider": "People", "enabled": true}
  ],
  "agents": {"defaults": {"models": ["good"]}, "list": [
    {"id": "alice", "default": true, "models": ["good"]},
    {"id": "bob", "models": ` + models + `}
  ]},
  "bindings": [
    {"agent_id": "bob", "default": true, "match": {"channel": "telegram-main", "peer": {"kind": "direct", "id": "4242"}}}
  ]
}`
}

// The running copy never runs a human agent that breaks its rules; the file
// keeps it for repair. A valid human agent runs.
func TestRuntimeConfig_HumanAgents(t *testing.T) {
	for _, tc := range []struct {
		name    string
		extra   bool
		running bool
	}{
		{"valid", false, true},
		{"with a fallback", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := openStore(t, t.TempDir(), humanConfigJSON(tc.extra))
			cfg, _, err := runtimeConfig(store)
			if err != nil {
				t.Fatalf("runtimeConfig: %v", err)
			}
			if got := cfg.Agents.List[1].IsEnabled(); got != tc.running {
				t.Errorf("bob enabled = %v, want %v", got, tc.running)
			}
			if !cfg.Agents.List[0].IsEnabled() {
				t.Error("alice was disabled")
			}
			if !store.Current().Agents.List[1].IsEnabled() {
				t.Error("the stored config was changed")
			}
		})
	}
}
