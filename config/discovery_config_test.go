package config

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
)

// A config.json written while tools.discovery still had ttl_max,
// visible_budget and the older ttl loads without an unknown-key warning, keeps
// the remaining discovery settings, and a save through the Store drops the
// removed keys.
func TestDiscoveryConfig_RemovedKeysIgnoredSilently(t *testing.T) {
	s := newTestStore(t)
	path := s.Path()

	doc := readConfigDoc(t, path)
	toolsDoc, ok := doc["tools"].(map[string]any)
	if !ok {
		t.Fatal("saved config has no tools object")
	}
	toolsDoc["discovery"] = map[string]any{
		"enabled":                 true,
		"always_shown_namespaces": []any{"fusion"},
		"max_search_results":      7,
		"ttl_max":                 40,
		"visible_budget":          90,
		"ttl":                     5,
	}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	decoded, err := decodeDocument(data)
	if err != nil {
		t.Fatal(err)
	}
	if got := unknownConfigKeys(decoded); len(got) != 0 {
		t.Fatalf("unknownConfigKeys = %v, want none (removed discovery keys are ignored silently)", got)
	}

	s, err = NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	d := s.Current().Tools.Discovery
	if !d.Enabled || d.MaxSearchResults != 7 || !slices.Equal(d.AlwaysShownNamespaces, []string{"fusion"}) {
		t.Fatalf("discovery = %+v, want enabled, max_search_results 7, always_shown_namespaces [fusion]", d)
	}

	if err := s.Update(func(c *Config) error {
		c.Tools.Discovery.MaxSearchResults = 8
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	saved := readConfigDoc(t, path)
	savedTools, ok := saved["tools"].(map[string]any)
	if !ok {
		t.Fatal("saved config has no tools object")
	}
	discovery, ok := savedTools["discovery"].(map[string]any)
	if !ok {
		t.Fatal("saved config has no tools.discovery object")
	}
	for _, key := range []string{"ttl_max", "visible_budget", "ttl"} {
		if _, present := discovery[key]; present {
			t.Errorf("save wrote back removed key tools.discovery.%s", key)
		}
	}
	if got, ok := discovery["max_search_results"].(float64); !ok || got != 8 {
		t.Errorf("saved max_search_results = %v, want 8", discovery["max_search_results"])
	}
}

// Only the three removed discovery keys are silent: any other unknown key
// under tools.discovery is still reported.
func TestDiscoveryConfig_OtherUnknownKeyStillReported(t *testing.T) {
	doc, err := decodeDocument([]byte(`{"tools": {"discovery": {"ttl_max": 1, "enabeld": true}}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"tools.discovery.enabeld"}
	if got := unknownConfigKeys(doc); !slices.Equal(got, want) {
		t.Fatalf("unknownConfigKeys = %v, want %v", got, want)
	}
}
