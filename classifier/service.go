// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package classifier

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/PivotLLM/ClawEh/classifier/classifierfs"
	"github.com/PivotLLM/ClawEh/internal/mergepatch"
)

// Service owns every classifier of the scopes the host reports: the
// definitions on disk, the state databases, the scheduler and the
// tool-facing operations. Every operation checks that the definition's owner
// is the scope's agent.
type Service struct {
	host Host

	mu      sync.Mutex
	states  map[string]*stateDB      // by base directory
	locks   map[string]chan struct{} // one run at a time per classifier, by entry key
	entries map[string]*entry        // the scheduler's table, by entry key
	closing bool                     // Close has begun: no run or rerun starts
	closed  bool                     // the state databases are closed
	// stateMuted is, by entry key, until when a classifier whose state.db
	// cannot be opened is not reported again (stateNotice).
	stateMuted map[string]time.Time

	fileMu sync.Mutex // serialises definition writes

	kick    chan struct{}
	started bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	reloadRequested bool
	lastScopes      time.Time
	scopes          []Scope
}

// New returns a service over host. Nothing runs until Start.
func New(host Host) *Service {
	return &Service{
		host:       host,
		states:     map[string]*stateDB{},
		locks:      map[string]chan struct{}{},
		entries:    map[string]*entry{},
		stateMuted: map[string]time.Time{},
		kick:       make(chan struct{}, 1),
	}
}

// Summary describes one classifier.
type Summary struct {
	ID      string     `json:"id"`
	Owner   string     `json:"owner,omitempty"`
	Enabled bool       `json:"enabled"`
	DryRun  bool       `json:"dry_run"`
	Trigger string     `json:"trigger,omitempty"`
	Invalid string     `json:"invalid,omitempty"` // why the file is not run
	LastRun *RunRecord `json:"last_run,omitempty"`
	// ErrorsReported is report.errors (nil for an invalid file);
	// ErrorsMutedUntil is set while error notices are muted.
	ErrorsReported   *bool     `json:"errors_reported,omitempty"`
	ErrorsMutedUntil time.Time `json:"errors_muted_until,omitzero"`
}

// Status is a classifier's Summary with its processed items by status, its
// most recent decisions, its last run's error, its failed runs in a row
// as error notices count them, and how often each rule was cited.
type Status struct {
	Summary
	Counts     map[string]int `json:"counts"`
	Recent     []Decision     `json:"recent,omitempty"`
	LastError  string         `json:"last_error,omitempty"`
	FailedRuns int            `json:"failed_runs"`
	// RuleUsage is how often each rule was cited; nil for an invalid file
	// or Markdown that cannot be read.
	RuleUsage *RuleUsage `json:"rule_usage,omitempty"`
}

// recentDecisions is the number of decisions Status returns.
const recentDecisions = 10

// entryKey identifies a classifier by its folder and id.
func entryKey(base, id string) string { return filepath.Clean(base) + "\x00" + NormalizeID(id) }

// definitionPath is the file of classifier id under base.
func definitionPath(base, id string) string {
	return filepath.Join(base, NormalizeID(id)+classifierfs.DefinitionExt)
}

// state returns base's state database, opening it on first use. An error
// opening it matches ErrStateUnavailable; a closed service is ErrClosed.
func (s *Service) state(ctx context.Context, base string) (*stateDB, error) {
	base = filepath.Clean(base)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	if st, ok := s.states[base]; ok {
		return st, nil
	}
	dir, file := s.host.modes(base)
	st, err := openState(ctx, base, dir, file)
	if err != nil {
		if ctx.Err() == nil {
			recordStateProblem(base, err, s.host.clock().Now())
		}
		return nil, fmt.Errorf("%w: %w", ErrStateUnavailable, err)
	}
	s.states[base] = st
	delete(s.stateMuted, base) // a streak of state failures ends
	recordStateProblem(base, nil, time.Time{})
	return st, nil
}

