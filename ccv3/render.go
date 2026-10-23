// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package ccv3

import (
	"cmp"
	"regexp"
	"slices"
	"strings"
)

// exampleSeparator splits mes_example into examples (matched in any case,
// as the apps that write cards do).
var exampleSeparator = regexp.MustCompile(`(?i)<START>`)

// Render turns a card into the system-prompt block every agent form uses, so
// a character always produces the same text:
//
//	# Character
//	<system_prompt>
//	You are <nickname or name>.
//	## Description, ## Personality, ## Scenario
//	## How <name> speaks   (each <START> example as a fenced text block)
//	## Background          (enabled constant lorebook entries, by insertion_order)
//
// Blocks are separated by a blank line and an empty one is left out. Every
// rendered field has its macros expanded (ExpandMacros) and its surrounding
// whitespace trimmed. The greetings, post_history_instructions, keyed
// lorebook entries, creator notes, tags, assets and metadata are never
// rendered. c must be a valid card, as the store returns; a card whose data
// cannot be read renders as "".
func Render(c *Card) string {
	d, err := decodeData(c.Data)
	if err != nil {
		return ""
	}
	char := d.displayName()
	field := func(s string) string { return strings.TrimSpace(ExpandMacros(s, char)) }

	blocks := []string{"# Character"}
	if sp := field(d.SystemPrompt); sp != "" {
		blocks = append(blocks, sp)
	}
	blocks = append(blocks, "You are "+field(char)+".")
	section := func(heading, body string) {
		if body != "" {
			blocks = append(blocks, "## "+heading+"\n"+body)
		}
	}
	section("Description", field(d.Description))
	section("Personality", field(d.Personality))
	section("Scenario", field(d.Scenario))
	section("How "+field(d.Name)+" speaks", examples(field(d.MesExample)))
	section("Background", background(d.CharacterBook, field))
	return strings.Join(blocks, "\n\n")
}

// examples renders mes_example: each <START> block as one fenced example.
func examples(text string) string {
	var out []string
	for _, part := range exampleSeparator.Split(text, -1) {
		if part = strings.TrimSpace(part); part != "" {
			fence := strings.Repeat("`", max(3, longestBacktickRun(part)+1))
			out = append(out, fence+"text\n"+part+"\n"+fence)
		}
	}
	return strings.Join(out, "\n\n")
}

// longestBacktickRun is the longest run of backticks in s, so an example's
// fence is longer than anything inside it.
func longestBacktickRun(s string) int {
	longest, run := 0, 0
	for i := range len(s) {
		if s[i] == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	return longest
}

// background renders the lorebook entries that are always in effect: enabled
// and constant, in insertion_order (ties keep their order in the book).
func background(book *characterBook, field func(string) string) string {
	if book == nil {
		return ""
	}
	var entries []bookEntry
	for _, e := range book.Entries {
		if e.Enabled && e.Constant {
			entries = append(entries, e)
		}
	}
	slices.SortStableFunc(entries, func(a, b bookEntry) int { return cmp.Compare(a.InsertionOrder, b.InsertionOrder) })
	var out []string
	for _, e := range entries {
		if text := field(e.Content); text != "" {
			out = append(out, text)
		}
	}
	return strings.Join(out, "\n\n")
}

// ExpandMacros expands the identity macros in text: {{char}} becomes
// charName, {{user}} becomes "the user" and a {{// ...}} comment is removed.
// Macro names are matched in any case. Every other macro, and a "{{" with no
// closing "}}", is left as written.
func ExpandMacros(text, charName string) string {
	var b strings.Builder
	for {
		start := strings.Index(text, "{{")
		if start < 0 {
			break
		}
		end := strings.Index(text[start+2:], "}}")
		if end < 0 {
			break
		}
		inner := text[start+2 : start+2+end]
		b.WriteString(text[:start])
		switch {
		case strings.HasPrefix(inner, "//"):
		case strings.EqualFold(inner, "char"):
			b.WriteString(charName)
		case strings.EqualFold(inner, "user"):
			b.WriteString("the user")
		default:
			b.WriteString(text[start : start+2+end+2])
		}
		text = text[start+2+end+2:]
	}
	b.WriteString(text)
	return b.String()
}
