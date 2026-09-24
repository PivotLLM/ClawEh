// ClawEh
// License: MIT

package devices

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/devices/events"
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

// fakeSource is an event source whose Start fails when err is set.
type fakeSource struct {
	kind events.Kind
	err  error
}

func (f *fakeSource) Kind() events.Kind { return f.kind }
func (f *fakeSource) Start(context.Context) (<-chan *events.DeviceEvent, error) {
	if f.err != nil {
		return nil, f.err
	}
	ch := make(chan *events.DeviceEvent)
	close(ch)
	return ch, nil
}
func (f *fakeSource) Stop() error { return nil }

// TestStart_AlertsWhenSourceFails: a source that fails to start raises one low
// alert keyed by its kind; a source that starts raises none, and the service
// still starts the others.
func TestStart_AlertsWhenSourceFails(t *testing.T) {
	rec := &alertRecorder{}
	s := NewService(Config{Enabled: true, Alerter: rec}, nil)
	s.sources = append(s.sources,
		&fakeSource{kind: events.KindUSB},
		&fakeSource{kind: events.KindBluetooth, err: errors.New("no adapter")},
	)
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop()

	if len(rec.alerts) != 1 || rec.alerts[0].High || rec.alerts[0].EventID != "devices:bluetooth" ||
		rec.alerts[0].Title != "Device source not started" || rec.alerts[0].Details != "no adapter" {
		t.Fatalf("failed source must alert low once keyed by kind, got %+v", rec.alerts)
	}
}