// stateProblems is, by classifiers folder, the last error opening its
// state.db that a service of this process met, until it opens. Check Up
// reads it (LastStateError), since a failure it cannot reproduce from the
// files alone (a migration, a lock) must still show.
var stateProblems = struct {
	sync.Mutex
	m map[string]StateProblem
}{m: map[string]StateProblem{}}

// StateProblem is the last error opening a folder's state.db and when it
// happened.
type StateProblem struct {
	Error string
	At    time.Time
}

// recordStateProblem keeps err as base's state problem; nil clears it.
func recordStateProblem(base string, err error, at time.Time) {
	stateProblems.Lock()
	defer stateProblems.Unlock()
	if err == nil {
		delete(stateProblems.m, filepath.Clean(base))
		return
	}
	stateProblems.m[filepath.Clean(base)] = StateProblem{Error: err.Error(), At: at.UTC()}
}

// LastStateError is the last error this process met opening base's
// state.db, while it has not opened since; ok is false when there is none.
// Every run that fails to open it records it again, so a problem older
// than ErrorMute at now is one nothing has met since (the classifiers were
// deleted or turned off): it is dropped rather than shown stale.
func LastStateError(base string, now time.Time) (p StateProblem, ok bool) {
	stateProblems.Lock()
	defer stateProblems.Unlock()
	base = filepath.Clean(base)
	p, ok = stateProblems.m[base]
	if ok && now.Sub(p.At) > ErrorMute {
		delete(stateProblems.m, base)
		return StateProblem{}, false
	}
	return p, ok
}

// lockFor returns the run lock of a classifier.
func (s *Service) lockFor(key string) chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.locks[key]
	if !ok {
		l = make(chan struct{}, 1)
		s.locks[key] = l
	}
	return l
}

// fileInfo is a definition file as read from disk.
type fileInfo struct {
	def     *Definition // nil when the file is not valid
	raw     []byte
	owner   string // read leniently; "" when unreadable
	invalid string // why def is nil or the file is not run
}

// readFile reads and checks the definition file at path against the
// maximums c: valid JSON, every key known, the static rules, and an id
// matching the file name.
func readFile(path string, c Ceilings) fileInfo {
	raw, err := os.ReadFile(path) // #nosec G304 -- a definition file in a scope's base directory
	if err != nil {
		return fileInfo{invalid: "the file could not be read"}
	}
	fi := fileInfo{raw: raw}
	var lenient struct {
		Owner string `json:"owner"`
	}
	if json.Unmarshal(raw, &lenient) == nil {
		fi.owner = lenient.Owner
	}
	def, err := decodeDefinition(raw)
	if err != nil {
		fi.invalid = err.Error()
		return fi
	}
	if err := ValidateStatic(def, c); err != nil {
		fi.invalid = strings.ReplaceAll(err.Error(), "\n", " ")
		return fi
	}
	stem := strings.TrimSuffix(filepath.Base(path), classifierfs.DefinitionExt)
	if NormalizeID(def.ID) != stem {
		fi.invalid = fmt.Sprintf("the file name %s does not match id %q", filepath.Base(path), def.ID)
		return fi
	}
	fi.def = def
	return fi
}

// definitionFiles lists the definition files in base, by name.
func definitionFiles(base string) ([]string, error) {
	entries, err := os.ReadDir(base)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || strings.HasPrefix(name, classifierfs.TempPrefix) || !strings.HasSuffix(name, classifierfs.DefinitionExt) {
			continue
		}
		out = append(out, filepath.Join(base, name))
	}
	sort.Strings(out)
	return out, nil
}

// owned reads classifier id of scope: ErrNotFound unless the file exists and
// its owner is the scope's agent.
func (s *Service) owned(scope Scope, id string) (fileInfo, error) {
	if !ValidID(id) {
		return fileInfo{}, ErrNotFound
	}
	fi := readFile(definitionPath(scope.BaseDirectory, id), s.host.ceilings())
	if fi.raw == nil || !sameAgent(fi.owner, scope.AgentID) {
		return fileInfo{}, ErrNotFound
	}
	return fi, nil
}

