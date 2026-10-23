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

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

// schemaURL is the in-memory location every compiled schema is registered
// under. It is never loaded from disk; the loader refuses every URL.
const schemaURL = "forum:///schema.json"

// compileSchema parses and compiles one JSON Schema document with
// github.com/santhosh-tekuri/jsonschema/v6: Draft 2020-12 by default, and no
// reference may leave the document (internal references only). It fails on
// a schema that is not valid or that references anything outside itself.
func compileSchema(schema json.RawMessage) (*compiledSchema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema))
	if err != nil {
		return nil, fmt.Errorf("compile schema: %w", err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.UseLoader(refuseLoader{})
	if err = c.AddResource(schemaURL, doc); err != nil {
		return nil, fmt.Errorf("compile schema: %w", err)
	}
	sch, err := c.Compile(schemaURL)
	if err != nil {
		return nil, fmt.Errorf("compile schema: %w", err)
	}
	return &compiledSchema{sch: sch}, nil
}

// compiledSchema validates JSON instances against one compiled schema.
type compiledSchema struct {
	sch *jsonschema.Schema
}

// Validate checks one JSON instance. A violation is returned as a
// *SchemaViolationError with one message per failing location
// ("<instance location>: <error>"); an instance that is not valid JSON is
// returned as an ordinary error, since the caller has already parsed it.
func (s *compiledSchema) Validate(instance []byte) error {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(instance))
	if err != nil {
		return fmt.Errorf("validate instance: %w", err)
	}
	err = s.sch.Validate(doc)
	if ve, ok := errors.AsType[*jsonschema.ValidationError](err); ok {
		return &SchemaViolationError{Messages: violationMessages(ve.BasicOutput(), nil)}
	}
	return err
}

// violationMessages flattens the basic output into leaf messages.
func violationMessages(u *jsonschema.OutputUnit, acc []string) []string {
	if u == nil {
		return acc
	}
	if u.Error != nil && len(u.Errors) == 0 {
		loc := u.InstanceLocation
		if loc == "" {
			loc = "/"
		}
		acc = append(acc, loc+": "+u.Error.String())
	}
	for i := range u.Errors {
		acc = violationMessages(&u.Errors[i], acc)
	}
	return acc
}

// refuseLoader is the URL loader that makes every external reference a
// compile error.
type refuseLoader struct{}

func (refuseLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("reference to %q: only references inside the schema are allowed", url)
}
