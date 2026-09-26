package matrix

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/testalerts"
)

// startSyncAgainst runs the channel against a fake homeserver.
func startSyncAgainst(t *testing.T, h http.HandlerFunc) (*MatrixChannel, *testalerts.Recorder) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	c, err := NewMatrixChannel(config.MatrixConfig{
		Homeserver:  srv.URL,
		UserID:      "@claw:matrix.test",
		AccessToken: "token",
	}, bus.NewMessageBus())
	if err != nil {
		t.Fatal(err)
	}
	rec := &testalerts.Recorder{}
	c.SetAlerter(rec)
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Stop(context.Background()) })
	return c, rec
}

func waitFor(t *testing.T, what string, within time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A revoked access token alerts at once.
func TestRunSync_RevokedTokenAlerts(t *testing.T) {
	_, rec := startSyncAgainst(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"errcode":"M_UNKNOWN_TOKEN","error":"Invalid access token"}`)
	})
	waitFor(t, "credentials alert", 3*time.Second, func() bool { return len(rec.Alerts()) > 0 })
	a := rec.Alerts()[0]
	if a.Title != "Channel credentials rejected" || a.EventID != "matrix" {
		t.Fatalf("unexpected alert %+v", a)
	}
}

// A server error is tracked as an outage without alerting, and a later
// successful sync ends the outage.
func TestRunSync_ServerErrorTrackedThenRecovers(t *testing.T) {
	var failing atomic.Bool
	failing.Store(true)
	c, rec := startSyncAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		if failing.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/filter") {
			fmt.Fprint(w, `{"filter_id":"1"}`)
			return
		}
		time.Sleep(20 * time.Millisecond) // stand in for the long poll
		fmt.Fprint(w, `{"next_batch":"s1"}`)
	})

	waitFor(t, "outage report", 3*time.Second, func() bool { return !c.ConnDownSince().IsZero() })
	failing.Store(false)
	waitFor(t, "recovery", 15*time.Second, func() bool { return c.ConnDownSince().IsZero() })
	if n := len(rec.Alerts()); n != 0 {
		t.Fatalf("transient failure must not alert, got %d", n)
	}
}
