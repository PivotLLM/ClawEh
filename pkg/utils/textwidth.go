// Package utils — display width.
//
// DisplayWidth approximates the monospace column width of a string the way
// terminals and chat clients (Slack/Telegram code blocks) render it. It exists
// because utf8.RuneCountInString miscounts emoji: "☀️" is U+2600 + the
// variation selector U+FE0F (2 runes) while "🌙" is a single rune U+1F319, yet
// both render at the same width. Counting runes makes a table column with mixed
// emoji misalign — the single-rune emoji cell measures one short and gets an
// extra padding space. Here emoji and East Asian wide runes count as 2, while
// variation selectors, zero-width joiners, and combining marks count as 0.

package utils

import "unicode"

// DisplayWidth returns the monospace render width of s.
func DisplayWidth(s string) int {
	w := 0
	for _, r := range s {
		w += RuneWidth(r)
	}
	return w
}

// RuneWidth returns the monospace render width of a single rune: 0 for
// zero-width modifiers (variation selectors, ZWJ, combining/format marks), 2 for
// emoji and East Asian wide/fullwidth runes, and 1 otherwise.
func RuneWidth(r rune) int {
	switch {
	case r == 0x200D, // zero-width joiner
		(r >= 0xFE00 && r <= 0xFE0F),   // variation selectors
		(r >= 0xE0100 && r <= 0xE01EF), // variation selectors supplement
		unicode.Is(unicode.Mn, r),      // nonspacing combining marks
		unicode.Is(unicode.Me, r),      // enclosing combining marks
		unicode.Is(unicode.Cf, r):      // format characters (zero-width)
		return 0
	case isWide(r):
		return 2
	default:
		return 1
	}
}

// isWide reports whether r renders as two monospace cells: emoji (the common
// blocks) and East Asian wide/fullwidth characters.
func isWide(r rune) bool {
	switch {
	// Emoji blocks.
	case r >= 0x1F000 && r <= 0x1FAFF, // pictographs, emoticons, transport, symbols & pictographs ext-A
		r >= 0x2600 && r <= 0x26FF,   // miscellaneous symbols (☀ ☁ ☂ …)
		r >= 0x2700 && r <= 0x27BF,   // dingbats (✂ ✅ …)
		r >= 0x2B00 && r <= 0x2BFF,   // misc symbols & arrows (⭐ …)
		r >= 0x1F1E6 && r <= 0x1F1FF, // regional indicators (flag halves)
		r == 0x231A || r == 0x231B,   // ⌚ ⏳
		r >= 0x23E9 && r <= 0x23FA,   // media-control emoji in Misc Technical
		r == 0x303D || r == 0x3030:   // CJK part-alternation / wavy dash
		return true
	// East Asian wide / fullwidth.
	case r >= 0x1100 && r <= 0x115F, // Hangul Jamo
		r >= 0x2E80 && r <= 0x303E,   // CJK radicals, Kangxi, symbols
		r >= 0x3041 && r <= 0x33FF,   // Hiragana … CJK compatibility
		r >= 0x3400 && r <= 0x4DBF,   // CJK ext A
		r >= 0x4E00 && r <= 0x9FFF,   // CJK unified ideographs
		r >= 0xA000 && r <= 0xA4CF,   // Yi
		r >= 0xAC00 && r <= 0xD7A3,   // Hangul syllables
		r >= 0xF900 && r <= 0xFAFF,   // CJK compatibility ideographs
		r >= 0xFE30 && r <= 0xFE4F,   // CJK compatibility forms
		r >= 0xFF00 && r <= 0xFF60,   // fullwidth forms
		r >= 0xFFE0 && r <= 0xFFE6,   // fullwidth signs
		r >= 0x20000 && r <= 0x3FFFD: // CJK ext B+ (supplementary ideographic planes)
		return true
	}
	return false
}
