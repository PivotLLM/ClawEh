// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package forum

import (
	"embed"
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

// guide returns the forum guide followed by the list of built-in templates.
func guide() string {
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
	return b.String()
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
