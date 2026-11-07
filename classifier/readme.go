// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package classifier

import (
	_ "embed"
	"fmt"
	"strings"
)

// The classifier guide, served by the readme tool.
//
//go:embed readme/README.md
var readme string

// guide returns the classifier guide followed by the install's maximums.
func guide(c Ceilings) string {
	var b strings.Builder
	b.WriteString(readme)
	lines := make([]string, 0, 7)
	for _, f := range []struct {
		name  string
		value int
	}{
		{"max_rules", c.MaxRules},
		{"max_prompt_chars", c.MaxPromptChars},
		{"max_item_chars", c.MaxItemChars},
		{"max_items_per_run", c.MaxItemsPerRun},
		{"max_parallel", c.MaxParallel},
		{"max_activity_hours", c.MaxActivityHours},
	} {
		if f.value > 0 {
			lines = append(lines, fmt.Sprintf("- `%s`: %d\n", f.name, f.value))
		}
	}
	if len(lines) > 0 {
		b.WriteString("\n## This install's maximums\n\n")
		for _, l := range lines {
			b.WriteString(l)
		}
	}
	return b.String()
}
