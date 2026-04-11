package slack

import (
	"fmt"
	"regexp"
	"strings"
)

// Pre-compiled regexes for markdownToMrkdwn conversions.
var (
	// Headers: # H1, ## H2, etc. — anchored at start of line.
	reHeader = regexp.MustCompile(`(?m)^#{1,6}\s+(.+)$`)

	// Bold: **text** — converted before italic.
	reBold = regexp.MustCompile(`\*\*(.+?)\*\*`)

	// Italic: *text* — single asterisks only.
	// Applied after bold markers have been replaced with placeholders.
	reItalic = regexp.MustCompile(`\*([^*\n]+?)\*`)

	// Strikethrough: ~~text~~ → ~text~
	reStrikethrough = regexp.MustCompile(`~~(.+?)~~`)

	// Links: [text](url) → <url|text>
	reLink = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)

	// Horizontal rules: ---, ***, or ___ alone on a line.
	reHRule = regexp.MustCompile(`(?m)^[ \t]*[-*_]{3,}[ \t]*$`)

	// Fenced code blocks: ``` ... ```
	reFencedCode = regexp.MustCompile("(?s)```.*?```")

	// Inline code spans: `code`
	reInlineCode = regexp.MustCompile("`[^`\n]+`")
)

// boldPlaceholder is a sentinel used during conversion so that italic processing
// does not match the single `*` delimiters produced by bold or header conversion.
const boldPlaceholder = "\x01BOLD\x01"

// markdownToMrkdwn converts standard Markdown to Slack mrkdwn format.
//
// Conversion rules applied:
//   - Bold **text** → *text*
//   - Italic *text* → _text_
//   - Strikethrough ~~text~~ → ~text~
//   - Links [text](url) → <url|text>
//   - Headers (# H1, ## H2, …) → *Header* (Slack has no native headers)
//   - Horizontal rules (---, ***) are removed
//
// Elements that Slack renders natively and are left unchanged:
//   - Backtick code spans and fenced code blocks
//   - Blockquotes (> text)
//   - Bullet lists (- item) and numbered lists
func markdownToMrkdwn(text string) string {
	// Extract code spans/blocks to protect them from conversion.
	var saved []string

	save := func(match string) string {
		idx := len(saved)
		saved = append(saved, match)
		return fmt.Sprintf("\x00%d\x00", idx)
	}

	// Fenced code blocks first (multi-line), then inline code.
	text = reFencedCode.ReplaceAllStringFunc(text, save)
	text = reInlineCode.ReplaceAllStringFunc(text, save)

	// Horizontal rules → empty line
	text = reHRule.ReplaceAllString(text, "")

	// Strikethrough: ~~text~~ → ~text~
	text = reStrikethrough.ReplaceAllString(text, "~${1}~")

	// Links: [text](url) → <url|text>
	text = reLink.ReplaceAllString(text, "<${2}|${1}>")

	// Bold: **text** → placeholder*text*placeholder so italic pass won't
	// re-match the surrounding single asterisks.
	text = reBold.ReplaceAllString(text, boldPlaceholder+"${1}"+boldPlaceholder)

	// Headers: # Heading → placeholder*Heading*placeholder
	text = reHeader.ReplaceAllString(text, boldPlaceholder+"${1}"+boldPlaceholder)

	// Italic: *text* → _text_  (safe now — all bold/header * are placeholders)
	text = reItalic.ReplaceAllString(text, "_${1}_")

	// Restore bold/header placeholders → Slack bold: *text*
	text = strings.ReplaceAll(text, boldPlaceholder, "*")

	// Restore saved code spans/blocks
	for i, block := range saved {
		text = strings.Replace(text, fmt.Sprintf("\x00%d\x00", i), block, 1)
	}

	return text
}
