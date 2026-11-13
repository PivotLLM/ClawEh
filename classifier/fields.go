// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package classifier

import (
	"bytes"
	"encoding/json"
	"html"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/PivotLLM/ClawEh/internal/dotpath"
)

// Field is one named value sent to the model about an item.
type Field struct {
	Name      string
	Value     string
	Shortened bool // cut to fit the item's budget
}

// itemData is what one item gives the prompt and the substitution: the
// fields in name order, the names whose path resolved to nothing, and the
// fetched values with their JSON types (for {{fetched.<name>}}). notJSON is
// the parse error of a fetch result that is not JSON.
type itemData struct {
	fields  []Field
	missing []string
	fetched map[string]any
	notJSON error
}

// shortened is the names of the fields cut to fit the item's budget.
func (d itemData) shortened() []string {
	var out []string
	for _, f := range d.fields {
		if f.Shortened {
			out = append(out, f.Name)
		}
	}
	return out
}

// fieldsFromElement is the item's data when the definition has no fetch:
// every member of the list element (in name order), or the whole element as
// one field "item" when it is not an object.
func fieldsFromElement(el any) itemData {
	obj, ok := el.(map[string]any)
	if !ok {
		return itemData{fields: []Field{{Name: "item", Value: valueText(el)}}}
	}
	names := make([]string, 0, len(obj))
	for k := range obj {
		names = append(names, k)
	}
	sort.Strings(names)
	var d itemData
	for _, k := range names {
		d.fields = append(d.fields, Field{Name: k, Value: valueText(obj[k])})
	}
	return d
}

// fieldsFromFetch selects the fetch fields from a fetch result (JSON, or
// plain text in which no path resolves), reducing the fields fetch.html names
// from HTML to text.
func fieldsFromFetch(result string, f *Fetch) itemData {
	d := itemData{fetched: map[string]any{}}
	var decoded any = result
	if v, err := decodeJSON(result); err == nil {
		decoded = v
	} else {
		d.notJSON = err
	}
	for _, name := range sortedKeys(f.Fields) {
		v := dotpath.Get(decoded, f.Fields[name])
		if v == nil {
			d.missing = append(d.missing, name)
			continue
		}
		d.fetched[name] = v
		text := valueText(v)
		if slices.Contains(f.HTML, name) {
			text = htmlToText(text)
		}
		d.fields = append(d.fields, Field{Name: name, Value: text})
	}
	return d
}

// valueText is a value as the model reads it: a string as is, anything else
// as compact JSON.
func valueText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return ""
	}
	return strings.TrimSuffix(b.String(), "\n")
}

var (
	reHTMLScript = regexp.MustCompile(`(?is)<script\b.*?</script\s*>`)
	reHTMLStyle  = regexp.MustCompile(`(?is)<style\b.*?</style\s*>`)
	reHTMLBreak  = regexp.MustCompile(`(?i)<br\s*/?>|</(p|div|li|tr|h[1-6])\s*>`)
	reHTMLTag    = regexp.MustCompile(`(?s)<[^>]*>`)
	reHTMLSpaces = regexp.MustCompile(`[ \t\f\v\r]+`)
)

// htmlToText reduces HTML to its text: scripts and styles removed, line
// breaks kept at <br> and the end of blocks, tags stripped, entities
// decoded, runs of spaces collapsed and blank lines dropped.
func htmlToText(s string) string {
	s = reHTMLScript.ReplaceAllLiteralString(s, "")
	s = reHTMLStyle.ReplaceAllLiteralString(s, "")
	s = reHTMLBreak.ReplaceAllLiteralString(s, "\n")
	s = reHTMLTag.ReplaceAllLiteralString(s, "")
	s = html.UnescapeString(s)
	s = strings.ReplaceAll(s, " ", " ")
	var lines []string
	for line := range strings.SplitSeq(s, "\n") {
		line = strings.TrimSpace(reHTMLSpaces.ReplaceAllLiteralString(line, " "))
		if line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

// truncate cuts the fields until together they have at most budget
// characters, the longest first: while over, the longest field (every field
// of that length, sharing the cut) is cut to the larger of the next length
// down and its length less the excess, and marked Shortened. budget <= 0
// leaves them whole.
func truncate(fields []Field, budget int) []Field {
	out := append([]Field(nil), fields...)
	if budget <= 0 {
		return out
	}
	for {
		total, longest, next, count := 0, 0, 0, 0
		for _, f := range out {
			n := utf8.RuneCountInString(f.Value)
			total += n
			switch {
			case n > longest:
				next, longest, count = longest, n, 1
			case n == longest:
				count++
			case n > next:
				next = n
			}
		}
		if total <= budget || longest == 0 {
			return out
		}
		share := (total - budget + count - 1) / count
		target := max(next, longest-share)
		for i := range out {
			if utf8.RuneCountInString(out[i].Value) == longest {
				out[i].Value = cutRunes(out[i].Value, target)
				out[i].Shortened = true
			}
		}
	}
}

// cutRunes returns the first n runes of s.
func cutRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}
