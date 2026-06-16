// ClawEh - Cognitive Memory
// License: MIT

// Package cogmem composes per-turn prompt memory from a session's .cogmem.db.
// It produces two blocks (mem-proposal.md §10):
//
//   - StableBlock: always-on learned memory (the "general" domain), the
//     pending-confirmation digest, and the domain index. Cacheable; it carries
//     the store's stable_rev so the caller invalidates only on change. The
//     caller (the agent context layer) prepends the verbatim curated .md files.
//   - RoutedBlock: the full state + active hooks of the most-recently-active
//     domain(s), pre-loaded by recency. The only per-turn-varying memory.
package cogmem

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/PivotLLM/ClawEh/pkg/cogmem/store"
)

// options tune composition (mirrors memory.prompt config); set via functional
// options on New.
type options struct {
	topKDomains    int     // routed domains to pre-load
	maxChars       int     // routed block budget
	minConfidence  float64 // hide active hooks below this
	pendingMax     int     // pending digest cap
	pendingSurface string  // PendingSurfaceAsk | PendingSurfaceExportOnly
}

// Option configures a Composer (functional options pattern, per dev standards).
type Option func(*options)

// WithTopKDomains sets how many routed domains to pre-load per turn.
func WithTopKDomains(n int) Option {
	return func(o *options) {
		if n > 0 {
			o.topKDomains = n
		}
	}
}

// WithMaxChars caps the routed block size.
func WithMaxChars(n int) Option {
	return func(o *options) {
		if n > 0 {
			o.maxChars = n
		}
	}
}

// WithMinConfidence hides active hooks below the given confidence.
func WithMinConfidence(c float64) Option {
	return func(o *options) {
		if c > 0 {
			o.minConfidence = c
		}
	}
}

// WithPendingMax caps the pending-confirmation digest.
func WithPendingMax(n int) Option {
	return func(o *options) {
		if n > 0 {
			o.pendingMax = n
		}
	}
}

// WithPendingSurface selects PendingSurfaceAsk or PendingSurfaceExportOnly.
func WithPendingSurface(s string) Option {
	return func(o *options) {
		if s != "" {
			o.pendingSurface = s
		}
	}
}

// Composer reads a session's cogmem store.
type Composer struct {
	st  *store.Store
	opt options
}

// New returns a Composer over st with the given options applied over defaults.
func New(st *store.Store, opts ...Option) *Composer {
	o := options{
		topKDomains:    defaultTopKDomains,
		maxChars:       defaultMaxChars,
		minConfidence:  defaultMinConfidence,
		pendingMax:     defaultPendingMax,
		pendingSurface: PendingSurfaceAsk,
	}
	for _, fn := range opts {
		fn(&o)
	}
	return &Composer{st: st, opt: o}
}

// RouteRequest carries the per-turn routing inputs.
type RouteRequest struct {
	RouteText   string   // latest user message (reserved for future signals)
	RecentTools []string // recently-invoked tool names (newest-first) for tool-trigger activation
	Trace       bool
}

// DomainSelection is one trace entry explaining a routed pre-load.
type DomainSelection struct {
	ID     string
	Name   string
	Signal string // "tool:<token>" (tool-trigger activation) or "recency"
}

// RoutedResult is the per-turn block plus optional trace.
type RoutedResult struct {
	Text   string
	Loaded []string
	Trace  []DomainSelection
}

// StableBlock renders the always-on learned memory and returns it with the
// store's stable_rev for cache keying. Returns "" (and the rev) when there is no
// learned content yet.
func (c *Composer) StableBlock(ctx context.Context) (string, int64, error) {
	db := c.st.DB()
	rev, err := c.st.StableRev(ctx)
	if err != nil {
		return "", 0, err
	}
	var b strings.Builder

	// The always-on "general" domain: global rules, preferences, and standing facts.
	active, err := c.st.ListDomains(ctx, db, store.StatusActive)
	if err != nil {
		return "", rev, err
	}
	for _, d := range active {
		if !d.Type.AlwaysOn() {
			continue
		}
		hooks, err := c.st.ListHooks(ctx, db, d.ID, store.StatusActive)
		if err != nil {
			return "", rev, err
		}
		hooks = filterConfidence(hooks, c.opt.minConfidence)
		if len(hooks) == 0 {
			continue
		}
		b.WriteString("## General\n")
		for _, h := range hooks {
			fmt.Fprintf(&b, "- %s\n", h.Text)
		}
		b.WriteString("\n")
	}

	// Pending (unconfirmed) digest.
	if c.opt.pendingSurface != PendingSurfaceExportOnly {
		pend, err := c.st.ListPending(ctx, db, c.opt.pendingMax)
		if err != nil {
			return "", rev, err
		}
		if len(pend) > 0 {
			b.WriteString("## Pending (unconfirmed — ask to confirm, do not act on as rules)\n")
			for _, h := range pend {
				fmt.Fprintf(&b, "- (%s) %s\n", h.ID, h.Text)
			}
			b.WriteString("\n")
		}
	}

	// Domain index (routed, active topic domains), stable sort by id.
	var index []store.Domain
	for _, d := range active {
		if !d.Type.AlwaysOn() {
			index = append(index, d)
		}
	}
	if len(index) > 0 {
		b.WriteString("## Projects / Topics (index)\n")
		for _, d := range index {
			fmt.Fprintf(&b, "- %s · %s — %s\n", d.ID, d.Name, oneLine(d.Summary))
		}
		b.WriteString("\n")
	}

	out := strings.TrimRight(b.String(), "\n")
	if out != "" {
		out = "# Learned Memory\n\n" + out
	}
	return out, rev, nil
}

