// ClawEh
// License: MIT

package global

import "strings"

// This file is the single place to find and update LLM content-refusal
// patterns. It is transport-neutral (stdlib only), so it can be vendored or
// imported by other software that needs to recognise the same refusals.

// RefusalFinishReasons are provider stop reasons that unambiguously signal the
// model declined to produce output on content-policy grounds. Edit this list to
// recognise additional provider-specific reasons.
var RefusalFinishReasons = []string{
	"refusal",
	"content_filter",
	"content-filter",
	"safety",
}

// RefusalMarkers are lowercase substrings that, when present in a response that
// otherwise FAILED to produce the expected structured output, indicate the model
// refused rather than merely returning malformed content. They are deliberately
// specific phrasings of a decline so ordinary output that happens to discuss a
// refusal is not flagged — callers should only consult these on an already-failed
// parse. Edit this list to add or refine refusal phrasings.
var RefusalMarkers = []string{
	"i'm sorry, but i cannot",
	"i'm sorry, but i can't",
	"i am sorry, but i cannot",
	"i am sorry, but i can't",
	"i cannot assist with that",
	"i can't assist with that",
	"i cannot help with that",
	"i can't help with that",
	"i'm unable to assist",
	"i am unable to assist",
	"i won't be able to help",
	"i will not be able to help",
	"i must decline",
	"i cannot comply",
	"i can't comply",
	"against my guidelines",
	"i cannot continue with this",
	"i can't continue with this",
}

// IsRefusal reports whether an LLM response that did not yield the expected
// output represents a content refusal. It is only meaningful for a response that
// already failed parsing/validation; well-formed output is never a refusal.
//
// A refusal is recognised either from the provider's finishReason (the reliable
// path when the provider sets one) or, as a backstop for providers that do not
// (many OpenAI-compatible gateways, Bedrock), from a RefusalMarker in content —
// which catches the common case of a model emitting partial output and then
// appending "I'm sorry, but I cannot…", truncating the result.
func IsRefusal(finishReason, content string) bool {
	if fr := strings.ToLower(strings.TrimSpace(finishReason)); fr != "" {
		for _, r := range RefusalFinishReasons {
			if fr == r {
				return true
			}
		}
	}
	lc := strings.ToLower(content)
	for _, marker := range RefusalMarkers {
		if strings.Contains(lc, marker) {
			return true
		}
	}
	return false
}

// RefusalDetail returns a short, human-readable detail string describing why a
// response was classified as a refusal.
func RefusalDetail(finishReason string) string {
	if fr := strings.ToLower(strings.TrimSpace(finishReason)); fr != "" {
		for _, r := range RefusalFinishReasons {
			if fr == r {
				return "content policy (finish_reason=" + fr + ")"
			}
		}
	}
	return "content policy"
}
