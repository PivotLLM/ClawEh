// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package classifier

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The classifier package imports no ClawEh package but its own classifierfs
// and the stdlib-only leaves internal/mergepatch and internal/dotpath, and
// classifierfs imports nothing, so config can name the classifier folder
// without depending on the engine and the agent loop can host it.
func TestClassifierImportsNoClawEhPackage(t *testing.T) {
	const module = "github.com/PivotLLM/ClawEh/"
	for dir, allowed := range map[string]map[string]bool{
		".": {
			module + "classifier/classifierfs": true,
			module + "internal/mergepatch":     true,
			module + "internal/dotpath":        true,
		},
		"classifierfs": {},
	} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		if len(files) == 0 {
			t.Fatalf("no Go files in %s", dir)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			parsed, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, imp := range parsed.Imports {
				path, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					t.Fatal(err)
				}
				if dir == "classifierfs" || (strings.HasPrefix(path, module) && !allowed[path]) {
					t.Errorf("%s imports %s", f, path)
				}
			}
		}
	}
}

// internal/dotpath, which the classifier and the watches share, imports only
// the standard library.
func TestDotpathImportsOnlyStdlib(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "internal", "dotpath", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range parsed.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(strings.SplitN(path, "/", 2)[0], ".") {
				t.Errorf("%s imports %s", f, path)
			}
		}
	}
}
