// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package classifier

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
)

// ids returns "<prefix>1" .. "<prefix>n".
func ids(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s%d", prefix, i+1)
	}
	return out
}

func messagesJSON(list []string, extra map[string]any) (string, error) {
	msgs := make([]map[string]any, len(list))
	for i, id := range list {
		msgs[i] = map[string]any{"id": id, "subject": "About " + id}
	}
	out := map[string]any{"messages": msgs}
	maps.Copy(out, extra)
	b, err := json.Marshal(out)
	return string(b), err
}

// setOffsetInbox makes mail_list return the messages of inbox() size at a
// time, from its "skip" argument on.
func (e *env) setOffsetInbox(inbox func() []string, size int) {
	e.tools.set("mail_list", func(args map[string]any) (string, error) {
		all := inbox()
		from := min(skipOf(args), len(all))
		return messagesJSON(all[from:min(from+size, len(all))], nil)
	})
}

// offsetDefinition is the inbox classifier over a source paged by "skip".
// skipOf is a call's "skip" argument, 0 when it has none.
func skipOf(args map[string]any) int {
	if skip, ok := args["skip"].(float64); ok {
		return int(skip)
	}
	return 0
}

func offsetDefinition() map[string]any {
	def := definition()
	obj(def["source"])["paging"] = map[string]any{"mode": "offset", "offset_arg": "skip"}
	return def
}

// skips are the "skip" arguments of the mail_list calls from call from on.
func (e *env) skips(from int) []float64 {
	calls := e.tools.callsOf("mail_list")[from:]
	out := make([]float64, 0, len(calls))
	for _, c := range calls {
		out = append(out, float64(skipOf(c.args)))
	}
	return out
}

func (e *env) listCalls() int { return len(e.tools.callsOf("mail_list")) }

// A paged source is listed to its end: every page, then the empty one.
func TestOffsetPagingListsEveryPage(t *testing.T) {
	e := newEnv(t)
	all := ids("n", 250)
	e.setOffsetInbox(func() []string { return all }, 100)
	e.create(offsetDefinition())
	before := e.listCalls()
	if before != 1 {
		t.Fatalf("create called the source %d times, want 1 (its first page)", before)
	}
	rec := e.run("inbox-priority")
	if rec.Status != RunOK || rec.Listed != 250 || rec.Acted != 250 || rec.Pages != 4 || rec.Listing != ListComplete || rec.MoreItems {
		t.Fatalf("run = %+v", rec)
	}
	if got := e.skips(before); !slices.Equal(got, []float64{0, 100, 200, 250}) {
		t.Errorf("skip arguments = %v", got)
	}
	// Everything is handled: the next run lists it all and takes nothing.
	rec = e.run("inbox-priority")
	if rec.Status != RunOK || rec.Listed != 250 || rec.NewItems != 0 {
		t.Errorf("second run = %+v", rec)
	}
}

// The stall that paging exists for: a first page full of items that stay
// in the source hides a new item behind it. Without paging the item is never
// seen; with it, it is handled.
func TestPagingSeesPastAFullPageOfHandledItems(t *testing.T) {
	for _, paged := range []bool{false, true} {
		t.Run(fmt.Sprintf("paged=%t", paged), func(t *testing.T) {
			e := newEnv(t)
			inbox := ids("kept", 100)
			e.setOffsetInbox(func() []string { return inbox }, 100)
			def := definition()
			if paged {
				def = offsetDefinition()
			}
			e.create(def)
			if rec := e.run("inbox-priority"); rec.Acted != 100 {
				t.Fatalf("first run = %+v", rec)
			}
			inbox = append(inbox, "late")
			rec := e.run("inbox-priority")
			_, moved := e.moves()["late"]
			if moved != paged {
				t.Errorf("late item handled = %t, want %t (run %+v)", moved, paged, rec)
			}
			if !paged && (rec.Listing != "" || rec.Pages != 1) {
				t.Errorf("a source without paging reported listing %q, %d pages", rec.Listing, rec.Pages)
			}
		})
	}
}

