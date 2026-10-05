package channels

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/logger"
)

// connWatch tracks one channel's connection outage. The zero value is ready:
// no outage in progress.
type connWatch struct {
	mu sync.Mutex
	// alertAfter overrides ConnDownAlertAfter when non-zero (tests only).
	alertAfter time.Duration
	downSince  time.Time // zero while the connection works
	lastErr    string
	timer      *time.Timer
	alerted    bool // the outage has outlasted the alert threshold
}

func (w *connWatch) resetLocked() {
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
	w.downSince = time.Time{}
	w.lastErr = ""
	w.alerted = false
}

// ReportConnected records that the channel's connection to its service works,
// ending any outage in progress. Channels call it on every successful poll,
// connect or reconnect; it is cheap when nothing is wrong. When it is the last
// channel of its platform to recover from an alerted outage, it raises the
// "<Platform> up" alert.
func (c *BaseChannel) ReportConnected() {
	w := &c.conn
	w.mu.Lock()
	if w.downSince.IsZero() {
		w.mu.Unlock()
		return
	}
	if w.alerted {
		logger.InfoCF("channels", "Channel connection restored", map[string]any{
			"channel":  c.name,
			"down_for": time.Since(w.downSince).Round(time.Second).String(),
		})
	}
	w.resetLocked()
	up := c.outages.connected(c)
	w.mu.Unlock()
	c.sendConnAlert(up)
}

// ReportConnFailure records a failed connection attempt that the channel is
// retrying. The first failure of an outage starts the clock; if no
// ReportConnected arrives within ConnDownAlertAfter, the channel joins its
// platform's outage, and the first channel of the platform to get there raises
// one "<Platform> down" alert. Faults that no retry can fix (a revoked token)
// are alerted by the channel itself, at once.
func (c *BaseChannel) ReportConnFailure(err error) {
	w := &c.conn
	w.mu.Lock()
	defer w.mu.Unlock()
	if err != nil {
		w.lastErr = err.Error()
	}
	if !w.downSince.IsZero() {
		c.outages.failure(c, w.downSince, w.lastErr)
		return
	}
	after := w.alertAfter
	if after <= 0 {
		after = ConnDownAlertAfter
	}
	start := time.Now()
	w.downSince = start
	c.outages.failure(c, start, w.lastErr)
	w.timer = time.AfterFunc(after, func() { c.connDownExpired(start, after) })
}

// ConnDownSince returns when the current connection outage began, or the zero
// time while the connection works.
func (c *BaseChannel) ConnDownSince() time.Time {
	c.conn.mu.Lock()
	defer c.conn.mu.Unlock()
	return c.conn.downSince
}

// connDownExpired runs when an outage that began at start has lasted after.
func (c *BaseChannel) connDownExpired(start time.Time, after time.Duration) {
	w := &c.conn
	w.mu.Lock()
	if !w.downSince.Equal(start) || w.alerted {
		w.mu.Unlock()
		return
	}
	if !c.IsRunning() {
		// Stopped mid-outage: nothing is retrying, so there is nothing to
		// report. A later Start begins a fresh outage.
		w.resetLocked()
		a := c.outages.stopped(c)
		w.mu.Unlock()
		c.sendConnAlert(a)
		return
	}
	w.alerted = true
	w.timer = nil
	down := c.outages.expired(c, after)
	w.mu.Unlock()
	c.sendConnAlert(down)
}

// connStopped ends the outage of a channel that has stopped: it no longer
// retries, so it leaves its platform's outage.
func (c *BaseChannel) connStopped() {
	w := &c.conn
	w.mu.Lock()
	if w.downSince.IsZero() {
		w.mu.Unlock()
		return
	}
	w.resetLocked()
	a := c.outages.stopped(c)
	w.mu.Unlock()
	c.sendConnAlert(a)
}

func (c *BaseChannel) sendConnAlert(a *alerter.Alert) {
	if a != nil {
		c.Alert(*a)
	}
}

// SetPlatform sets the platform the channel belongs to ("telegram", "slack"),
// which groups its connection outages with the platform's other channels. The
// Manager injects it; without it the platform is the channel name up to the
// first "-".
func (c *BaseChannel) SetPlatform(platform string) { c.platform = platform }

func (c *BaseChannel) platformID() string {
	if c.platform != "" {
		return c.platform
	}
	p, _, _ := strings.Cut(c.name, "-")
	return p
}

// connOutages is the process-wide outage aggregator. It is package-level so
// an outage spanning a config reload keeps its alert state.
var connOutages = newConnAggregator()

// connAggregator groups channel connection outages by platform, so an outage
// that takes down every bot of a platform raises one alert, not one per bot.
type connAggregator struct {
	mu        sync.Mutex
	platforms map[string]*platformOutage
}

