// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package classifier

import "time"

// Definition is one classifier, stored as <base>/<id>.json. Owner and the
// dates are written by ClawEh: values a caller gives for them are replaced.
type Definition struct {
	ID            string            `json:"id"`
	Owner         string            `json:"owner"`
	Enabled       bool              `json:"enabled"`
	DryRun        bool              `json:"dry_run"`
	Trigger       Trigger           `json:"trigger"`
	Source        Source            `json:"source"`
	Fetch         *Fetch            `json:"fetch,omitempty"`
	Classify      Classify          `json:"classify"`
	Actions       map[string]Action `json:"actions,omitempty"` // category name -> action
	Limits        Limits            `json:"limits,omitzero"`
	Report        Report            `json:"report,omitzero"`
	ActivityHours int               `json:"activity_hours,omitempty"` // 0 = DefaultActivityHours
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
}

// Trigger says when a classifier runs: every EverySeconds seconds, on its
// own timer.
type Trigger struct {
	EverySeconds int `json:"every_seconds,omitempty"`
}

// Source is the tool that lists the items. Items is the dot-path to the list
// in its result ("" = the whole result); ID the path, inside one element, to
// the item's stable id.
type Source struct {
	Tool  string         `json:"tool"`
	Args  map[string]any `json:"args,omitempty"`
	Items string         `json:"items"`
	ID    string         `json:"id"`
}

// Fetch is the optional tool that reads one item. Fields names what is sent
// to the model (name -> dot-path in the result); HTML names the fields whose
// HTML is reduced to text; MaxChars caps the fields together (0 = the
// install's max_item_chars).
type Fetch struct {
	Tool     string            `json:"tool"`
	Args     map[string]any    `json:"args,omitempty"`
	Fields   map[string]string `json:"fields"`
	HTML     []string          `json:"html,omitempty"`
	MaxChars int               `json:"max_chars,omitempty"`
}

// Classify is the question asked about each item.
type Classify struct {
	Instructions string     `json:"instructions"`
	Rules        []string   `json:"rules,omitempty"`
	Categories   []Category `json:"categories"`
}

// Category is one possible answer.
type Category struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// Action is the tool run for items of one category.
type Action struct {
	Tool string         `json:"tool"`
	Args map[string]any `json:"args,omitempty"`
}

// Limits bounds one run. 0 uses the default: ItemsPerRun the install's
// max_items_per_run, Parallel 1, StopAfterFailures DefaultStopAfterFailures.
type Limits struct {
	ItemsPerRun       int `json:"items_per_run,omitempty"`
	Parallel          int `json:"parallel,omitempty"`
	StopAfterFailures int `json:"stop_after_failures,omitempty"`
}

// Report says when a run's summary is posted to the owning agent's chat.
type Report struct {
	Summary string `json:"summary,omitempty"` // SummaryOnProblems (default), SummaryAlways or SummaryNever
}

// The values of report.summary.
const (
	SummaryOnProblems = "on_problems"
	SummaryAlways     = "always"
	SummaryNever      = "never"
)

var summaryValues = []string{SummaryOnProblems, SummaryAlways, SummaryNever}

// Unclassified is the outcome of an item whose model reply named no category
// twice. It is reserved: no category or action may use it.
const Unclassified = "unclassified"

// Defaults of a definition's optional values.
const (
	DefaultActivityHours     = 72
	DefaultParallel          = 1
	DefaultStopAfterFailures = 5
)

// ActivityValueMax caps each field value stored in the activity log, in
// characters.
const ActivityValueMax = 500

// ReasonMax caps the reason kept from a model reply, in characters.
const ReasonMax = 500

// Ceilings are the install's maximums and minimum interval (config
// classifiers.limits) and the processed-id retention
// (classifiers.processed_days). A zero field has no limit (ProcessedDays: ids
// are kept DefaultProcessedDays).
type Ceilings struct {
	MaxRules         int
	MaxPromptChars   int
	MaxItemChars     int
	MaxItemsPerRun   int
	MaxParallel      int
	MaxActivityHours int
	MinEverySeconds  int
	ProcessedDays    int
}

// DefaultProcessedDays is how long processed ids are kept when Ceilings sets
// no retention.
const DefaultProcessedDays = 30

// activityHours is the definition's activity retention.
func (d *Definition) activityHours() int {
	if d.ActivityHours > 0 {
		return d.ActivityHours
	}
	return DefaultActivityHours
}

// itemsPerRun is the definition's per-run cap under c.
func (d *Definition) itemsPerRun(c Ceilings) int {
	if d.Limits.ItemsPerRun > 0 {
		return d.Limits.ItemsPerRun
	}
	return c.MaxItemsPerRun // 0: no cap
}

// parallel is the number of items in flight at once.
func (d *Definition) parallel() int {
	if d.Limits.Parallel > 0 {
		return d.Limits.Parallel
	}
	return DefaultParallel
}

// stopAfterFailures is the number of consecutive failures that ends a run.
func (d *Definition) stopAfterFailures() int {
	if d.Limits.StopAfterFailures > 0 {
		return d.Limits.StopAfterFailures
	}
	return DefaultStopAfterFailures
}

// itemBudget is the characters the fields of one item may take together.
func (d *Definition) itemBudget(c Ceilings) int {
	if d.Fetch != nil && d.Fetch.MaxChars > 0 {
		return d.Fetch.MaxChars
	}
	return c.MaxItemChars // 0: no cap
}

// summaryMode is report.summary with its default.
func (d *Definition) summaryMode() string {
	if d.Report.Summary == "" {
		return SummaryOnProblems
	}
	return d.Report.Summary
}

// categoryNamed returns the definition's category whose name equals name
// ignoring case.
func (d *Definition) categoryNamed(name string) (Category, bool) {
	for _, c := range d.Classify.Categories {
		if equalFoldASCII(c.Name, name) {
			return c, true
		}
	}
	return Category{}, false
}

// processedDays is the retention of processed ids under c.
func (c Ceilings) processedDays() int {
	if c.ProcessedDays > 0 {
		return c.ProcessedDays
	}
	return DefaultProcessedDays
}

func equalFoldASCII(a, b string) bool { return NormalizeID(a) == NormalizeID(b) }
