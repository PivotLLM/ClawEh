// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package classifier

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/PivotLLM/ClawEh/internal/dotpath"
)

// Listing a source (source.paging). A source without paging is called once.
// A paged one is called page after page until it has no more, until the
// caller has enough, or until the definition's page maximum; every page is
// listed before any item is handled, so an action that removes an item from
// the source cannot move the pages under the listing.

// How a listing ended.
const (
	ListComplete = "complete"      // the source had no more pages
	ListEnough   = "items_per_run" // the run found as many items as it takes; later pages were not asked for
	ListMaxPages = "max_pages"     // the page maximum was reached with pages left: items on them were not seen
)

// listing is what a source listed: each item once (the first time its id
// appeared), in source order; the pages called for; and how it ended ("" for
// a source without paging, whose one call is all there is to know).
type listing struct {
	items []listedItem
	pages int
	end   string
}

// maxExactOffset keeps an offset exact as a JSON number.
const maxExactOffset = 1 << 53

// checkPaging is ValidateStatic's check of source.paging.
func checkPaging(src Source, c Ceilings) []string {
	p := src.Paging
	if p == nil {
		return nil
	}
	var issues []string
	add := func(format string, a ...any) { issues = append(issues, fmt.Sprintf(format, a...)) }
	unused := func(key, value, mode string) {
		if value != "" {
			add("source.paging.%s is not used with mode %q; remove it.", key, mode)
		}
	}
	switch p.Mode {
	case PagingOffset:
		if strings.TrimSpace(p.OffsetArg) == "" {
			add("source.paging.offset_arg is required with mode %q: the argument of %s that takes the number of items to skip.", PagingOffset, sourceName(src))
		} else if v, ok := src.Args[p.OffsetArg]; ok {
			if _, valid := offsetValue(v); !valid {
				add("source.args.%s must be a whole number, 0 or more: it is where the listing starts.", p.OffsetArg)
			}
		}
		unused("cursor_arg", p.CursorArg, PagingOffset)
		unused("next", p.Next, PagingOffset)
	case PagingCursor:
		if strings.TrimSpace(p.CursorArg) == "" {
			add("source.paging.cursor_arg is required with mode %q: the argument of %s that takes the next page's cursor.", PagingCursor, sourceName(src))
		} else if _, ok := src.Args[p.CursorArg]; ok {
			add("source.args.%s must be left out: the first page is asked for without a cursor.", p.CursorArg)
		}
		switch {
		case strings.TrimSpace(p.Next) == "":
			add("source.paging.next is required with mode %q: the dot-path, in the tool's result, to the next page's cursor.", PagingCursor)
		case !validPath(p.Next):
			add("source.paging.next %q is not a dot-path.", p.Next)
		}
		unused("offset_arg", p.OffsetArg, PagingCursor)
	default:
		add("source.paging.mode must be %s.", strings.Join(quoteAll(pagingModes), " or "))
	}
	switch {
	case p.MaxPages < 0:
		add("source.paging.max_pages must be 0 (the default) or more.")
	case c.MaxSourcePages > 0 && p.MaxPages > c.MaxSourcePages:
		add("source.paging.max_pages is %d; this install allows %d.", p.MaxPages, c.MaxSourcePages)
	}
	return issues
}

// sourceName names the source tool in a message.
func sourceName(src Source) string {
	if t := strings.TrimSpace(src.Tool); t != "" {
		return t
	}
	return "the source tool"
}

// offsetValue is an offset argument's value as a whole number, 0 or more.
func offsetValue(v any) (int, bool) {
	var f float64
	switch n := v.(type) {
	case float64:
		f = n
	case int:
		f = float64(n)
	case json.Number:
		parsed, err := n.Float64()
		if err != nil {
			return 0, false
		}
		f = parsed
	default:
		return 0, false
	}
	if f < 0 || f > maxExactOffset || f != math.Trunc(f) {
		return 0, false
	}
	return int(f), true
}

// cursorValue is the next page's cursor in a result: a text that is not
// blank or a number. ok is false when the result names none, which ends
// the list.
func cursorValue(v any) (value any, key string, ok bool) {
	switch c := v.(type) {
	case string:
		if strings.TrimSpace(c) == "" {
			return nil, "", false
		}
		return c, "s:" + c, true
	case json.Number:
		return plainJSON(c), "n:" + c.String(), true
	}
	return nil, "", false
}