// summaryOf is the Summary of a file.
func summaryOf(path string, fi fileInfo) Summary {
	sum := Summary{ID: strings.TrimSuffix(filepath.Base(path), classifierfs.DefinitionExt), Owner: fi.owner, Invalid: fi.invalid}
	if fi.def != nil {
		sum.ID, sum.Enabled, sum.DryRun = fi.def.ID, fi.def.Enabled, fi.def.DryRun
		sum.Trigger = fmt.Sprintf("every %d seconds", fi.def.Trigger.EverySeconds)
		reported := fi.def.reportErrors()
		sum.ErrorsReported = &reported
	}
	return sum
}

// failuresOf reads a classifier's failureState from db and sets the
// summary's ErrorsMutedUntil when the mute has not expired at now. A read
// failure is reported as no failures.
func failuresOf(ctx context.Context, db *sql.DB, sum *Summary, now time.Time) failureState {
	f, err := runFailuresFrom(ctx, db, NormalizeID(sum.ID))
	if err != nil {
		return failureState{}
	}
	if now.Before(f.mutedUntil) {
		sum.ErrorsMutedUntil = f.mutedUntil
	}
	return f
}

// List returns the scope agent's classifiers, invalid ones included (their
// Invalid says why).
func (s *Service) List(scope Scope) ([]Summary, error) {
	files, err := definitionFiles(scope.BaseDirectory)
	if err != nil {
		return nil, err
	}
	out := []Summary{}
	ctx := context.Background()
	for _, path := range files {
		fi := readFile(path, s.host.ceilings())
		if !sameAgent(fi.owner, scope.AgentID) {
			continue
		}
		sum := summaryOf(path, fi)
		if st, stErr := s.state(ctx, scope.BaseDirectory); stErr == nil {
			sum.LastRun = s.lastRun(ctx, st.db, sum.ID)
			failuresOf(ctx, st.db, &sum, s.host.clock().Now())
		}
		out = append(out, sum)
	}
	return out, nil
}

// Get returns a classifier's definition file as stored.
func (s *Service) Get(scope Scope, id string) (json.RawMessage, error) {
	fi, err := s.owned(scope, id)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(fi.raw), nil
}

// check is the validation of a definition for scope: the static rules under
// the current maximums, the agent's grants, and (live) one call of the
// source tool that must answer with a list at source.items whose elements
// all have an id.
func (s *Service) check(ctx context.Context, scope Scope, def *Definition, live bool) error {
	if err := ValidateStatic(def, s.host.ceilings()); err != nil {
		return err
	}
	if err := validateGrants(def, scope.AgentID, s.host.Tools); err != nil {
		return err
	}
	if !live {
		return nil
	}
	if err := s.checkInstructions(scope, def); err != nil {
		return err
	}
	if _, err := listItems(ctx, def, scope.AgentID, s.host.Tools); err != nil {
		return s.sourceRefusal(err)
	}
	return nil
}

// sourceRefusal is a tool's refusal of a source that failed (its tool's
// error, a result that is not JSON or holds no list), with the real reason
// so the agent can act on it: ClawEh's summary, then what the tool said
// inside Host.WrapUntrusted.
func (s *Service) sourceRefusal(err error) error {
	c := s.redactCause(causeOf(err))
	ve := &ValidationError{Issues: []string{withPeriod(c.summary)}}
	if c.detail != "" {
		ve.Issues = append(ve.Issues, s.host.wrapUntrusted(c.detail))
	}
	return ve
}

// ErrInterrupted: a test or rerun was cut short (its call or the service
// stopped) before the item was handled.
var ErrInterrupted = errors.New("the classifier stopped before the item was handled")

// ErrActionPending: a rerun was cut short during its action, after the
// classification was stored; the next run runs the action.
var ErrActionPending = errors.New("the classifier stopped before running the action")