// A run stops listing once it found items_per_run items to handle, and says
// that more are left.
func TestPagingStopsAtItemsPerRun(t *testing.T) {
	e := newEnv(t)
	all := ids("n", 250)
	e.setOffsetInbox(func() []string { return all }, 100)
	def := offsetDefinition()
	def["limits"] = map[string]any{"items_per_run": 150}
	def["report"] = map[string]any{"summary": "always"}
	e.create(def)

	calls := e.listCalls()
	rec := e.run("inbox-priority")
	if rec.NewItems != 150 || rec.Pages != 2 || rec.Listing != ListEnough || !rec.MoreItems || rec.Status != RunOK {
		t.Fatalf("first run = %+v", rec)
	}
	if got := e.skips(calls); !slices.Equal(got, []float64{0, 100}) {
		t.Errorf("first run's skips = %v", got)
	}
	if texts := e.notifier.all(); len(texts) != 1 || !strings.Contains(texts[0], "more items are left for the next run") {
		t.Errorf("summary = %q", texts)
	}
	// The second run lists to the end and takes the remaining 100.
	rec = e.run("inbox-priority")
	if rec.NewItems != 100 || rec.Listing != ListComplete || rec.MoreItems {
		t.Fatalf("second run = %+v", rec)
	}
	if len(e.moves()) != 250 {
		t.Errorf("%d items moved, want 250", len(e.moves()))
	}
}

// A listing that reaches the page maximum with pages left fails the run,
// after handling what it listed; a source that ends exactly there does not.
func TestPagingMaxPages(t *testing.T) {
	e := newEnv(t)
	all := ids("n", 200)
	e.setOffsetInbox(func() []string { return all }, 100)
	def := offsetDefinition()
	obj(obj(def["source"])["paging"])["max_pages"] = 2
	e.create(def)

	rec := e.run("inbox-priority")
	if rec.Status != RunOK || rec.Listed != 200 || rec.Pages != 3 || rec.Listing != ListComplete {
		t.Fatalf("a source ending at the maximum: run = %+v", rec)
	}

	all = append(all, "beyond")
	rec = e.run("inbox-priority")
	if rec.Status != RunError || rec.Listing != ListMaxPages || rec.Pages != 2 || rec.Listed != 200 {
		t.Fatalf("a source past the maximum: run = %+v", rec)
	}
	for _, want := range []string{"source mail_list lists more items than the 2 pages one run reads", "source.paging.max_pages"} {
		if !strings.Contains(rec.Error, want) {
			t.Errorf("error %q lacks %q", rec.Error, want)
		}
	}
	if _, moved := e.moves()["beyond"]; moved {
		t.Error("an item past the page maximum was handled")
	}
	if n := e.notifier.allNotices(); len(n) != 1 || !strings.Contains(n[0].text, "the run failed (source mail_list lists more items") {
		t.Errorf("notices = %+v", n)
	}

	// At the install's maximum the error does not suggest raising it.
	e.mu.Lock()
	e.ceilings.MaxSourcePages = 2
	e.mu.Unlock()
	if rec = e.run("inbox-priority"); strings.Contains(rec.Error, "raise") {
		t.Errorf("error at the install's maximum suggests raising it: %q", rec.Error)
	}
}

// The install's page maximum applies when the definition names none, and
// the built-in one when the install names none either.
func TestPagingDefaultMaxPages(t *testing.T) {
	d := &Definition{Source: Source{Paging: &Paging{Mode: PagingOffset}}}
	if got := d.maxPages(Ceilings{}); got != DefaultMaxSourcePages {
		t.Errorf("no maximum anywhere = %d", got)
	}
	if got := d.maxPages(Ceilings{MaxSourcePages: 7}); got != 7 {
		t.Errorf("install's maximum = %d", got)
	}
	d.Source.Paging.MaxPages = 3
	if got := d.maxPages(Ceilings{MaxSourcePages: 7}); got != 3 {
		t.Errorf("definition's maximum = %d", got)
	}
}

// A tool that ignores the paging argument returns the same page for ever:
// the run fails on the second page, naming the argument.
func TestPagingArgumentIgnored(t *testing.T) {
	e := newEnv(t)
	e.setInbox(ids("n", 5)) // returns everything, whatever "skip" says
	e.create(offsetDefinition())
	rec := e.run("inbox-priority")
	want := `source mail_list returned only items already listed on page 2; it does not seem to use source.paging.offset_arg "skip"`
	if rec.Status != RunError || rec.Error != want {
		t.Fatalf("run = %+v", rec)
	}
	if len(e.moves()) != 0 {
		t.Error("items were handled although the listing failed")
	}
}

