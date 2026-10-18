// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

// Package mergepatch applies RFC 7386 JSON merge patches while keeping the
// target document's member order and untouched bytes. It imports only the
// standard library, so packages that must stay free of ClawEh imports (the
// forum, the character-card library) can share it.
package mergepatch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
)

// member is one member of a JSON object, in document order.
type member struct {
	key   string
	value json.RawMessage
}

// Apply applies patch to target as an RFC 7386 JSON merge patch: a
// patch that is not an object replaces the target; an object patch merges
// member by member, a null member deletes that key, and any other member is
// merged into the target's value recursively (so arrays are replaced whole).
// A nil target is absent. Object members keep their order, new ones are
// appended, and values the patch does not touch are kept byte for byte.
func Apply(target, patch json.RawMessage) (json.RawMessage, error) {
	patchMembers, isObject, err := objectMembers(patch)
	if err != nil {
		return nil, err
	}
	if !isObject {
		return patch, nil
	}
	var members []member
	if target != nil {
		if members, isObject, err = objectMembers(target); err != nil {
			return nil, err
		}
		if !isObject {
			members = nil
		}
	}
	for _, p := range patchMembers {
		sameKey := func(m member) bool { return m.key == p.key }
		if isNull(p.value) {
			members = slices.DeleteFunc(members, sameKey)
			continue
		}
		i := slices.IndexFunc(members, sameKey)
		var current json.RawMessage
		if i >= 0 {
			current = members[i].value
		}
		merged, err := Apply(current, p.value)
		if err != nil {
			return nil, err
		}
		if i >= 0 {
			members[i].value = merged
		} else {
			members = append(members, member{key: p.key, value: merged})
		}
	}
	return encodeMembers(members)
}

// IsObject reports whether raw is a JSON object. Malformed JSON is an error.
func IsObject(raw json.RawMessage) (bool, error) {
	_, isObject, err := objectMembers(raw)
	return isObject, err
}

// objectMembers returns the members of raw in order when it is a JSON
// object, and false when it is any other JSON value.
func objectMembers(raw json.RawMessage) ([]member, bool, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, false, fmt.Errorf("merge patch: %w", err)
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, false, nil
	}
	var out []member
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, false, fmt.Errorf("merge patch: %w", err)
		}
		key, ok := tok.(string)
		if !ok {
			return nil, false, fmt.Errorf("merge patch: object key %v is not a string", tok)
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, false, fmt.Errorf("merge patch: member %q: %w", key, err)
		}
		out = append(out, member{key: key, value: value})
	}
	return out, true, nil
}

// isNull reports whether raw is the JSON null.
func isNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// encodeMembers writes members as a compact JSON object.
func encodeMembers(members []member) (json.RawMessage, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range members {
		if i > 0 {
			b.WriteByte(',')
		}
		key, err := marshalCompact(m.key)
		if err != nil {
			return nil, fmt.Errorf("merge patch: %w", err)
		}
		b.Write(key)
		b.WriteByte(':')
		b.Write(m.value)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// marshalCompact is json.Marshal without HTML escaping.
func marshalCompact(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}
