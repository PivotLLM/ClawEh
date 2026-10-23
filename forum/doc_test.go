// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// doc.go and DESIGN.md list every source file of the package.
func TestFileMapsComplete(t *testing.T) {
	doc, err := os.ReadFile("doc.go")
	if err != nil {
		t.Fatal(err)
	}
	design, err := os.ReadFile("DESIGN.md")
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "doc.go" {
			continue
		}
		if !strings.Contains(string(doc), f) {
			t.Errorf("doc.go does not list %s", f)
		}
		if !strings.Contains(string(design), "`"+f+"`") {
			t.Errorf("DESIGN.md does not list %s", f)
		}
	}
}