type platformOutage struct {
	down     map[*BaseChannel]*channelOutage // channels with an outage in progress
	affected map[*BaseChannel]struct{}       // channels counted in the active alert
	alerted  bool
	since    time.Time // start of the earliest affected outage
	// recovered is set once an affected channel reconnects; an outage whose
	// channels all stopped instead ends without an "up" alert.
	recovered bool
}

type channelOutage struct {
	since   time.Time
	lastErr string
}

func newConnAggregator() *connAggregator {
	return &connAggregator{platforms: make(map[string]*platformOutage)}
}

func (g *connAggregator) platformLocked(id string) *platformOutage {
	p := g.platforms[id]
	if p == nil {
		p = &platformOutage{
			down:     make(map[*BaseChannel]*channelOutage),
			affected: make(map[*BaseChannel]struct{}),
		}
		g.platforms[id] = p
	}
	return p
}

// failure records that c has been down since since, most recently with lastErr.
func (g *connAggregator) failure(c *BaseChannel, since time.Time, lastErr string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.platformLocked(c.platformID()).down[c] = &channelOutage{since: since, lastErr: lastErr}
}

// expired records that c's outage has lasted after. The first channel of a
// platform to get there returns the "<Platform> down" alert, which counts every
// channel of the platform then down; later ones join it silently.
func (g *connAggregator) expired(c *BaseChannel, after time.Duration) *alerter.Alert {
	g.mu.Lock()
	defer g.mu.Unlock()
	id := c.platformID()
	p := g.platformLocked(id)
	mine, ok := p.down[c]
	if !ok {
		return nil
	}
	if p.alerted {
		p.affected[c] = struct{}{}
		return nil
	}
	p.alerted = true
	p.since = mine.since
	names := make([]string, 0, len(p.down))
	for ch, o := range p.down {
		p.affected[ch] = struct{}{}
		names = append(names, ch.name)
		if o.since.Before(p.since) {
			p.since = o.since
		}
	}
	slices.Sort(names)
	return &alerter.Alert{
		Title:       platformDisplayName(id) + " down",
		Description: fmt.Sprintf("%s, no working connection for %s", countNoun(id, len(names)), shortDuration(after)),
		Details:     "Channels: " + strings.Join(names, ", ") + "\nLast error: " + mine.lastErr,
		EventID:     id,
	}
}

// connected records that c's connection works again, returning the
// "<Platform> up" alert when it was the last affected channel to recover.
func (g *connAggregator) connected(c *BaseChannel) *alerter.Alert {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.leaveLocked(c, true)
}

// stopped records that c stopped and no longer retries.
func (g *connAggregator) stopped(c *BaseChannel) *alerter.Alert {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.leaveLocked(c, false)
}

func (g *connAggregator) leaveLocked(c *BaseChannel, recovered bool) *alerter.Alert {
	id := c.platformID()
	p := g.platformLocked(id)
	delete(p.down, c)
	if _, ok := p.affected[c]; !ok {
		return nil
	}
	delete(p.affected, c)
	if recovered {
		p.recovered = true
	}
	if len(p.affected) > 0 {
		return nil
	}
	var up *alerter.Alert
	if p.recovered {
		up = &alerter.Alert{
			Title:       platformDisplayName(id) + " up",
			Description: "back after " + shortDuration(time.Since(p.since)),
			EventID:     id + "-up",
		}
	}
	p.alerted, p.recovered, p.since = false, false, time.Time{}
	return up
}

// platformNames are the display names of the channel platforms.
var platformNames = map[string]string{
	"telegram": "Telegram",
	"discord":  "Discord",
	"slack":    "Slack",
	"matrix":   "Matrix",
	"line":     "LINE",
	"secmsg":   "SecMsg",
	"webui":    "WebUI",
	"device":   "Device",
}

func platformDisplayName(id string) string {
	if n, ok := platformNames[id]; ok {
		return n
	}
	if id == "" {
		return id
	}
	return strings.ToUpper(id[:1]) + id[1:]
}

// countNoun is "1 bot", "7 bots" for bot platforms and "1 connection",
// "2 connections" for the others.
func countNoun(platform string, n int) string {
	noun := "connection"
	switch platform {
	case "telegram", "discord", "slack":
		noun = "bot"
	}
	if n != 1 {
		noun += "s"
	}
	return fmt.Sprintf("%d %s", n, noun)
}

// shortDuration formats d to the minute from a minute up ("10m", "1h5m",
// "2h"), and to the second below.
func shortDuration(d time.Duration) string {
	if d < time.Minute {
		return d.Round(time.Second).String()
	}
	s := strings.TrimSuffix(d.Round(time.Minute).String(), "0s")
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// NextConnRetry returns the wait before the next connection attempt after a
// wait of cur: ConnRetryMin when cur is zero, otherwise double cur, capped at
// ConnRetryMax.
func NextConnRetry(cur time.Duration) time.Duration {
	if cur <= 0 {
		return ConnRetryMin
	}
	return min(cur*2, ConnRetryMax)
}
