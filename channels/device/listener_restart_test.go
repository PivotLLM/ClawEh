// ClawEh
// License: MIT

package device

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/testalerts"
	"github.com/PivotLLM/ClawEh/utils"
)

// freePort asks the kernel for an unused loopback port.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("unexpected listener address type %T", ln.Addr())
	}
	port := tcpAddr.Port
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

// listenRecorder wraps listenTCP: it keeps every listener it hands out (so the
// test can kill the live one) and can fail a given number of binds first.
type listenRecorder struct {
	mu        sync.Mutex
	listeners []net.Listener
	failNext  int
	binds     int
}

func (r *listenRecorder) listen(addr string) (net.Listener, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.binds++
	if r.failNext > 0 {
		r.failNext--
		return nil, errors.New("bind: address already in use")
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	r.listeners = append(r.listeners, ln)
	return ln, nil
}

func (r *listenRecorder) last() net.Listener {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.listeners[len(r.listeners)-1]
}

func (r *listenRecorder) bindCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.binds
}

// installSeams makes re-listen waits immediate and routes binds through rec.
func installSeams(t *testing.T, rec *listenRecorder) {
	t.Helper()
	prevListen, prevAfter := listenTCP, retryAfter
	listenTCP = rec.listen
	retryAfter = func(time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		ch <- time.Time{}
		return ch
	}
	t.Cleanup(func() { listenTCP, retryAfter = prevListen, prevAfter })
}

func canConnect(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
	if err != nil {
		return false
	}
	utils.CloseQuietly(conn)
	return true
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestDeviceListenerRestartsAfterServeError: when the listener dies the
// channel alerts once, re-binds (retrying a failed bind), and serves again.
// Stop then ends the loop and frees the port.
func TestDeviceListenerRestartsAfterServeError(t *testing.T) {
	rec := &listenRecorder{}
	installSeams(t, rec)

	port := freePort(t)
	dc, err := NewDeviceChannel(config.DeviceChannelConfig{Enabled: true, Host: "127.0.0.1", Port: port}, t.TempDir(), false, bus.NewMessageBus(), "", nil)
	if err != nil {
		t.Fatalf("NewDeviceChannel: %v", err)
	}
	alerts := &testalerts.Recorder{}
	dc.SetAlerter(alerts)
	if err := dc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	waitFor(t, "initial listener", func() bool { return canConnect(addr) })

	// Kill the live listener: Serve's Accept fails and the loop must re-bind.
	// The first re-bind is refused (port in use) and must be retried.
	rec.mu.Lock()
	rec.failNext = 1
	rec.mu.Unlock()
	if err := rec.last().Close(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "listener restored", func() bool { return rec.bindCount() >= 3 && canConnect(addr) })

	got := alerts.Alerts()
	if len(got) != 1 {
		t.Fatalf("alerts = %d, want exactly 1: %+v", len(got), got)
	}
	if got[0].Title != "Channel receive loop stopped" {
		t.Fatalf("alert title = %q", got[0].Title)
	}
	if !dc.IsRunning() {
		t.Fatal("channel should still report running across a listener restart")
	}

	if err := dc.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case <-dc.loopDone:
	default:
		t.Fatal("Stop returned before the serve loop exited")
	}
	if canConnect(addr) {
		t.Fatal("listener still accepting after Stop")
	}
	if n := rec.bindCount(); n != 3 {
		t.Fatalf("binds after Stop = %d, want 3 (no re-listen after stop)", n)
	}
}

// TestDeviceListenerStopsOnContextCancel: cancelling the context Start was
// given shuts the listener down without an alert; the loop does not re-bind.
func TestDeviceListenerStopsOnContextCancel(t *testing.T) {
	rec := &listenRecorder{}
	installSeams(t, rec)

	port := freePort(t)
	dc, err := NewDeviceChannel(config.DeviceChannelConfig{Enabled: true, Host: "127.0.0.1", Port: port}, t.TempDir(), false, bus.NewMessageBus(), "", nil)
	if err != nil {
		t.Fatalf("NewDeviceChannel: %v", err)
	}
	alerts := &testalerts.Recorder{}
	dc.SetAlerter(alerts)
	ctx, cancel := context.WithCancel(context.Background())
	if err := dc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	waitFor(t, "initial listener", func() bool { return canConnect(addr) })

	cancel()
	select {
	case <-dc.loopDone:
	case <-time.After(5 * time.Second):
		t.Fatal("serve loop did not exit on context cancel")
	}
	if canConnect(addr) {
		t.Fatal("listener still accepting after context cancel")
	}
	if n := len(alerts.Alerts()); n != 0 {
		t.Fatalf("alerts = %d, want 0 for an intentional stop", n)
	}
	if n := rec.bindCount(); n != 1 {
		t.Fatalf("binds = %d, want 1", n)
	}
	if err := dc.Stop(context.Background()); err != nil {
		t.Fatalf("Stop after cancel: %v", err)
	}
}