// withPeriod is text ending with a period.
func withPeriod(text string) string {
	if strings.HasSuffix(text, ".") {
		return text
	}
	return text + "."
}

// Validate checks a definition without saving it; live also calls its
// source tool once.
func (s *Service) Validate(ctx context.Context, scope Scope, raw []byte, live bool) error {
	def, err := decodeDefinition(raw)
	if err != nil {
		return err
	}
	return s.check(ctx, scope, def, live)
}

// Create validates a new definition (static, grants and the live source
// call), makes the scope's agent its owner, and saves it. It returns the id.
// It is live at once unless dry_run is set (design 7.1f).
func (s *Service) Create(ctx context.Context, scope Scope, raw []byte) (string, error) {
	def, err := decodeDefinition(raw)
	if err != nil {
		return "", err
	}
	if err = s.check(ctx, scope, def, true); err != nil {
		return "", err
	}
	now := s.host.clock().Now().UTC().Truncate(time.Second)
	def.Owner, def.CreatedAt, def.UpdatedAt = NormalizeID(scope.AgentID), now, now
	data, err := marshalDefinition(def)
	if err != nil {
		return "", err
	}
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	dirMode, fileMode := s.host.modes(scope.BaseDirectory)
	if err := mkdirAllExact(scope.BaseDirectory, dirMode); err != nil {
		return "", err
	}
	if err := writeAtomic(definitionPath(scope.BaseDirectory, def.ID), data, fileMode, true); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return "", ErrExists
		}
		return "", err
	}
	s.host.audit(AuditEvent{
		Kind: AuditWrite, Agent: scope.AgentID, Classifier: def.ID, Outcome: "ok", Clone: scope.Clone,
		Details: map[string]any{"op": "create", "definition": json.RawMessage(data)},
	})
	s.clearFailures(ctx, scope, def.ID)
	s.host.Logger.Infof("classifier=%s agent=%s: created", def.ID, scope.AgentID)
	s.wake()
	return def.ID, nil
}

// Update applies an RFC 7386 merge patch to a classifier's definition,
// validates the result (static, grants and the live source call) and saves
// it. The id, owner and creation time cannot change. It does not reprocess
// items already handled (design 7.1d).
func (s *Service) Update(ctx context.Context, scope Scope, id string, patch []byte) error {
	if isObject, patchErr := mergepatch.IsObject(patch); patchErr != nil || !isObject {
		return &ValidationError{Issues: []string{"The changes must be a JSON object."}}
	}
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	fi, err := s.owned(scope, id)
	if err != nil {
		return err
	}
	merged, err := mergepatch.Apply(fi.raw, patch)
	if err != nil {
		return &ValidationError{Issues: []string{"The definition file is not a JSON object; fix it by hand or delete it."}}
	}
	def, err := decodeDefinition(merged)
	if err != nil {
		return err
	}
	if NormalizeID(def.ID) != NormalizeID(id) {
		return &ValidationError{Issues: []string{"id cannot be changed; create a new classifier instead."}}
	}
	if err = s.check(ctx, scope, def, true); err != nil {
		return err
	}
	var old struct {
		CreatedAt time.Time `json:"created_at"`
	}
	if json.Unmarshal(fi.raw, &old) != nil {
		old.CreatedAt = time.Time{} // unreadable: the update does not invent one
	}
	def.Owner, def.CreatedAt, def.UpdatedAt = NormalizeID(scope.AgentID), old.CreatedAt, s.host.clock().Now().UTC().Truncate(time.Second)
	data, err := marshalDefinition(def)
	if err != nil {
		return err
	}
	_, fileMode := s.host.modes(scope.BaseDirectory)
	if err := writeAtomic(definitionPath(scope.BaseDirectory, def.ID), data, fileMode, false); err != nil {
		return err
	}
	s.host.audit(AuditEvent{
		Kind: AuditWrite, Agent: scope.AgentID, Classifier: def.ID, Outcome: "ok", Clone: scope.Clone,
		Details: map[string]any{"op": "update", "patch": json.RawMessage(patch)},
	})
	s.clearFailures(ctx, scope, def.ID)
	s.host.Logger.Infof("classifier=%s agent=%s: updated", def.ID, scope.AgentID)
	s.wake()
	return nil
}

