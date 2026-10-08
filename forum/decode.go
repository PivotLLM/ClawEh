// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// Seam (a): strict decoding (spec §3: reject unknown fields and duplicate
// keys before anything else is checked).

// Decode parses one configuration document strictly, in two stages, and
// reports problems as a *ValidationError.
//
// The first stage walks the document and stops it from decoding at all;
// the configuration is nil and the error lists what it found:
//
//   - malformed JSON and trailing content after the one JSON value
//     (reported with line and column);
//   - an unknown field anywhere in the document, naming its path; field
//     names match exactly (encoding/json alone would accept "Version" for
//     "version");
//   - a duplicate key in any object, naming its path, which is also how
//     duplicate participant, source and schema IDs are caught, since those
//     are map keys (checkDuplicateKeys covers the untyped parts: schemas
//     and inline JSON sources);
//   - a value of the wrong JSON type, naming its path.
//
// The second stage checks the decoded configuration and returns it
// together with the error, so a caller can report ValidateStatic's
// findings at the same time:
//
//   - Version must equal ConfigVersion;
//   - an explicit layer `max_calls` of 0 (an absent one means "no layer
//     budget", and the Go zero value cannot tell the two apart);
//   - an explicit `"share": null`: an absent share publishes the whole
//     output and `[]` publishes nothing, and a null would be silently read
//     as absent.
//
// Decode does not validate references or limits; ValidateStatic does.
func Decode(data []byte) (*Config, error) {
	w := &jsonWalker{dec: newTokenDecoder(data), data: data}
	if err := w.document(reflect.TypeFor[Config]()); err != nil {
		return nil, err
	}
	if len(w.issues) > 0 {
		return nil, &ValidationError{Issues: w.issues}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return nil, &ValidationError{Issues: []Issue{decodeIssue(err)}}
	}
	var issues []Issue
	if cfg.Version != ConfigVersion {
		issues = append(issues, Issue{Path: "version", Message: fmt.Sprintf("version %d is not supported (want %d)", cfg.Version, ConfigVersion)})
	}
	issues = append(issues, explicitZeroLayerBudgets(data)...)
	issues = append(issues, explicitNullShares(data)...)
	if len(issues) > 0 {
		// The document decoded: the configuration is returned with the
		// issues, so a caller can report ValidateStatic's findings too.
		return &cfg, &ValidationError{Issues: issues}
	}
	return &cfg, nil
}

// explicitZeroLayerBudgets reports every layer whose `max_calls` is present
// and 0. The document has already decoded, so the error is ignored.
func explicitZeroLayerBudgets(data []byte) []Issue {
	var presence struct {
		Layers []struct {
			MaxCalls *int `json:"max_calls"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(data, &presence); err != nil {
		return nil
	}
	var issues []Issue
	for i, l := range presence.Layers {
		if l.MaxCalls != nil && *l.MaxCalls == 0 {
			issues = append(issues, Issue{Path: fmt.Sprintf("layers[%d].max_calls", i), Message: "must be a positive integer (omit it for no layer budget)"})
		}
	}
	return issues
}

// explicitNullShares reports every layer whose output `share` is present
// and null. The document has already decoded, so the error is ignored.
func explicitNullShares(data []byte) []Issue {
	var presence struct {
		Layers []struct {
			Output struct {
				Share json.RawMessage `json:"share"`
			} `json:"output"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(data, &presence); err != nil {
		return nil
	}
	var issues []Issue
	for i, l := range presence.Layers {
		if string(l.Output.Share) == "null" {
			issues = append(issues, Issue{Path: fmt.Sprintf("layers[%d].output.share", i), Message: "must be an array of JSON pointers, not null (omit it to publish the whole output)"})
		}
	}
	return issues
}

// decodeIssue turns an encoding/json error into an Issue naming the field.
func decodeIssue(err error) Issue {
	if te, ok := errors.AsType[*json.UnmarshalTypeError](err); ok {
		return Issue{Path: indexPath(te.Field), Message: fmt.Sprintf("want %s, got JSON %s", jsonTypeName(te.Type), te.Value)}
	}
	return Issue{Message: err.Error()}
}

// indexPath rewrites encoding/json's field path ("layers.0.max_rounds")
// into this package's form ("layers[0].max_rounds"). Configuration IDs
// cannot start with a digit, so an all-digit segment is an array index.
func indexPath(field string) string {
	var b strings.Builder
	for i, seg := range strings.Split(field, ".") {
		if _, err := strconv.Atoi(seg); err == nil && i > 0 {
			b.WriteString("[" + seg + "]")
			continue
		}
		if i > 0 {
			b.WriteByte('.')
		}
		b.WriteString(seg)
	}
	return b.String()
}

// jsonTypeName names the JSON type a Go type decodes from.
func jsonTypeName(t reflect.Type) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct, reflect.Map:
		return "an object"
	case reflect.Slice, reflect.Array:
		return "an array"
	case reflect.String:
		return "a string"
	case reflect.Bool:
		return "a boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "an integer"
	case reflect.Float32, reflect.Float64:
		return "a number"
	default:
		return t.String()
	}
}

