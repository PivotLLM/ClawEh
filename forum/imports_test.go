// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The forum package imports no ClawEh package but its own forumfs, and
// forumfs imports nothing, so config can name the forum's folders without
// depending on the forum.
func TestForumImportsNoClawEhPackage(t *testing.T) {
	const module = "github.com/PivotLLM/ClawEh/"
	for dir, allowed := range map[string]map[string]bool{
		".":       {module + "forum/forumfs": true},
		"forumfs": {},
	} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
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
				if dir == "forumfs" || (strings.HasPrefix(path, module) && !allowed[path]) {
					t.Errorf("%s imports %s", f, path)
				}
			}
		}
	}
}