// clearFailures resets a classifier's failed-run count and lifts its error
// mute: the agent created or changed it, so its next failure is reported
// as a first notice. It does not wait for a run in progress; a failure
// that run records after it counts.
func (s *Service) clearFailures(ctx context.Context, scope Scope, id string) {
	st, err := s.state(ctx, scope.BaseDirectory)
	if err == nil {
		err = st.clearRunFailures(context.WithoutCancel(ctx), NormalizeID(id))
	}
	if err != nil {
		s.host.Logger.Errorf("classifier=%s agent=%s: reset run failures: %v", id, scope.AgentID, err)
	}
}

// Delete removes a classifier's definition and its state. A run in progress
// finishes the items in flight and starts no more; Delete waits for it (and
// for a rerun) to end before it removes anything, so nothing is written
// after the state is forgotten.
func (s *Service) Delete(ctx context.Context, scope Scope, id string) error {
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	if _, err := s.owned(scope, id); err != nil {
		return err
	}
	key := entryKey(scope.BaseDirectory, id)
	s.stopEntry(key)
	lock := s.lockFor(key)
	select {
	case lock <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-lock }()
	if err := os.Remove(definitionPath(scope.BaseDirectory, id)); err != nil {
		return err
	}
	if err := syncDir(scope.BaseDirectory); err != nil {
		return err
	}
	if st, err := s.state(ctx, scope.BaseDirectory); err == nil {
		if err := st.forget(ctx, NormalizeID(id)); err != nil {
			s.host.Logger.Errorf("classifier=%s agent=%s: forget state: %v", id, scope.AgentID, err)
		}
	}
	s.host.audit(AuditEvent{
		Kind: AuditWrite, Agent: scope.AgentID, Classifier: id, Outcome: "ok", Clone: scope.Clone,
		Details: map[string]any{"op": "delete"},
	})
	s.host.Logger.Infof("classifier=%s agent=%s: deleted", id, scope.AgentID)
	s.wake()
	return nil
}

// Status returns the status of classifier id, or of every classifier of the
// scope's agent when id is "".
func (s *Service) Status(scope Scope, id string) ([]Status, error) {
	ctx := context.Background()
	var sums []Summary
	if id == "" {
		all, err := s.List(scope)
		if err != nil {
			return nil, err
		}
		sums = all
	} else {
		fi, err := s.owned(scope, id)
		if err != nil {
			return nil, err
		}
		sums = []Summary{summaryOf(definitionPath(scope.BaseDirectory, id), fi)}
	}
	out := make([]Status, 0, len(sums))
	if len(sums) == 0 {
		return out, nil // nothing to read: the state is not created for it
	}
	st, err := s.state(ctx, scope.BaseDirectory)
	if err != nil {
		return nil, err
	}
	for _, sum := range sums {
		cls := NormalizeID(sum.ID)
		status := Status{Summary: sum}
		if status.LastRun == nil {
			status.LastRun = s.lastRun(ctx, st.db, sum.ID)
		}
		if status.LastRun != nil {
			status.LastError = status.LastRun.Error
		}
		if status.Counts, err = st.counts(ctx, cls); err != nil {
			return nil, err
		}
		status.FailedRuns = failuresOf(ctx, st.db, &status.Summary, s.host.clock().Now()).failures
		recent, err := st.activity(ctx, cls, ActivityQuery{Limit: recentDecisions, itemsOnly: true})
		if err != nil {
			return nil, err
		}
		for _, e := range recent {
			status.Recent = append(status.Recent, e.Decision)
		}
		if fi, err := s.owned(scope, sum.ID); err == nil && fi.def != nil {
			if status.RuleUsage, err = s.ruleUsage(ctx, scope, fi.def, st, s.host.clock().Now().UTC()); err != nil {
				return nil, err
			}
		}
		out = append(out, status)
	}
	return out, nil
}

