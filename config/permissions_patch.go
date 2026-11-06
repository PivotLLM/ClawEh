// ClawEh
// License: MIT

package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/PivotLLM/ClawEh/fileutil"
)

// rawMember is one member of a JSON object, its value kept verbatim.
type rawMember struct {
	key   string
	value json.RawMessage
}

// decodeRawObject splits a JSON object into its members in file order,
// keeping each value's bytes as they are.
func decodeRawObject(data []byte) ([]rawMember, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errors.New("not a JSON object")
	}
	var members []rawMember
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, errors.New("object key is not a string")
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		members = append(members, rawMember{key: key, value: v})
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return members, nil
}

// encodeRawObject joins members back into a compact JSON object.
func encodeRawObject(members []rawMember) ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range members {
		if i > 0 {
			b.WriteByte(',')
		}
		k, err := json.Marshal(m.key)
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(m.value)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// setRawMember replaces key's value in members, or appends it.
func setRawMember(members []rawMember, key string, value json.RawMessage) []rawMember {
	for i := range members {
		if members[i].key == key {
			members[i].value = value
			return members
		}
	}
	return append(members, rawMember{key: key, value: value})
}

// rawMemberValue returns key's value in members, or nil.
func rawMemberValue(members []rawMember, key string) json.RawMessage {
	for _, m := range members {
		if m.key == key {
			return m.value
		}
	}
	return nil
}

// writePermissionsBlock replaces security.permissions in the config file at
// path with p and leaves every other key as the file holds it: unknown keys,
// secret references, values set only by environment variables and their
// absence all stay as they were. Only that block is decoded; the rest of the
// document is copied through. The file is rewritten atomically with exactly
// p's root file mode, indented two spaces like every config save.
func writePermissionsBlock(path string, p PermissionsConfig) error {
	data, err := os.ReadFile(path) //nolint:gosec // the store's own config path
	if err != nil {
		return err
	}
	top, err := decodeRawObject(data)
	if err != nil {
		return fmt.Errorf("config %s: %w", path, err)
	}
	var security []rawMember
	if v := rawMemberValue(top, "security"); v != nil && !bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
		if security, err = decodeRawObject(v); err != nil {
			return fmt.Errorf("config %s: security: %w", path, err)
		}
	}
	block, err := json.Marshal(p)
	if err != nil {
		return err
	}
	security = setRawMember(security, "permissions", block)
	sec, err := encodeRawObject(security)
	if err != nil {
		return err
	}
	compact, err := encodeRawObject(setRawMember(top, "security", sec))
	if err != nil {
		return err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, compact, "", "  "); err != nil {
		return err
	}
	return fileutil.WriteFileAtomic(path, out.Bytes(), p.Modes(AreaRoot).File)
}
