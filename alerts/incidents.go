// ClawEh
// License: MIT

package alerts

import (
	"strconv"
	"sync"
	"time"
)

// RemindEvery is the least time between two alerts about one condition that
// lasts: a model out of credit, a revoked token, a server that stays down.
const RemindEvery = 24 * time.Hour

// maxIncidents bounds the incidents an Incidents keeps. Past it, those whose
// last alert is RemindEvery old are dropped: their next report alerts either
// way, only without the time the condition began.
const maxIncidents = 1024

// Incidents decides which reports of a lasting condition become alerts: the
// first, then one every RemindEvery for as long as the condition is still
// reported, however often that is. Its owner calls Report each time it meets
// the condition and Resolve when it sees the condition gone, so the next
// report starts a new incident and alerts at once.
//
// The alerter's own window (ten minutes) cannot do this: it is shorter than
// the pause between two retries of most conditions, and it cannot tell that a
// condition ended, so a longer one would also hold back a new problem.
//
// A key names one condition of one thing: a model, a channel, a job. The zero
// value is ready to use and safe for concurrent use. Callers pass the time,
// so each keeps the clock it already has.
type Incidents struct {
	mu   sync.Mutex
	open map[string]*incident
}

type incident struct {
	since     time.Time // the first report
	lastAlert time.Time // zero once re-armed: the next report alerts
	held      int       // reports held back since the last alert
}

// Occurrence is what Report decided about one report of a condition.
type Occurrence struct {
	// Alert says the report is to be sent: the incident is new, its last
	// alert is RemindEvery old, or it was re-armed.
	Alert bool
	// Ongoing says the incident was already open, so an alert is a reminder.
	Ongoing bool
	// Since is when the incident began.
	Since time.Time
	// Held counts the reports held back since the incident's last alert.
	Held int
}

// Note is what a reminder adds to an alert's description: since when the
// condition has lasted and how many reports were held back, for example
// "; ongoing since Nov 14 09:02, 47 more since the last alert". It is empty
// for the first alert of an incident.
func (o Occurrence) Note() string {
	if !o.Ongoing {
		return ""
	}
	note := "; ongoing since " + Stamp(o.Since)
	if o.Held > 0 {
		note += ", " + strconv.Itoa(o.Held) + " more since the last alert"
	}
	return note
}

// Stamp formats t for an alert's text, with the date: an incident outlasts
// the day it began on.
func Stamp(t time.Time) string {
	return t.Format("Jan 2 15:04")
}

// Report records that key's condition was met at now and says whether to
// alert about it.
func (in *Incidents) Report(key string, now time.Time) Occurrence {
	in.mu.Lock()
	defer in.mu.Unlock()

	inc := in.open[key]
	if inc == nil {
		if in.open == nil {
			in.open = make(map[string]*incident)
		}
		if len(in.open) >= maxIncidents {
			in.pruneLocked(now)
		}
		in.open[key] = &incident{since: now, lastAlert: now}
		return Occurrence{Alert: true, Since: now}
	}
	if !inc.lastAlert.IsZero() && now.Sub(inc.lastAlert) < RemindEvery {
		inc.held++
		return Occurrence{Ongoing: true, Since: inc.since, Held: inc.held}
	}
	o := Occurrence{Alert: true, Ongoing: true, Since: inc.since, Held: inc.held}
	inc.lastAlert, inc.held = now, 0
	return o
}

// Resolve ends key's incident: the condition is gone, and the next report
// alerts as a new one. It returns when the incident began and whether there
// was one, so the owner can tell the operator it is over.
func (in *Incidents) Resolve(key string) (since time.Time, ok bool) {
	in.mu.Lock()
	defer in.mu.Unlock()
	inc := in.open[key]
	if inc == nil {
		return time.Time{}, false
	}
	delete(in.open, key)
	return inc.since, true
}

// ResolveAll ends every incident and reports whether there was one.
func (in *Incidents) ResolveAll() bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	had := len(in.open) > 0
	clear(in.open)
	return had
}

// Rearm makes the next report of key's incident alert at once, without ending
// the incident. It is for an operator action that may have fixed the
// condition (clearing a model's cooldown): if it did not, the operator hears
// so at once; if it did, Resolve still knows since when the condition lasted.
func (in *Incidents) Rearm(key string) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if inc := in.open[key]; inc != nil {
		inc.lastAlert = time.Time{}
	}
}

// RearmAll re-arms every incident.
func (in *Incidents) RearmAll() {
	in.mu.Lock()
	defer in.mu.Unlock()
	for _, inc := range in.open {
		inc.lastAlert = time.Time{}
	}
}

// pruneLocked drops the incidents whose last alert, or start when re-armed,
// is RemindEvery old.
func (in *Incidents) pruneLocked(now time.Time) {
	for key, inc := range in.open {
		last := inc.lastAlert
		if last.IsZero() {
			last = inc.since
		}
		if now.Sub(last) >= RemindEvery {
			delete(in.open, key)
		}
	}
}