// Activity reads a classifier's activity log (design 7.2). An Outcome that
// is not one of ActivityOutcomes, or a Rule that names no rule loaded now
// or cited in the log, is refused.
func (s *Service) Activity(scope Scope, id string, q ActivityQuery) ([]ActivityEntry, error) {
	fi, err := s.owned(scope, id)
	if err != nil {
		return nil, err
	}
	if q.Outcome != "" && !slices.Contains(ActivityOutcomes, q.Outcome) {
		return nil, &ValidationError{Issues: []string{"outcome must be " + strings.Join(ActivityOutcomes, ", ") + "."}}
	}
	ctx := context.Background()
	st, err := s.state(ctx, scope.BaseDirectory)
	if err != nil {
		return nil, err
	}
	if q.Rule != "" {
		if q.rule, err = s.resolveRuleFilter(ctx, scope, fi.def, st, NormalizeID(id), q.Rule); err != nil {
			return nil, err
		}
	}
	entries, err := st.activity(ctx, NormalizeID(id), q)
	if entries == nil && err == nil {
		entries = []ActivityEntry{}
	}
	return entries, err
}

// validDefinition is classifier id of scope, refused when its file is not
// valid.
func (s *Service) validDefinition(scope Scope, id string) (*Definition, error) {
	fi, err := s.owned(scope, id)
	if err != nil {
		return nil, err
	}
	if fi.def == nil {
		return nil, &ValidationError{Issues: []string{"Classifier " + id + " is not valid: " + fi.invalid}}
	}
	return fi.def, nil
}

// pickItem lists the source and returns the item with itemID, or the first
// item in source order when itemID is "".
func (s *Service) pickItem(ctx context.Context, scope Scope, def *Definition, itemID string) (listedItem, error) {
	if err := validateGrants(def, scope.AgentID, s.host.Tools); err != nil {
		return listedItem{}, err
	}
	items, err := listItems(ctx, def, scope.AgentID, s.host.Tools)
	if err != nil {
		return listedItem{}, s.sourceRefusal(err)
	}
	if len(items) == 0 {
		return listedItem{}, ErrNoItems
	}
	if itemID == "" {
		return items[0], nil
	}
	for _, it := range items {
		if it.id == itemID {
			return it, nil
		}
	}
	return listedItem{}, ErrItemNotFound
}

// TestResult is classifier_test's answer: the Decision and the system
// prompt the model was given, with the Markdown instructions loaded, so the
// agent can check its rules. With rules excluded (TestExcluding), also the
// labels left out, the decision without them and whether its category
// (or unclassified) changed; Changed is nil when either attempt failed.
type TestResult struct {
	Decision
	SystemPrompt string    `json:"system_prompt"`
	ExcludeRules []string  `json:"exclude_rules,omitempty"`
	WithoutRules *Decision `json:"without_rules,omitempty"`
	Changed      *bool     `json:"changed,omitempty"`
}

// instructionsRefusal is a tool's refusal of Markdown instructions that
// could not be loaded: loadInstructions' sentence.
func instructionsRefusal(err error) error {
	return &ValidationError{Issues: []string{withPeriod(err.Error())}}
}

// Test classifies one item (itemID, or the first the source lists) with the
// current definition: the category and reason, the action that would run
// and the system prompt, nothing stored and no action run (design 9.1f).
func (s *Service) Test(ctx context.Context, scope Scope, id, itemID string) (TestResult, error) {
	return s.TestExcluding(ctx, scope, id, itemID, nil)
}

