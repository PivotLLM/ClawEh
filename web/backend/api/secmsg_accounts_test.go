package api

import (
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/config"
)

func TestFindSecMsgDaemon(t *testing.T) {
	cfg := &config.Config{}
	cfg.Channels.SecMsg = []config.SecMsgConfig{
		{Name: "signal", Address: "127.0.0.1:9600"},
		{Name: "", Address: "127.0.0.1:9601"}, // unnamed → addressable as "secmsg"
	}

	tests := []struct {
		name     string
		lookup   string
		wantAddr string
		wantOK   bool
	}{
		{"by explicit name", "signal", "127.0.0.1:9600", true},
		{"unnamed via secmsg alias", "secmsg", "127.0.0.1:9601", true},
		{"unknown", "telegram", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, ok := findSecMsgDaemon(cfg, tt.lookup)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && d.Address != tt.wantAddr {
				t.Errorf("address = %q, want %q", d.Address, tt.wantAddr)
			}
		})
	}
}
