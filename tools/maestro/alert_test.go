// ClawEh
// License: MIT

package maestro

import (
	"errors"
	"testing"

	"github.com/PivotLLM/ClawEh/internal/testalerts"
)

// TestAlertMaestroDisabled: the alert is high and keyed by the agent, so an
// agent whose Maestro directory is broken is reported once per window.
func TestAlertMaestroDisabled(t *testing.T) {
	rec := testalerts.Install(t)
	alertMaestroDisabled("alice", "/ws/maestro", errors.New("permission denied"))
	got := rec.Alerts()
	if len(got) != 1 || !got[0].High || got[0].EventID != "maestro:alice" ||
		got[0].Title != "Maestro tools disabled" || got[0].Details != "permission denied" {
		t.Fatalf("got %+v", got)
	}
}