// TestExcluding is Test that, when exclude names rules (labels in any
// accepted form, rules.go), classifies the item a second time with those
// rules left out of the prompt and reports both decisions. An unknown
// label is refused before anything is called. Nothing is stored.
func (s *Service) TestExcluding(ctx context.Context, scope Scope, id, itemID string, exclude []string) (TestResult, error) {
	def, err := s.validDefinition(scope, id)
	if err != nil {
		return TestResult{}, err
	}
	in, err := s.loadInstructions(scope, def)
	if err != nil {
		return TestResult{}, instructionsRefusal(err)
	}
	var without instructions
	if len(exclude) > 0 {
		without = in
		if without.exclude, err = resolveExclusions(def, in, exclude); err != nil {
			return TestResult{}, err
		}
	}
	item, err := s.pickItem(ctx, scope, def, itemID)
	if err != nil {
		if ctx.Err() != nil {
			return TestResult{}, ErrInterrupted
		}
		return TestResult{}, err
	}
	d := s.handleItem(ctx, scope, def, in, nil, item, nil, modeTest)
	if d.Outcome == OutcomeFailed && ctx.Err() != nil {
		return TestResult{}, ErrInterrupted
	}
	system := d.system
	if system == "" { // no model call: the fetch failed
		system, _ = buildPrompt(def, in, itemData{}, "", newDelimiter())
	}
	res := TestResult{Decision: d.Decision, SystemPrompt: system}
	if without.exclude == nil || !answered(d) {
		return res, nil // nothing excluded, or no answer to compare with
	}
	for l := range without.exclude {
		res.ExcludeRules = append(res.ExcludeRules, l)
	}
	slices.Sort(res.ExcludeRules)
	d2 := s.testAgain(ctx, scope, def, without, item, d)
	if d2.Outcome == OutcomeFailed && ctx.Err() != nil {
		return TestResult{}, ErrInterrupted
	}
	res.WithoutRules = &d2.Decision
	if answered(d2) {
		changed := d.Category != d2.Category
		res.Changed = &changed
	}
	return res, nil
}

// answered reports whether the model answered about the decision's item:
// a category, or unclassified.
func answered(d decision) bool { return d.Category != "" || d.Outcome == OutcomeUnclassified }

// testAgain classifies the item of a test decision (first) again with in,
// reusing the fetched data, as classifier_test would show it.
func (s *Service) testAgain(ctx context.Context, scope Scope, def *Definition, in instructions, item listedItem, first decision) decision {
	d := decision{data: first.data}
	d.ItemID, d.MissingFields, d.ShortenedFields = first.ItemID, first.MissingFields, first.ShortenedFields
	d = s.classify(withClassifier(ctx, NormalizeID(def.ID)), scope, def, in, d)
	d.Error, d.cause = s.host.redact(d.Error), s.redactCause(d.cause)
	d.At = s.host.clock().Now().UTC()
	if d.Category != "" {
		d.Outcome = OutcomeRecorded
		previewAction(def, item, &d)
	}
	return d
}

// Rerun classifies one item again with the current definition, replaces
// its stored outcome and runs its action (design 9.1g); a dry-run
// classifier records the decision as a dry run instead. It waits for a run
// of the classifier in progress to end.
func (s *Service) Rerun(ctx context.Context, scope Scope, id, itemID string) (Decision, error) {
	def, err := s.validDefinition(scope, id)
	if err != nil {
		return Decision{}, err
	}
	if !s.beginWork() {
		return Decision{}, ErrClosed
	}
	defer s.wg.Done()
	lock := s.lockFor(entryKey(scope.BaseDirectory, id))
	select {
	case lock <- struct{}{}:
	case <-ctx.Done():
		return Decision{}, ErrInterrupted
	}
	defer func() { <-lock }()
	in, err := s.loadInstructions(scope, def)
	if err != nil {
		return Decision{}, instructionsRefusal(err)
	}
	st, err := s.state(ctx, scope.BaseDirectory)
	if err != nil {
		return Decision{}, err
	}
	item, err := s.pickItem(ctx, scope, def, itemID)
	if err != nil {
		if ctx.Err() != nil {
			return Decision{}, ErrInterrupted
		}
		return Decision{}, err
	}
	d := s.handleItem(ctx, scope, def, in, st, item, nil, modeRerun)
	switch {
	case d.actionPending:
		return Decision{}, ErrActionPending // the classification is stored
	case d.interrupted:
		return Decision{}, ErrInterrupted // nothing was recorded
	}
	return d.Decision, nil
}

