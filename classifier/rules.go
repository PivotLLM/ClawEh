// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package classifier

import (
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Rule citations: every source of instructions in the prompt has a label
// the model cites on its reply's Rules line, so the agent can see which of
// its rules drove each decision.
//
// Labels:
//   - classify.instructions: "instructions";
//   - classify.instructions_file: its file name (its workspace path when a
//     file of instructions_dir has the same name);
//   - each file of classify.instructions_dir: its file name;
//   - classify.rules: "rule N", N its number (1 for the first).
//
// A cited label is matched case-insensitively. Also accepted: a file name
// without ".md", and for an inline rule "rule #N", "#N" or "N". A file name
// matches before a rule number (a file named 3.md is "3").

// Rule label constants.
const (
	// RulesNone is the stored citation of a decision for which the model
	// said no rule applied ("Rules: none").
	RulesNone = "none"
	// labelInstructions labels classify.instructions.
	labelInstructions = "instructions"
)

// ruleLabel is the label of inline rule n (1-based).
func ruleLabel(n int) string { return "rule " + strconv.Itoa(n) }

// section is one source of instructions in the prompt: its label, its
// text, and for an inline rule its number (0 otherwise).
type section struct {
	label string
	text  string
	rule  int
}

// promptSections are the definition's instructions and Markdown (in) in
// prompt order, each with its label: the inline instructions and the
// instructions file when not empty, every folder file, then the inline
// rules. Excluded labels (in.exclude) are left out; the numbering of the
// rules stays as defined.
func promptSections(def *Definition, in instructions) []section {
	var out []section
	add := func(s section) {
		if !in.exclude[s.label] {
			out = append(out, s)
		}
	}
	if t := strings.TrimSpace(def.Classify.Instructions); t != "" {
		add(section{label: labelInstructions, text: t})
	}
	if t := strings.TrimSpace(in.file); t != "" {
		add(section{label: fileLabel(def.Classify.InstructionsFile, in.dir), text: t})
	}
	for _, f := range in.dir {
		add(section{label: f.Name, text: strings.TrimSpace(f.Text)})
	}
	for i, r := range def.Classify.Rules {
		add(section{label: ruleLabel(i + 1), text: strings.TrimSpace(r), rule: i + 1})
	}
	return out
}

// fileLabel is the instructions file's label: its name, or its cleaned
// path when a folder file has the same name (in any case).
func fileLabel(file string, dir []NamedText) string {
	name := path.Base(path.Clean(file))
	for _, f := range dir {
		if strings.EqualFold(f.Name, name) {
			return path.Clean(file)
		}
	}
	return name
}

// promptLabels are the labels of the sections a request with def and in
// sends, in prompt order.
func promptLabels(def *Definition, in instructions) []string {
	secs := promptSections(def, in)
	out := make([]string, len(secs))
	for i, s := range secs {
		out[i] = s.label
	}
	return out
}

// ruleNumber matches the accepted ways of citing inline rule N.
var ruleNumber = regexp.MustCompile(`(?i)^(?:rules?\s*)?#?\s*(\d+)$`)

// resolveLabel returns the label of labels that token names (see the
// accepted forms above), in this order: the exact label, the label in any
// case, a file name without ".md", then an inline rule's number.
func resolveLabel(labels []string, token string) (string, bool) {
	token = strings.Join(strings.Fields(token), " ")
	if token == "" {
		return "", false
	}
	for _, l := range labels {
		if l == token {
			return l, true
		}
	}
	for _, l := range labels {
		if strings.EqualFold(l, token) {
			return l, true
		}
	}
	for _, l := range labels {
		if isMarkdownName(l) && strings.EqualFold(l[:len(l)-len(".md")], token) {
			return l, true
		}
	}
	if m := ruleNumber.FindStringSubmatch(token); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			want := ruleLabel(n)
			for _, l := range labels {
				if l == want {
					return l, true
				}
			}
		}
	}
	return "", false
}

// citation is a reply's Rules line: absent (given false), "none", or the
// tokens it lists as written.
type citation struct {
	given  bool
	none   bool
	tokens []string
}

// citationTrim is what a cited token (and the Rules line's body) is
// trimmed of: spaces, quotes, backticks, brackets, emphasis and end
// punctuation.
func citationTrim(s string) string {
	return strings.TrimRight(strings.Trim(s, " \t\"'`*[](){}<>"), ".,;:!? \t\"'`*])}>")
}

// rulesLineBody returns the text after "Rules:" when line is a Rules line:
// it starts, after any quotes, emphasis, brackets or a list dash, with
// "rules" or "rule" in any case, then a colon.
func rulesLineBody(line string) (string, bool) {
	s := strings.TrimLeft(line, " \t\"'`*[(<-")
	var rest string
	switch {
	case hasPrefixFold(s, "rules"):
		rest = s[len("rules"):]
	case hasPrefixFold(s, "rule"):
		rest = s[len("rule"):]
	default:
		return "", false
	}
	rest = strings.TrimLeft(rest, " \t*`\"'")
	body, ok := strings.CutPrefix(rest, ":")
	return body, ok
}

func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

// parseCitation finds the Rules line of a reply's non-empty lines
// (replyLines) in the second or third position. A line that lists nothing
// is not a citation.
func parseCitation(lines []string) citation {
	for i := 1; i < len(lines) && i <= 2; i++ {
		body, ok := rulesLineBody(lines[i])
		if !ok {
			continue
		}
		var c citation
		for t := range strings.FieldsFuncSeq(citationTrim(body), func(r rune) bool { return r == ',' || r == ';' }) {
			if t = citationTrim(t); t == "" {
				continue
			}
			if strings.EqualFold(t, RulesNone) {
				c.none = true
				continue
			}
			c.tokens = append(c.tokens, t)
		}
		if len(c.tokens) > 0 {
			c.none = false // "none" beside labels says nothing
		}
		c.given = c.none || len(c.tokens) > 0
		return c
	}
	return citation{}
}

// resolve matches the citation against the labels loaded for the request:
// the known labels in the reply's order without repeats, or [RulesNone],
// and how many tokens named no loaded label. No Rules line is nil, 0.
func (c citation) resolve(labels []string) (rules []string, unknown int) {
	if !c.given {
		return nil, 0
	}
	if c.none {
		return []string{RulesNone}, 0
	}
	for _, t := range c.tokens {
		l, ok := resolveLabel(labels, t)
		switch {
		case !ok:
			unknown++
		case !slices.Contains(rules, l):
			rules = append(rules, l)
		}
	}
	return rules, unknown
}