// The listing starts at the offset source.args gives.
func TestOffsetPagingStartsAtArgument(t *testing.T) {
	e := newEnv(t)
	all := ids("n", 30)
	e.setOffsetInbox(func() []string { return all }, 10)
	def := offsetDefinition()
	obj(obj(def["source"])["args"])["skip"] = 20
	e.create(def)
	calls := e.listCalls()
	rec := e.run("inbox-priority")
	if rec.Listed != 10 || !slices.Equal(e.skips(calls), []float64{20, 30}) {
		t.Errorf("run = %+v, skips %v", rec, e.skips(calls))
	}
}

// cursorDefinition is the inbox classifier over a source paged by a cursor
// in "next", passed back as "page_token".
func cursorDefinition() map[string]any {
	def := definition()
	obj(def["source"])["paging"] = map[string]any{"mode": "cursor", "cursor_arg": "page_token", "next": "paging.next"}
	return def
}

// setCursorInbox serves pages by cursor: the result of the call with cursor
// c (nil for the first) is pages[c].
func (e *env) setCursorInbox(pages map[any]string) {
	e.tools.set("mail_list", func(args map[string]any) (string, error) {
		page, ok := pages[args["page_token"]]
		if !ok {
			return "", fmt.Errorf("unknown cursor %v", args["page_token"])
		}
		return page, nil
	})
}

