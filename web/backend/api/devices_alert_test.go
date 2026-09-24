// ClawEh
// License: MIT

package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/tenebris-tech/alerter"
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

// TestDeviceStoreUnavailable_Alerts: a store-open failure answers 500 without
// the error text and raises one low alert; with no alerter set it still
// answers rather than panicking.
func TestDeviceStoreUnavailable_Alerts(t *testing.T) {
	h := NewHandler(setupTestEnv(t))
	w := httptest.NewRecorder()
	h.deviceStoreUnavailable(w, errors.New("open /data/state/gateway.db: locked"))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}

	rec := &alertRecorder{}
	h.SetAlerter(rec)
	w = httptest.NewRecorder()
	h.deviceStoreUnavailable(w, errors.New("open /data/state/gateway.db: locked"))
	if w.Code != http.StatusInternalServerError || w.Body.String() != `{"error":"store open failed"}`+"\n" {
		t.Fatalf("response must hide the error, got %d %q", w.Code, w.Body.String())
	}
	if len(rec.alerts) != 1 || rec.alerts[0].High || rec.alerts[0].EventID != "device-store" ||
		rec.alerts[0].Title != "Device store unavailable" ||
		rec.alerts[0].Details != "open /data/state/gateway.db: locked" {
		t.Fatalf("store failure must alert low once, got %+v", rec.alerts)
	}
}