// RoutedBlock renders the most-recently-active topic domain(s), up to TopKDomains
// and within MaxChars.
func (c *Composer) RoutedBlock(ctx context.Context, req RouteRequest) (RoutedResult, error) {
	db := c.st.DB()
	active, err := c.st.ListDomains(ctx, db, store.StatusActive)
	if err != nil {
		return RoutedResult{}, err
	}
	var topics []store.Domain
	for _, d := range active {
		if !d.Type.AlwaysOn() {
			topics = append(topics, d)
		}
	}
	// Most-recently-active first; nil last_active sorts last.
	sort.SliceStable(topics, func(i, j int) bool {
		return lastActive(topics[i]) > lastActive(topics[j])
	})

	// Order the candidates: domains activated by a recently-used tool come first
	// (each tagged with the matching token), then the rest by recency. A single
	// pass with a seen-set guarantees every domain appears at most once, so a
	// domain that is both tool-triggered and recent is never duplicated.
	type candidate struct {
		d      store.Domain
		signal string
	}
	seen := make(map[string]bool, len(topics))
	var ordered []candidate
	for _, d := range topics {
		if tok, ok := matchAnyTool(d, req.RecentTools); ok {
			seen[d.ID] = true
			ordered = append(ordered, candidate{d: d, signal: "tool:" + tok})
		}
	}
	for _, d := range topics {
		if !seen[d.ID] {
			seen[d.ID] = true
			ordered = append(ordered, candidate{d: d, signal: "recency"})
		}
	}
	if len(ordered) > c.opt.topKDomains {
		ordered = ordered[:c.opt.topKDomains]
	}

	var b strings.Builder
	var res RoutedResult
	for _, cand := range ordered {
		d := cand.d
		hooks, err := c.st.ListHooks(ctx, db, d.ID, store.StatusActive)
		if err != nil {
			return RoutedResult{}, err
		}
		hooks = filterConfidence(hooks, c.opt.minConfidence)
		section := renderDomain(d, hooks)
		if b.Len()+len(section) > c.opt.maxChars && b.Len() > 0 {
			break
		}
		b.WriteString(section)
		res.Loaded = append(res.Loaded, d.ID)
		if req.Trace {
			res.Trace = append(res.Trace, DomainSelection{ID: d.ID, Name: d.Name, Signal: cand.signal})
		}
	}
	res.Text = strings.TrimRight(b.String(), "\n")
	return res, nil
}

// matchAnyTool reports the first trigger token of d that matches any recently
// used tool name, and whether one matched.
func matchAnyTool(d store.Domain, recentTools []string) (string, bool) {
	if d.Triggers == "" {
		return "", false
	}
	for _, name := range recentTools {
		if tok, ok := d.MatchTrigger(name); ok {
			return tok, true
		}
	}
	return "", false
}

func renderDomain(d store.Domain, hooks []store.Hook) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Active Context: %s · %s\n", d.ID, d.Name)
	if s := oneLine(d.Summary); s != "" {
		fmt.Fprintf(&b, "Summary: %s\n", s)
	}
	if len(d.State.Blockers) > 0 {
		b.WriteString("Blockers:\n")
		for _, x := range d.State.Blockers {
			fmt.Fprintf(&b, "- %s\n", x)
		}
	}
	if len(d.State.NextActions) > 0 {
		b.WriteString("Next actions:\n")
		for _, x := range d.State.NextActions {
			fmt.Fprintf(&b, "- %s\n", x)
		}
	}
	if len(d.State.Constraints) > 0 {
		b.WriteString("Constraints:\n")
		for _, x := range d.State.Constraints {
			fmt.Fprintf(&b, "- %s\n", x)
		}
	}
	for _, h := range hooks {
		fmt.Fprintf(&b, "- (%s) %s\n", h.ID, h.Text)
	}
	b.WriteString("\n")
	return b.String()
}

func filterConfidence(hooks []store.Hook, min float64) []store.Hook {
	out := hooks[:0:0]
	for _, h := range hooks {
		if h.Confidence >= min {
			out = append(out, h)
		}
	}
	return out
}

func lastActive(d store.Domain) int64 { return d.LastActiveAt }

func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return s
}
