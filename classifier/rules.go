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
// Labels, each shown in the prompt only on the section's marker line
// "=== Rules from <label> ===" (sectionMarker), never as a Markdown heading,
// so a file's own headings are text, not labels:
//   - classify.instructions: "instructions";
//   - classify.instructions_file: its cleaned workspace path, such as
//     "files/classifiers/inbox/guide.md";
//   - each file of classify.instructions_dir: its file name;
//   - classify.rules: "rule N", N its number (1 for the first), in the
//     numbered list.
//
// Matching a cited label (resolveLabel): the exact label wins. Otherwise
// these loose forms count only when they name exactly one label (several:
// unknown): the label in any case; a file label without ".md"; the
// instructions file's bare name, with or without ".md"; an inline rule as
// "rule #N", "rules N", "#N" or "N". Bare "none" always means no rule, so a
// folder file none.md is cited only as "none.md". A number that names no
// numbered rule names the one rule file whose name starts with it
// (numberedFiles): "rule 11" is 11-jira.md when there is no rule 11.
//
// A citation that names no label as written is tried again in the shapes a
// model gives a label it does know (citedForms), each by the same rules:
// without the marker around it ("=== Rules from <label> ===", "Rules from
// <label>"), without a note in parentheses after it ("<label> (client
// domain)"), and, for a path, by its last element ("rules/<label>"). A cited
// token that still names nothing is kept as written (Decision.UnknownLabels),
// so the agent can see what the model called its rules.

// Rule label constants.
const (
	// RulesNone is the stored citation of a decision for which the model
	// said no rule applied ("Rules: none").
	RulesNone = "none"
	// labelInstructions labels classify.instructions.
	labelInstructions = "instructions"
)

// sectionMarker is the line that starts a section of the prompt and shows
// its label.
func sectionMarker(label string) string { return "=== Rules from " + label + " ===" }

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
		add(section{label: path.Clean(def.Classify.InstructionsFile), text: t})
	}
	for _, f := range in.dir {
		add(section{label: f.Name, text: strings.TrimSpace(f.Text)})
	}
	for i, r := range def.Classify.Rules {
		add(section{label: ruleLabel(i + 1), text: strings.TrimSpace(r), rule: i + 1})
	}
	return out
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
// accepted forms above): the exact label, else the one label a loose form
// names; none when several do, and never for bare "none".
func resolveLabel(labels []string, token string) (string, bool) {
	for _, form := range citedForms(token) {
		if l, ok := matchLabel(labels, form); ok {
			return l, true
		}
	}
	return "", false
}

// rulesFromPrefix starts a label cited with its marker's words.
const rulesFromPrefix = "rules from "

// citedForms are the texts a cited token is matched as, in order: the token
// itself, then the same without the section marker's "=" and "Rules from",
// then without a note in parentheses after the label, then a path's last
// element. Each later form is derived from the one before, so
// "=== Rules from rules/050-x.md (clients) ===" ends as "050-x.md".
func citedForms(token string) []string {
	token = strings.Join(strings.Fields(token), " ")
	forms := []string{token}
	add := func(next string) string {
		next = citationTrim(next)
		if next != "" && next != forms[len(forms)-1] {
			forms = append(forms, next)
		}
		return forms[len(forms)-1]
	}
	cur := add(strings.Trim(token, "= "))
	if hasPrefixFold(cur, rulesFromPrefix) {
		cur = add(cur[len(rulesFromPrefix):])
	}
	if i := strings.Index(cur, " ("); i > 0 {
		cur = add(cur[:i])
	}
	if i := strings.LastIndex(cur, "/"); i >= 0 {
		add(cur[i+1:])
	}
	return forms
}

// matchLabel is resolveLabel for one form of a cited token.
func matchLabel(labels []string, token string) (string, bool) {
	if token == "" || strings.EqualFold(token, RulesNone) {
		return "", false
	}
	for _, l := range labels {
		if l == token {
			return l, true
		}
	}
	rule := ""
	if m := ruleNumber.FindStringSubmatch(token); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			rule = ruleLabel(n)
		}
	}
	var found []string
	for _, l := range labels {
		if l == rule || slices.ContainsFunc(looseForms(l), func(f string) bool { return strings.EqualFold(f, token) }) {
			found = append(found, l)
		}
	}
	if len(found) == 0 && rule != "" {
		// A number that is no numbered rule: the one rule file whose name
		// starts with it ("rule 11" for 11-jira.md, "5" for 005-vip.md).
		found = numberedFiles(labels, rule)
	}
	if len(found) != 1 {
		return "", false
	}
	return found[0], true
}

