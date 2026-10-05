package mcp

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/tenebris-tech/alerter"
)

// Alert cadence and retry backoff for servers that cannot be (re)connected.
const (
	// alertReminderInterval is the least time between two alerts for a server
	// that stays unreachable.
	alertReminderInterval = time.Hour
	// maxReconnectBackoff caps the wait between attempts for a server that
	// keeps failing (unless the configured initial cooldown is longer).
	maxReconnectBackoff = 10 * time.Minute
	// alertReasonMax bounds the alert description, which is sent by SMS.
	alertReasonMax = 120
)

// serverHealth is the per-server failure state the manager keeps across
// attempts and config reloads. Guarded by Manager.cooldownMu.
type serverHealth struct {
	failures  int       // consecutive failed attempts; drives the backoff
	downSince time.Time // zero while the server is not reported down
	lastAlert time.Time // last down alert or reminder
	lastErr   string    // failure message, including the stderr tail
	lastErrAt time.Time
}

// backoff returns the wait before the next attempt after the given number of
// consecutive failures: the initial cooldown, doubling per failure, capped at
// maxReconnectBackoff (or the initial cooldown when that is longer).
func backoff(initial time.Duration, failures int) time.Duration {
	limit := max(maxReconnectBackoff, initial)
	d := initial
	for i := 1; i < failures && d < limit; i++ {
		d *= 2
	}
	return min(d, limit)
}

// recordFailure notes a failed connect or reconnect of name: it extends the
// backoff, stores the error for Status, and alerts when the server has just
// become unreachable or a reminder is due.
func (m *Manager) recordFailure(name string, err error) {
	now := m.now()
	var alert *alerter.Alert

	m.cooldownMu.Lock()
	h := m.healthLocked(name)
	h.failures++
	h.lastErr = err.Error()
	h.lastErrAt = now
	m.cooldownUntil[name] = now.Add(backoff(m.reconnectCooldown, h.failures))
	switch {
	case h.downSince.IsZero():
		h.downSince = now
		h.lastAlert = now
		a := downAlert(name, err, "")
		alert = &a
	case now.Sub(h.lastAlert) >= alertReminderInterval:
		h.lastAlert = now
		a := downAlert(name, err, "still down since "+h.downSince.Format("15:04")+": ")
		alert = &a
	}
	m.cooldownMu.Unlock()

	if alert != nil {
		m.sendAlert(*alert)
	}
}

// recordSuccess notes a successful connect of name: the backoff resets, the
// stored error clears, and a server that was reported down gets one recovery
// alert.
func (m *Manager) recordSuccess(name string) {
	now := m.now()
	var downSince time.Time

	m.cooldownMu.Lock()
	delete(m.cooldownUntil, name)
	if h, ok := m.health[name]; ok {
		downSince = h.downSince
		delete(m.health, name)
	}
	m.cooldownMu.Unlock()

	if !downSince.IsZero() {
		m.sendAlert(alerter.Alert{
			Title:       "MCP " + name + " up",
			Description: "reconnected after " + formatDowntime(now.Sub(downSince)),
			EventID:     name + "-up",
		})
	}
}

// forgetHealth drops the failure state of servers no longer desired, so a
// server removed or disabled while down neither lingers in Status nor alerts.
func (m *Manager) forgetHealth(desired map[string]bool) {
	m.cooldownMu.Lock()
	defer m.cooldownMu.Unlock()
	for name := range m.health {
		if !desired[name] {
			delete(m.health, name)
			delete(m.cooldownUntil, name)
		}
	}
}

// healthLocked returns name's state, creating it. Caller holds cooldownMu.
func (m *Manager) healthLocked(name string) *serverHealth {
	h, ok := m.health[name]
	if !ok {
		h = &serverHealth{}
		m.health[name] = h
	}
	return h
}

// lastError returns the stored failure message and time for name.
func (m *Manager) lastError(name string) (string, time.Time) {
	m.cooldownMu.Lock()
	defer m.cooldownMu.Unlock()
	if h, ok := m.health[name]; ok {
		return h.lastErr, h.lastErrAt
	}
	return "", time.Time{}
}

// sendAlert hands a to the alerter, when one is set.
func (m *Manager) sendAlert(a alerter.Alert) {
	m.mu.RLock()
	al := m.alerter
	m.mu.RUnlock()
	if al != nil {
		al.Send(a)
	}
}

// downAlert builds the unreachable alert. The description is short enough for
// SMS and prefers the child's own message over the transport error; the full
// error and stderr tail go in the details.
func downAlert(name string, err error, prefix string) alerter.Alert {
	reason := err.Error()
	var lines []string
	if se, ok := errors.AsType[*stderrError](err); ok {
		lines = se.lines
		reason = lines[len(lines)-1]
	}

	var details strings.Builder
	details.WriteString(err.Error())
	if len(lines) > 0 {
		details.WriteString("\n\nstderr:\n")
		details.WriteString(strings.Join(lines, "\n"))
	}
	details.WriteString("\n\nIts tools are unavailable until it reconnects; reconnects are retried after the cooldown, or force one from the MCP servers page.")

	return alerter.Alert{
		Title:       "MCP " + name + " down",
		Description: truncateRunes(oneLine(prefix+reason), alertReasonMax),
		Details:     details.String(),
		EventID:     name,
	}
}

// formatDowntime renders d compactly: "45s", "12m", "2h5m".
func formatDowntime(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// oneLine collapses newlines and surrounding space so s fits on one line.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// truncateRunes shortens s to at most n runes, marking a cut with "...".
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-3]) + "..."
}

// Bounds of the stderr kept per stdio server.
const (
	stderrTailBytes = 4096
	stderrTailLines = 20
)

// stderrTail keeps the last stderrTailBytes a stdio child wrote to stderr.
// It is the child's cmd.Stderr, so writes come from the exec copy goroutine.
type stderrTail struct {
	mu        sync.Mutex
	buf       []byte
	truncated bool
}

// Write appends p, dropping the oldest bytes beyond the bound. It never fails,
// so the child can never block on stderr.
func (t *stderrTail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - stderrTailBytes; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
		t.truncated = true
	}
	return len(p), nil
}

// Lines returns up to the last stderrTailLines non-empty lines, oldest first.
// A line cut by the byte bound is dropped.
func (t *stderrTail) Lines() []string {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	s := string(t.buf)
	truncated := t.truncated
	t.mu.Unlock()

	if truncated {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		} else {
			s = ""
		}
	}
	var lines []string
	for line := range strings.SplitSeq(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) > stderrTailLines {
		lines = lines[len(lines)-stderrTailLines:]
	}
	return lines
}

// last returns the last non-empty stderr line, or "".
func (t *stderrTail) last() string {
	lines := t.Lines()
	if len(lines) == 0 {
		return ""
	}
	return lines[len(lines)-1]
}

// stderrError is a connect error carrying the stdio child's stderr tail. Its
// message appends the last line; the alert details use all of them.
type stderrError struct {
	err   error
	lines []string
}

func (e *stderrError) Error() string {
	return e.err.Error() + "; stderr: " + e.lines[len(e.lines)-1]
}

func (e *stderrError) Unwrap() error { return e.err }

// withStderr attaches tail's lines to err, or returns err unchanged when the
// child wrote nothing to stderr.
func withStderr(err error, tail *stderrTail) error {
	lines := tail.Lines()
	if len(lines) == 0 {
		return err
	}
	return &stderrError{err: err, lines: lines}
}