// checkDuplicateKeys walks the token stream of data and fails on the first
// object that repeats a key, naming the key and its JSON path (for example
// "participants.alice"). encoding/json silently keeps the last duplicate, so
// this walk is what enforces §3. It does not decode into Go values and
// accepts any well-formed JSON; malformed JSON is reported as a syntax error.
func checkDuplicateKeys(data []byte) error {
	w := &jsonWalker{dec: newTokenDecoder(data), data: data}
	if err := w.document(nil); err != nil {
		return err
	}
	if len(w.issues) > 0 {
		return &ValidationError{Issues: w.issues[:1]}
	}
	return nil
}

// newTokenDecoder returns a decoder that keeps numbers as json.Number, so
// walking never loses precision or fails on a large number.
func newTokenDecoder(data []byte) *json.Decoder {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	return dec
}

// rawMessageType is json.RawMessage: any JSON value, walked untyped.
var rawMessageType = reflect.TypeFor[json.RawMessage]()

// jsonWalker walks one JSON document token by token. With a Go type it
// also reports object members the type does not declare; without one
// (nil) it only checks for duplicate keys. Findings accumulate in issues;
// a syntax error stops the walk and is returned.
type jsonWalker struct {
	dec    *json.Decoder
	data   []byte
	issues []Issue
}

// document walks the single root value and requires end of input after it.
func (w *jsonWalker) document(t reflect.Type) error {
	if err := w.value("", t); err != nil {
		return err
	}
	end := w.dec.InputOffset()
	if _, err := w.dec.Token(); !errors.Is(err, io.EOF) {
		offset := end
		for offset < int64(len(w.data)) && strings.IndexByte(" \t\r\n", w.data[offset]) >= 0 {
			offset++
		}
		return &ValidationError{Issues: []Issue{{Message: "trailing content after the JSON value" + w.position(offset)}}}
	}
	return nil
}

// token reads the next token, turning a read failure into a syntax issue.
func (w *jsonWalker) token() (json.Token, error) {
	tok, err := w.dec.Token()
	if err == nil {
		return tok, nil
	}
	if errors.Is(err, io.EOF) {
		err = io.ErrUnexpectedEOF
	}
	offset := w.dec.InputOffset()
	if se, ok := errors.AsType[*json.SyntaxError](err); ok && se.Offset > 0 {
		offset = se.Offset - 1 // Offset counts the offending byte
	}
	return nil, &ValidationError{Issues: []Issue{{Message: "malformed JSON" + w.position(offset) + ": " + err.Error()}}}
}

// position renders a byte offset as " at line L, column C".
func (w *jsonWalker) position(offset int64) string {
	if offset < 0 || offset > int64(len(w.data)) {
		return ""
	}
	before := w.data[:offset]
	line := bytes.Count(before, []byte("\n")) + 1
	col := len(before) - bytes.LastIndexByte(before, '\n')
	return fmt.Sprintf(" at line %d, column %d", line, col)
}

// value walks one value at path, expected to decode into t (nil: untyped).
func (w *jsonWalker) value(path string, t reflect.Type) error {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == rawMessageType {
		t = nil
	}
	tok, err := w.token()
	if err != nil {
		return err
	}
	switch tok {
	case json.Delim('{'):
		return w.object(path, t)
	case json.Delim('['):
		var elem reflect.Type
		if t != nil && t.Kind() == reflect.Slice {
			elem = t.Elem()
		}
		for i := 0; w.dec.More(); i++ {
			if verr := w.value(path+"["+strconv.Itoa(i)+"]", elem); verr != nil {
				return verr
			}
		}
		_, err = w.token() // ']'
		return err
	}
	return nil // a scalar; its type is checked by encoding/json
}

// object walks the members of an object whose '{' was just read.
func (w *jsonWalker) object(path string, t reflect.Type) error {
	// A t of another kind is the wrong JSON type, which encoding/json
	// reports; its members are walked untyped.
	var fields map[string]reflect.Type
	var elem reflect.Type
	if t != nil && t.Kind() == reflect.Struct {
		fields = structFields(t)
	} else if t != nil && t.Kind() == reflect.Map {
		elem = t.Elem()
	}
	seen := map[string]bool{}
	for w.dec.More() {
		tok, err := w.token()
		if err != nil {
			return err
		}
		key, ok := tok.(string)
		if !ok { // the decoder only yields string keys
			return fmt.Errorf("decode configuration: object key at %q is not a string", path)
		}
		member := joinPath(path, key)
		if seen[key] {
			w.issues = append(w.issues, Issue{Path: member, Message: fmt.Sprintf("duplicate key %q", key)})
		}
		seen[key] = true
		next := elem
		if fields != nil {
			ft, known := fields[key]
			if !known {
				w.issues = append(w.issues, Issue{Path: member, Message: fmt.Sprintf("unknown field %q (allowed: %s)", key, fieldList(fields))})
			}
			next = ft
		}
		if err := w.value(member, next); err != nil {
			return err
		}
	}
	_, err := w.token() // '}'
	return err
}

// structFields maps the JSON names of t's exported fields to their types.
func structFields(t reflect.Type) map[string]reflect.Type {
	out := map[string]reflect.Type{}
	for f := range t.Fields() {
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		out[name] = f.Type
	}
	return out
}

// fieldList renders the allowed field names, sorted.
func fieldList(fields map[string]reflect.Type) string {
	names := make([]string, 0, len(fields))
	for n := range fields {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// joinPath appends an object member to a dotted path.
func joinPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}
