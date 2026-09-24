// ClawEh
// License: MIT

package mcp

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/server"
	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/config"
)

type alertRecorder struct {
	mu     sync.Mutex
	alerts []alerter.Alert
}

func (r *alertRecorder) Send(a alerter.Alert) {
	r.mu.Lock()
	r.alerts = append(r.alerts, a)
	r.mu.Unlock()
}
func (r *alertRecorder) High(string, string, ...string) {}
func (r *alertRecorder) Low(string, string, ...string)  {}
func (r *alertRecorder) Close(context.Context) error    { return nil }

// TestRetryDisconnected_AlertsUnreachable: a desired server that cannot be
// connected raises one low alert keyed by its name.
func TestRetryDisconnected_AlertsUnreachable(t *testing.T) {
	mgr := NewManager()
	mgr.reconnectCooldown = time.Minute
	defer func() {
		if err := mgr.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()
	rec := &alertRecorder{}
	mgr.SetAlerter(rec)

	down := httptest.NewServer(server.NewStreamableHTTPServer(server.NewMCPServer("down", "0.0.1")))
	downURL := down.URL
	down.Close()
	mgr.setDesired(map[string]config.MCPServerConfig{
		"down": {Enabled: true, Type: "http", URL: downURL},
	})
	mgr.RetryDisconnected(context.Background())

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.alerts) != 1 || rec.alerts[0].High || rec.alerts[0].EventID != "down" ||
		rec.alerts[0].Title != "MCP server unreachable" {
		t.Fatalf("expected one low alert for 'down', got %+v", rec.alerts)
	}
}
