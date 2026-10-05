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
)

// Seam (a): strict decoding (spec §3: reject unknown fields and duplicate
// keys before anything else is checked).

// Decode parses one configuration document strictly:
//
//   - unknown fields anywhere in the document are an error
//     (json.Decoder.DisallowUnknownFields);
//   - a duplicate key in any object is an error (checkDuplicateKeys), which
//     is also how duplicate participant, source and schema IDs are caught,
//     since those are map keys;
//   - trailing content after the one JSON value is an error;
//   - Version must equal ConfigVersion.
//
// Decode does not validate references or limits; call ValidateStatic next.
func Decode(data []byte) (*Config, error) {
	if err := checkDuplicateKeys(data); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode configuration: %w", err)
	}
	if dec.More() {
		return nil, errors.New("decode configuration: trailing content after the JSON value")
	}
	if cfg.Version != ConfigVersion {
		return nil, fmt.Errorf("decode configuration: version %d is not supported (want %d)", cfg.Version, ConfigVersion)
	}
	return &cfg, nil
}

// checkDuplicateKeys walks the token stream of data and fails on the first
// object that repeats a key, naming the key and its JSON path (for example
// "participants.alice"). encoding/json silently keeps the last duplicate, so
// this walk is what enforces §3. It does not decode into Go values and
// accepts any well-formed JSON; malformed JSON is reported as a syntax error.
func checkDuplicateKeys(data []byte) error {
	return errNotImplemented
}
