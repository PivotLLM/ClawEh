// ClawEh
// License: MIT

package alerts

import (
	"strconv"
	"testing"
	"time"
)

var incidentStart = time.Date(2026, time.November, 14, 9, 2, 0, 0, time.UTC)

// TestIncidents_FirstThenDaily: the first report alerts, repeats inside
// RemindEvery are held and counted, and the first one after it is a reminder
// that says since when and how many were held.
func TestIncidents_FirstThenDaily(t *testing.T) {
	var in Incidents
	now := incidentStart

	first := in.Report("m", now)
	if !first.Alert || first.Ongoing || !first.Since.Equal(now) || first.Note() != "" {
		t.Fatalf("first report must alert as a new incident, got %+v note %q", first, first.Note())
	}

	for i := 1; i <= 3; i++ {
		o := in.Report("m", now.Add(time.Duration(i)*time.Hour))
		if o.Alert || !o.Ongoing || o.Held != i {
			t.Fatalf("report %d inside the day must be held, got %+v", i, o)
		}
	}
	if o := in.Report("m", now.Add(RemindEvery-time.Second)); o.Alert {
		t.Fatalf("a second short of RemindEvery must still be held, got %+v", o)
	}

	reminder := in.Report("m", now.Add(RemindEvery))
	if !reminder.Alert || !reminder.Ongoing || reminder.Held != 4 || !reminder.Since.Equal(now) {
		t.Fatalf("report at RemindEvery must be a reminder counting 4 held, got %+v", reminder)
	}
	if want := "; ongoing since Nov 14 09:02, 4 more since the last alert"; reminder.Note() != want {
		t.Fatalf("note = %q, want %q", reminder.Note(), want)
	}

	// The reminder restarts the day and the count.
	if o := in.Report("m", now.Add(RemindEvery+time.Hour)); o.Alert || o.Held != 1 {
		t.Fatalf("report after the reminder must be held with a fresh count, got %+v", o)
	}
}

// TestIncidents_NoteWithoutHeld: a reminder that held nothing back says only
// since when.
func TestIncidents_NoteWithoutHeld(t *testing.T) {
	var in Incidents
	in.Report("m", incidentStart)
	o := in.Report("m", incidentStart.Add(3*RemindEvery))
	if want := "; ongoing since Nov 14 09:02"; !o.Alert || o.Note() != want {
		t.Fatalf("got %+v note %q, want an alert with note %q", o, o.Note(), want)
	}
}

// TestIncidents_KeysAreIndependent: one key's incident holds nothing of
// another's.
func TestIncidents_KeysAreIndependent(t *testing.T) {
	var in Incidents
	in.Report("a", incidentStart)
	if o := in.Report("b", incidentStart.Add(time.Minute)); !o.Alert || o.Ongoing {
		t.Fatalf("another key must alert as new, got %+v", o)
	}
}

// TestIncidents_ResolveStartsAfresh: Resolve returns the incident's start
// once, and the next report is a new incident that alerts at once.
func TestIncidents_ResolveStartsAfresh(t *testing.T) {
	var in Incidents
	if _, ok := in.Resolve("m"); ok {
		t.Fatal("nothing to resolve on an empty Incidents")
	}
	in.Report("m", incidentStart)
	in.Report("m", incidentStart.Add(time.Hour))

	since, ok := in.Resolve("m")
	if !ok || !since.Equal(incidentStart) {
		t.Fatalf("Resolve = %v, %v; want the start and true", since, ok)
	}
	if _, ok := in.Resolve("m"); ok {
		t.Fatal("an incident resolves once")
	}

	again := incidentStart.Add(2 * time.Hour)
	if o := in.Report("m", again); !o.Alert || o.Ongoing || !o.Since.Equal(again) || o.Held != 0 {
		t.Fatalf("a report after Resolve must alert as a new incident, got %+v", o)
	}
}

// TestIncidents_ResolveAll ends every incident and says whether there was one.
func TestIncidents_ResolveAll(t *testing.T) {
	var in Incidents
	if in.ResolveAll() {
		t.Fatal("nothing to resolve on an empty Incidents")
	}
	in.Report("a", incidentStart)
	in.Report("b", incidentStart)
	if !in.ResolveAll() {
		t.Fatal("ResolveAll must report the incidents it ended")
	}
	if in.ResolveAll() {
		t.Fatal("nothing is left after ResolveAll")
	}
	if o := in.Report("a", incidentStart.Add(time.Minute)); !o.Alert || o.Ongoing {
		t.Fatalf("a report after ResolveAll must alert as new, got %+v", o)
	}
}

// TestIncidents_RearmAlertsNextReport: a re-armed incident alerts on its next
// report inside the day, as a reminder that keeps the start, and is still
// there to resolve.
func TestIncidents_RearmAlertsNextReport(t *testing.T) {
	var in Incidents
	in.Rearm("m") // unknown key: nothing to do
	in.Report("m", incidentStart)
	in.Report("m", incidentStart.Add(time.Minute))

	in.Rearm("m")
	o := in.Report("m", incidentStart.Add(2*time.Minute))
	if !o.Alert || !o.Ongoing || !o.Since.Equal(incidentStart) || o.Held != 1 {
		t.Fatalf("report after Rearm must alert as a reminder, got %+v", o)
	}
	if o := in.Report("m", incidentStart.Add(3*time.Minute)); o.Alert {
		t.Fatalf("Rearm covers one report, got %+v", o)
	}

	in.RearmAll()
	if o := in.Report("m", incidentStart.Add(4*time.Minute)); !o.Alert {
		t.Fatalf("report after RearmAll must alert, got %+v", o)
	}
	if since, ok := in.Resolve("m"); !ok || !since.Equal(incidentStart) {
		t.Fatalf("a re-armed incident still resolves with its start, got %v, %v", since, ok)
	}
}

// TestIncidents_PrunesOldOnes: past maxIncidents, incidents whose last alert
// is RemindEvery old make room; recent ones stay.
func TestIncidents_PrunesOldOnes(t *testing.T) {
	var in Incidents
	for i := range maxIncidents - 1 {
		in.Report("old"+strconv.Itoa(i), incidentStart)
	}
	recent := incidentStart.Add(RemindEvery)
	in.Report("recent", recent)
	if len(in.open) != maxIncidents {
		t.Fatalf("open = %d, want %d", len(in.open), maxIncidents)
	}

	in.Report("new", recent.Add(time.Minute))
	if len(in.open) != 2 {
		t.Fatalf("open = %d after pruning, want the recent and the new one", len(in.open))
	}
	if o := in.Report("recent", recent.Add(2*time.Minute)); o.Alert {
		t.Fatalf("a recent incident must survive pruning and stay held, got %+v", o)
	}
}
