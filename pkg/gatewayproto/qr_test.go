package gatewayproto

import (
	"encoding/json"
	"testing"
)

func TestSetupPayloadEncode(t *testing.T) {
	p := NewSetupPayload([]string{"192.168.1.10", "10.0.0.5"}, 18790, "sektok", "")
	s, err := p.Encode()
	if err != nil {
		t.Fatal(err)
	}
	// Must round-trip and carry the exact legacy type/version the R1 firmware expects.
	var got map[string]any
	if err := json.Unmarshal([]byte(s), &got); err != nil {
		t.Fatalf("not valid json: %v (%s)", err, s)
	}
	if got["type"] != "clawdbot-gateway" {
		t.Fatalf("type=%v want clawdbot-gateway", got["type"])
	}
	if got["version"].(float64) != 1 {
		t.Fatalf("version=%v want 1", got["version"])
	}
	if got["protocol"] != "ws" {
		t.Fatalf("protocol=%v want ws (default)", got["protocol"])
	}
	if got["port"].(float64) != 18790 {
		t.Fatalf("port=%v", got["port"])
	}
	ips, _ := got["ips"].([]any)
	if len(ips) != 2 {
		t.Fatalf("ips=%v", got["ips"])
	}
}
