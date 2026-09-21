// ClawEh
// License: MIT

// Package audit builds a security-audit report describing what a ClawEh
// instance is able to do — its listeners, credentials, agents and their folder
// access, external services and scheduled activity — from the loaded config
// and a few facts about the running process, and renders it as PDF or plain
// text. It is an inventory rather than a severity rating, and it never
// contains a secret value: keys, tokens, passwords and header or env values
// are reported only as set/not set, by name, or by count.
package audit

import "time"

// Environment carries facts about the running process that the config cannot
// supply. The caller fills it; this package never reads the environment.
type Environment struct {
	ConfigPath string
	DataDir    string
	Executable string
	Hostname   string
	User       string // e.g. "eric"
	Group      string // e.g. "eric"
	OS, Arch   string
	GoVersion  string
	Version    string // app.Version(), e.g. "0.6.0+99d4f1b8"
	Commit     string
	BuildTime  string
	Now        time.Time
}

// Report is the collected audit: identity, then one Section per area.
type Report struct {
	Product     string // "ClawEh"
	TagLine     string
	Version     string
	GeneratedAt time.Time
	Sections    []Section
}

// Section is one area of the report: a title, short notes, tables, and at
// most one level of subsections (for example one per agent).
type Section struct {
	Title       string
	Notes       []string // short paragraphs shown under the title
	Tables      []Table
	Subsections []Section // one level is enough (e.g. one per agent)
}

// Table is a captioned grid. Highlight lists row indexes to emphasise (shaded
// in the PDF, marked in the text rendering).
type Table struct {
	Caption   string
	Columns   []string
	Rows      [][]string
	Highlight []int // row indexes to emphasise (shaded in the PDF)
}