// Inspect lists the classifiers in base for the configuration report,
// without a service: every definition file, invalid ones (checked against
// the install's maximums c, as a run checks them) and ones owned by another
// agent included, with the last run and the error mute read from state.db
// (opened read only; none when it does not exist).
func Inspect(ctx context.Context, base string, c Ceilings) ([]Summary, error) {
	files, err := definitionFiles(base)
	if err != nil {
		return nil, err
	}
	var db *sql.DB
	statePath := filepath.Join(base, classifierfs.StateFile)
	if _, err := os.Stat(statePath); err == nil {
		if db, err = sql.Open("sqlite", stateDSN(statePath, true)); err == nil {
			defer closeQuietly(db)
		}
	}
	out := []Summary{}
	now := time.Now()
	for _, path := range files {
		sum := summaryOf(path, readFile(path, c))
		if db != nil {
			if r, runErr := lastRunFrom(ctx, db, NormalizeID(sum.ID)); runErr == nil {
				sum.LastRun = r
			}
			failuresOf(ctx, db, &sum, now)
		}
		out = append(out, sum)
	}
	return out, nil
}

// CheckState reports, for the configuration report, whether base's
// state.db can be opened as a run opens it: when it does not exist yet, the
// first run creates it, so the folder (or the nearest one that exists) must
// be writable; when it exists, it must be a regular file that can be opened
// for writing and read.
func CheckState(ctx context.Context, base string) error {
	path := filepath.Join(base, classifierfs.StateFile)
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return checkCreatable(base)
	case err != nil:
		return err
	case !info.Mode().IsRegular():
		return fmt.Errorf("%s is not a file", path)
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0) // #nosec G304 -- <base>/state.db
	if err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", stateDSN(path, true))
	if err != nil {
		return err
	}
	defer closeQuietly(db)
	var n int
	return db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master`).Scan(&n)
}

// checkCreatable reports whether state.db can be created in base: a file
// is created and removed in base, or in the nearest folder above it that
// exists when base does not (the run creates the folders).
func checkCreatable(base string) error {
	dir := filepath.Clean(base)
	for {
		info, err := os.Stat(dir)
		if err == nil {
			if !info.IsDir() {
				return fmt.Errorf("%s is not a folder", dir)
			}
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return err
		}
		dir = parent
	}
	f, err := os.CreateTemp(dir, classifierfs.TempPrefix+"check-*")
	if err != nil {
		return fmt.Errorf("%s cannot be created: %w", filepath.Join(base, classifierfs.StateFile), err)
	}
	name := f.Name()
	return errors.Join(f.Close(), os.Remove(name))
}

// lastRun reads a classifier's last run; a read failure is logged and
// reported as no run.
func (s *Service) lastRun(ctx context.Context, db *sql.DB, id string) *RunRecord {
	r, err := lastRunFrom(ctx, db, NormalizeID(id))
	if err != nil {
		s.host.Logger.Warnf("classifier=%s: read last run: %v", id, err)
		return nil
	}
	return r
}

// closeQuietly closes c, ignoring the error of a read-only handle.
func closeQuietly(c io.Closer) {
	if err := c.Close(); err != nil {
		return
	}
}

// beginWork counts one more run or rerun for Close to wait for; false once
// Close has begun.
func (s *Service) beginWork() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return false
	}
	s.wg.Add(1)
	return true
}

// Close stops the scheduler and every run (their context is cancelled), lets
// no run or rerun start, waits for those in progress, and then closes the
// state databases. When ctx ends first, the databases are left open (a run
// may still use them) and ctx's error is returned.
func (s *Service) Close(ctx context.Context) error {
	s.mu.Lock()
	s.closing = true
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	var err error
	for base, st := range s.states {
		err = errors.Join(err, st.close())
		delete(s.states, base)
	}
	return err
}