func cursorPage(t *testing.T, list []string, next any) string {
	t.Helper()
	extra := map[string]any{}
	if next != nil {
		extra["paging"] = map[string]any{"next": next}
	}
	s, err := messagesJSON(list, extra)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A cursor source is followed to the result that names no next page; a
// cursor may be a text or a number, and a page may be empty.
func TestCursorPaging(t *testing.T) {
	e := newEnv(t)
	e.setCursorInbox(map[any]string{
		nil:        cursorPage(t, []string{"a1", "a2"}, "tok-2"),
		"tok-2":    cursorPage(t, nil, 3),
		float64(3): cursorPage(t, []string{"a3"}, ""),
	})
	e.create(cursorDefinition())
	rec := e.run("inbox-priority")
	if rec.Status != RunOK || rec.Listed != 3 || rec.Acted != 3 || rec.Pages != 3 || rec.Listing != ListComplete {
		t.Fatalf("run = %+v", rec)
	}
	first := e.tools.callsOf("mail_list")[0]
	if _, sent := first.args["page_token"]; sent {
		t.Error("the first page was asked for with a cursor")
	}
}

// A cursor given twice would loop: the run fails. The page maximum is exact
// for a cursor source, which says whether more follows.
func TestCursorPagingGuards(t *testing.T) {
	e := newEnv(t)
	e.setCursorInbox(map[any]string{nil: cursorPage(t, []string{"a1"}, "again")})
	e.create(cursorDefinition())
	e.setCursorInbox(map[any]string{
		nil:     cursorPage(t, []string{"a1"}, "again"),
		"again": cursorPage(t, []string{"a2"}, "again"),
	})
	rec := e.run("inbox-priority")
	if rec.Status != RunError || !strings.Contains(rec.Error, `does not seem to use source.paging.cursor_arg "page_token"`) {
		t.Fatalf("repeated cursor: run = %+v", rec)
	}

	e.setCursorInbox(map[any]string{
		nil:  cursorPage(t, []string{"a1"}, "p2"),
		"p2": cursorPage(t, []string{"a2"}, "p3"),
		"p3": cursorPage(t, []string{"a3"}, nil),
	})
	if err := e.svc.Update(context.Background(), e.scope, "inbox-priority", []byte(`{"source":{"paging":{"max_pages":2}}}`)); err != nil {
		t.Fatal(err)
	}
	rec = e.run("inbox-priority")
	if rec.Status != RunError || rec.Listing != ListMaxPages || rec.Pages != 2 || rec.Acted != 2 {
		t.Fatalf("page maximum: run = %+v", rec)
	}
}

// classifier_test and classifier_rerun find an item on a later page, and
// take the first item from the first page only.
func TestPickItemAcrossPages(t *testing.T) {
	e := newEnv(t)
	all := ids("n", 250)
	e.setOffsetInbox(func() []string { return all }, 100)
	e.create(offsetDefinition())

	calls := e.listCalls()
	res, err := e.svc.Test(context.Background(), e.scope, "inbox-priority", "n230")
	if err != nil || res.ItemID != "n230" {
		t.Fatalf("Test(n230) = %+v, %v", res.Decision, err)
	}
	if got := e.skips(calls); !slices.Equal(got, []float64{0, 100, 200}) {
		t.Errorf("skips to find n230 = %v", got)
	}
	calls = e.listCalls()
	if res, err = e.svc.Test(context.Background(), e.scope, "inbox-priority", ""); err != nil || res.ItemID != "n1" {
		t.Fatalf("Test(first) = %+v, %v", res.Decision, err)
	}
	if e.listCalls()-calls != 1 {
		t.Errorf("the first item took %d source calls", e.listCalls()-calls)
	}
	if _, err = e.svc.Test(context.Background(), e.scope, "inbox-priority", "nowhere"); !errors.Is(err, ErrItemNotFound) {
		t.Errorf("Test(nowhere) = %v", err)
	}
}

func TestValidatePaging(t *testing.T) {
	cases := []struct {
		name   string
		paging map[string]any
		args   map[string]any
		want   string // "" = valid
	}{
		{"offset", map[string]any{"mode": "offset", "offset_arg": "skip"}, nil, ""},
		{"offset with start", map[string]any{"mode": "offset", "offset_arg": "skip"}, map[string]any{"skip": 40}, ""},
		{"cursor", map[string]any{"mode": "cursor", "cursor_arg": "token", "next": "meta.next"}, nil, ""},
		{"no mode", map[string]any{"offset_arg": "skip"}, nil, `source.paging.mode must be "offset" or "cursor".`},
		{"unknown mode", map[string]any{"mode": "link"}, nil, `source.paging.mode must be "offset" or "cursor".`},
		{"offset without argument", map[string]any{"mode": "offset"}, nil, "source.paging.offset_arg is required"},
		{"offset with cursor keys", map[string]any{"mode": "offset", "offset_arg": "skip", "next": "n"}, nil, `source.paging.next is not used with mode "offset"`},
		{"offset start not a number", map[string]any{"mode": "offset", "offset_arg": "skip"}, map[string]any{"skip": "0"}, "source.args.skip must be a whole number"},
		{"offset start negative", map[string]any{"mode": "offset", "offset_arg": "skip"}, map[string]any{"skip": -1}, "source.args.skip must be a whole number"},
		{"offset start fraction", map[string]any{"mode": "offset", "offset_arg": "skip"}, map[string]any{"skip": 1.5}, "source.args.skip must be a whole number"},
		{"cursor without argument", map[string]any{"mode": "cursor", "next": "n"}, nil, "source.paging.cursor_arg is required"},
		{"cursor without next", map[string]any{"mode": "cursor", "cursor_arg": "token"}, nil, "source.paging.next is required"},
		{"cursor next not a path", map[string]any{"mode": "cursor", "cursor_arg": "token", "next": "a..b"}, nil, `source.paging.next "a..b" is not a dot-path.`},
		{"cursor argument given", map[string]any{"mode": "cursor", "cursor_arg": "token", "next": "n"}, map[string]any{"token": "x"}, "source.args.token must be left out"},
		{"cursor with offset key", map[string]any{"mode": "cursor", "cursor_arg": "token", "next": "n", "offset_arg": "skip"}, nil, `source.paging.offset_arg is not used with mode "cursor"`},
		{"negative pages", map[string]any{"mode": "offset", "offset_arg": "skip", "max_pages": -1}, nil, "source.paging.max_pages must be 0 (the default) or more."},
		{"pages over the install's", map[string]any{"mode": "offset", "offset_arg": "skip", "max_pages": 51}, nil, "source.paging.max_pages is 51; this install allows 50."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			def := definition()
			src := obj(def["source"])
			src["paging"] = tc.paging
			maps.Copy(obj(src["args"]), tc.args)
			d, err := decodeDefinition(mustJSON(t, def))
			if err != nil {
				t.Fatal(err)
			}
			err = ValidateStatic(d, Ceilings{MaxSourcePages: 50})
			switch {
			case tc.want == "" && err != nil:
				t.Errorf("refused: %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Errorf("error = %v, want %q", err, tc.want)
			}
		})
	}
	d, err := decodeDefinition(mustJSON(t, map[string]any{"limits": map[string]any{"max_attempts": -1}}))
	if err != nil {
		t.Fatal(err)
	}
	if err = ValidateStatic(d, Ceilings{}); err == nil || !strings.Contains(err.Error(), "limits.max_attempts must be 0 (the default) or more.") {
		t.Errorf("negative max_attempts: %v", err)
	}
}
