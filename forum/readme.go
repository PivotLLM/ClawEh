// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"embed"
	"fmt"
	"strings"
)

// The forum guide and the built-in templates, served by the readme tool.
//
//go:embed readme/README.md readme/templates/*.json
var readmeFS embed.FS

// template is one built-in forum configuration: its name (the file
// readme/templates/<name>.json) and one line saying what it does.
type template struct {
	name, description string
}

// templates lists the built-in templates in the order the guide shows them.
// Each has a file under readme/templates; a test keeps the two in step.
var templates = []template{
	{"writing", "A writer drafts on a topic read from a file, two critics review structure and style, the writer revises, an editor finalises."},
	{"council", "Three models answer independently, review each other's answers anonymously and rank them; a chair writes the final answer."},
}

// guide returns the forum guide followed by the list of built-in templates
// and, when any is set, the install's maximums.
func guide(c Ceilings) string {
	data, err := readmeFS.ReadFile("readme/README.md")
	if err != nil {
		panic("forum: embedded guide missing: " + err.Error())
	}
	var b strings.Builder
	b.Write(data)
	b.WriteString("\n## Templates\n\n")
	for _, t := range templates {
		b.WriteString("- `" + t.name + "`: " + t.description + "\n")
	}
	writeCeilings(&b, c)
	return b.String()
}

// writeCeilings appends the install's maximums to the guide, one line per
// limit that has one.
func writeCeilings(b *strings.Builder, c Ceilings) {
	lines := make([]string, 0, 4)
	for _, f := range []struct {
		name  string
		value int
	}{
		{"max_calls", c.MaxCalls},
		{"max_duration_seconds", c.MaxDurationSeconds},
		{"call_timeout_seconds", c.CallTimeoutSeconds},
		{"max_parallel_calls", c.MaxParallelCalls},
	} {
		if f.value > 0 {
			lines = append(lines, fmt.Sprintf("- `%s`: %d\n", f.name, f.value))
		}
	}
	if len(lines) == 0 {
		return
	}
	b.WriteString("\n## This install's maximums\n\n")
	for _, l := range lines {
		b.WriteString(l)
	}
}

// templateConfig returns the named template's configuration verbatim, and
// whether it exists.
func templateConfig(name string) (string, bool) {
	for _, t := range templates {
		if t.name == name {
			data, err := readmeFS.ReadFile("readme/templates/" + name + ".json")
			if err != nil {
				panic("forum: embedded template missing: " + err.Error())
			}
			return string(data), true
		}
	}
	return "", false
}

// templateNames lists the template names for a message: "a, b or c".
func templateNames() string {
	names := make([]string, len(templates))
	for i, t := range templates {
		names[i] = t.name
	}
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
}