// leadingNumber matches the digits a file name starts with.
var leadingNumber = regexp.MustCompile(`^\d+`)

// numberedFiles are the file labels whose name (a path's last element)
// starts with the number of the inline-rule label rule, as "11-jira.md"
// does for "rule 11"; leading zeros do not count ("005-" is 5).
func numberedFiles(labels []string, rule string) []string {
	var out []string
	for _, l := range labels {
		if !isMarkdownName(l) {
			continue // "instructions" and "rule N" are not files
		}
		digits := leadingNumber.FindString(path.Base(l))
		if n, err := strconv.Atoi(digits); digits != "" && err == nil && ruleLabel(n) == rule {
			out = append(out, l)
		}
	}
	return out
}

// looseForms are the forms a label may also be cited by, in any case: the
// label, without ".md", and for a path (the instructions file) its bare
// name with and without ".md".
func looseForms(label string) []string {
	forms := []string{label}
	names := []string{label}
	if strings.Contains(label, "/") {
		base := path.Base(label)
		forms = append(forms, base)
		names = append(names, base)
	}
	for _, n := range names {
		if isMarkdownName(n) {
			forms = append(forms, n[:len(n)-len(".md")])
		}
	}
	return forms
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
// "rules" in any case, then a colon. "Rule:" starts a reason, not a
// citation.
func rulesLineBody(line string) (string, bool) {
	s := strings.TrimLeft(line, " \t\"'`*[(<-")
	if !hasPrefixFold(s, "rules") {
		return "", false
	}
	rest := strings.TrimLeft(s[len("rules"):], " \t*`\"'")
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
		for _, t := range splitCitation(citationTrim(body)) {
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

// splitCitation splits a Rules line's body into its cited tokens at commas
// and semicolons, except those inside parentheses, which belong to a note
// after a label ("050-x.md (clients, partners)").
func splitCitation(body string) []string {
	var out []string
	depth, start := 0, 0
	for i, r := range body {
		switch r {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ',', ';':
			if depth == 0 {
				out = append(out, body[start:i])
				start = i + 1
			}
		}
	}
	return append(out, body[start:])
}

// Limits of the unknown labels kept from one reply.
const (
	// UnknownLabelsMax is how many are kept.
	UnknownLabelsMax = 10
	// UnknownLabelChars cuts each of them, in characters.
	UnknownLabelChars = 120
)

// resolve matches the citation against the labels loaded for the request:
// the known labels in the reply's order without repeats, or [RulesNone],
// and the tokens that named no loaded label, as the model wrote them (each
// once, cut to UnknownLabelChars, at most UnknownLabelsMax). The rules list
// is never nil: it is empty when the reply had no Rules line or cited only
// unknown labels.
func (c citation) resolve(labels []string) (rules, unknown []string) {
	rules = []string{}
	if !c.given {
		return rules, nil
	}
	if c.none {
		return []string{RulesNone}, nil
	}
	for _, t := range c.tokens {
		l, ok := resolveLabel(labels, t)
		switch {
		case !ok:
			t = strings.Join(strings.Fields(t), " ")
			if strings.Count(t, "(") > strings.Count(t, ")") {
				t += ")" // trimmed as end punctuation: the note's own bracket
			}
			t = cutRunes(t, UnknownLabelChars)
			if len(unknown) < UnknownLabelsMax && !slices.Contains(unknown, t) {
				unknown = append(unknown, t)
			}
		case !slices.Contains(rules, l):
			rules = append(rules, l)
		}
	}
	return rules, unknown
}

// storedRules is the citation of a categorised decision as read back from
// state: a row written before rules were stored, or one whose citation
// was empty, has an empty list, never nil.
func storedRules(rules []string) []string {
	if rules == nil {
		return []string{}
	}
	return rules
}
