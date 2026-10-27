// Package templates embeds the default agent workspace template files.
// These are copied into a new agent workspace on first creation.
package templates

import "embed"

//go:embed *.md channels all:skills
var FS embed.FS
