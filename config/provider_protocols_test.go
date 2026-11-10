// ClawEh
// License: MIT

package config

import (
	"encoding/json"
	"maps"
	"os"
	"slices"
	"testing"
)

// testdata/provider_protocols.json lists the protocols a provider may
// declare and the one the WebUI's provider forms start with. The frontend's
// provider-config-fields.test.ts checks its picker and default against the
// same file, so a form never offers or defaults to a protocol a save would
// refuse.
func TestProviderProtocolsFixture(t *testing.T) {
	b, err := os.ReadFile("testdata/provider_protocols.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Protocols       []string `json:"protocols"`
		DefaultProtocol string   `json:"default_protocol"`
	}
	if err = json.Unmarshal(b, &fixture); err != nil {
		t.Fatal(err)
	}
	want := slices.Sorted(maps.Keys(validProtocols))
	if !slices.Equal(fixture.Protocols, want) {
		t.Fatalf("provider_protocols.json protocols = %v, want %v", fixture.Protocols, want)
	}
	p := Provider{Name: "Alice", Protocol: fixture.DefaultProtocol, BaseURL: "https://api.example.com/v1"}
	if problem := protocolProblem(&p); problem != "" {
		t.Fatalf("default protocol %q is refused: %s", fixture.DefaultProtocol, problem)
	}
}