// listSource lists def's source as agentID. enough, when not nil, is given
// the items each page added (each id once) and reports whether the caller
// has what it needs, which ends the listing without asking for more pages.
// A source without paging is called once.
//
// A paged source that does not use its paging argument would return the
// same page for ever: a page with items none of which is new, or a cursor
// given twice, is an error naming the page.
func listSource(ctx context.Context, def *Definition, agentID string, tools ToolRunner, c Ceilings,
	enough func(added []listedItem) bool,
) (listing, error) {
	var out listing
	seen := map[string]bool{}
	// add appends a page's items not listed yet and returns them.
	add := func(page []listedItem) []listedItem {
		from := len(out.items)
		for _, it := range page {
			if seen[it.id] {
				continue // a list naming an item twice handles it once
			}
			seen[it.id] = true
			it.index = len(out.items)
			out.items = append(out.items, it)
		}
		return out.items[from:]
	}
	call := func(args map[string]any) (sourcePage, error) {
		page, err := callSource(ctx, def, agentID, tools, args)
		if err == nil {
			out.pages++
		}
		return page, err
	}
	p := def.Source.Paging
	if p == nil {
		page, err := call(plainArgs(def.Source.Args))
		if err != nil {
			return listing{}, err
		}
		add(page.items)
		return out, nil
	}
	maxPages := def.maxPages(c)
	stale := func(what string) error {
		return fmt.Errorf("source %s returned only items already listed on page %d; it does not seem to use %s",
			def.Source.Tool, out.pages, what)
	}

	if p.Mode == PagingOffset {
		offset, _ := offsetValue(def.Source.Args[p.OffsetArg]) // absent: from the start
		for {
			args := plainArgs(def.Source.Args)
			args[p.OffsetArg] = float64(offset)
			page, err := call(args)
			if err != nil {
				return listing{}, err
			}
			if len(page.items) == 0 {
				out.end = ListComplete
				return out, nil
			}
			if out.pages > maxPages {
				// The page after the last one listed has items: they were
				// not seen. Its own items are not taken.
				out.pages, out.end = maxPages, ListMaxPages
				return out, nil
			}
			added := add(page.items)
			if len(added) == 0 {
				return listing{}, stale("source.paging.offset_arg " + strconvQuote(p.OffsetArg))
			}
			if enough != nil && enough(added) {
				out.end = ListEnough
				return out, nil
			}
			offset += len(page.items)
		}
	}

	var cursor any
	cursors := map[string]bool{}
	for {
		args := plainArgs(def.Source.Args)
		if cursor != nil {
			args[p.CursorArg] = cursor
		}
		page, err := call(args)
		if err != nil {
			return listing{}, err
		}
		added := add(page.items)
		next, key, more := cursorValue(dotpath.Get(page.result, p.Next))
		if !more {
			out.end = ListComplete
			return out, nil
		}
		if cursors[key] || (len(page.items) > 0 && len(added) == 0) {
			return listing{}, stale("source.paging.cursor_arg " + strconvQuote(p.CursorArg))
		}
		cursors[key] = true
		if enough != nil && enough(added) {
			out.end = ListEnough
			return out, nil
		}
		if out.pages >= maxPages {
			out.end = ListMaxPages
			return out, nil
		}
		cursor = next
	}
}

// strconvQuote quotes s for a message.
func strconvQuote(s string) string { return quoteAll([]string{s})[0] }

// firstPage is an enough that ends a listing after its first page.
func firstPage([]listedItem) bool { return true }

// truncatedError is the failure of a run whose listing ended at the page
// maximum: the items on the pages after it were not seen, and will not be
// while the pages before them stay full.
type truncatedError struct {
	tool    string
	pages   int
	ceiling int // the install's maximum; 0 when it has none
}

func (e *truncatedError) Error() string {
	text := fmt.Sprintf("source %s lists more items than the %s one run reads, so the rest were not seen; "+
		"make source.args list fewer items", e.tool, pagePlural(e.pages))
	if e.ceiling <= 0 || e.pages < e.ceiling {
		text += " or raise source.paging.max_pages"
		if e.ceiling > 0 {
			text += fmt.Sprintf(" (this install allows %d)", e.ceiling)
		}
	}
	return text
}

func pagePlural(n int) string {
	if n == 1 {
		return "1 page"
	}
	return fmt.Sprintf("%d pages", n)
}

// itemIDs is the ids of items.
func itemIDs(items []listedItem) []string {
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = it.id
	}
	return slices.Clip(ids)
}
