package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/PivotLLM/ClawEh/providers"
	"github.com/PivotLLM/spawnllm/common"
)

// renderBillingError converts a provider-billing failure into a short,
// user-facing string suitable for posting back to chat. Returns "" if the
// error is not a billing failure — the caller falls back to the raw error
// rendering in that case.
//
// Sniffs the response body preview attached to each attempt for a top-up URL
// so the user sees actionable text ("Out of credits on X. Top up: <url>")
// instead of a stack trace.
func renderBillingError(err error) string {
	if err == nil {
		return ""
	}

	var summaries []billingSummary

	var exhausted *providers.FallbackExhaustedError
	if errors.As(err, &exhausted) {
		for _, a := range exhausted.Attempts {
			if a.Skipped || a.Reason != providers.FailoverBilling {
				continue
			}
			summaries = append(summaries, billingSummary{
				Provider: a.Provider,
				Model:    a.Model,
				URL:      sniffBillingURL(extractBodyPreview(a.Error)),
			})
		}
	} else {
		// Single-attempt path: an unwrapped FailoverError can also surface
		// here when only the primary was configured.
		var fe *providers.FailoverError
		if errors.As(err, &fe) && fe.Reason == providers.FailoverBilling {
			summaries = append(summaries, billingSummary{
				Provider: fe.Provider,
				Model:    fe.Model,
				URL:      sniffBillingURL(extractBodyPreview(fe.Wrapped)),
			})
		}
	}

	if len(summaries) == 0 {
		return ""
	}
	return formatBillingMessage(summaries)
}

type billingSummary struct {
	Provider string
	Model    string
	URL      string
}

func formatBillingMessage(items []billingSummary) string {
	if len(items) == 1 {
		s := items[0]
		out := fmt.Sprintf("Out of credits on %s/%s.", s.Provider, s.Model)
		if s.URL != "" {
			out += " Top up: " + s.URL
		}
		return out
	}
	var b strings.Builder
	b.WriteString("Out of credits on multiple providers:\n")
	for _, s := range items {
		b.WriteString("  • " + s.Provider + "/" + s.Model)
		if s.URL != "" {
			b.WriteString(" — " + s.URL)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func extractBodyPreview(err error) string {
	var hse *common.HTTPStatusError
	if errors.As(err, &hse) {
		return hse.BodyPreview
	}
	return ""
}

// sniffBillingURL pulls an actionable top-up URL out of a (possibly truncated)
// JSON error body, in decreasing order of specificity:
//
//  1. an explicit billing_url field — top level, under "error", or under
//     "error.metadata";
//  2. the first URL inside error.metadata.remedy_hint — OpenRouter sends no
//     billing_url at all, it sends "Add credits at <url>, or lower max_tokens…"
//     here, so without this a 402 from them renders with no link;
//  3. the first URL inside error.message — OpenRouter's key/limit page, the
//     next best pointer when there is no remedy hint.
//
// Falls back to a substring scan when JSON parsing fails: common.HandleErrorResponse
// truncates the body mid-document, which is exactly the case for the bodies we
// care about. A value cut off by that truncation is discarded rather than
// returned half-formed.
func sniffBillingURL(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}

	var top map[string]any
	if err := json.Unmarshal([]byte(body), &top); err == nil {
		if u := billingURLFromJSON(top); u != "" {
			return u
		}
	}

	// Truncated (or non-)JSON: scan the raw text in the same order.
	if u := valueAfterKey(body, "billing_url"); u != "" {
		return u
	}
	if u := firstURL(valueAfterKey(body, "remedy_hint")); u != "" {
		return u
	}
	return firstURL(valueAfterKey(body, "message"))
}

// billingURLFromJSON applies the sniff order to a fully-parsed body.
func billingURLFromJSON(top map[string]any) string {
	if u := stringField(top, "billing_url"); u != "" {
		return u
	}
	inner, ok := top["error"].(map[string]any)
	if !ok {
		return ""
	}
	if u := stringField(inner, "billing_url"); u != "" {
		return u
	}
	if meta, ok := inner["metadata"].(map[string]any); ok {
		if u := stringField(meta, "billing_url"); u != "" {
			return u
		}
		if u := firstURL(stringField(meta, "remedy_hint")); u != "" {
			return u
		}
	}
	return firstURL(stringField(inner, "message"))
}

// valueAfterKey returns the string value of "<key>" in a raw JSON body without
// parsing it — the body is often truncated mid-document, so json.Unmarshal fails
// on precisely the inputs that matter. Returns "" when the key is absent, its
// value is not a string, or the value runs past the end of the (truncated)
// body — a half-copied URL is worse than none.
func valueAfterKey(body, key string) string {
	i := strings.Index(body, `"`+key+`"`)
	if i < 0 {
		return ""
	}
	rest := strings.TrimLeft(body[i+len(key)+2:], " \t")
	if !strings.HasPrefix(rest, ":") {
		return ""
	}
	rest = strings.TrimLeft(rest[1:], " \t")
	if !strings.HasPrefix(rest, `"`) {
		return ""
	}
	rest = rest[1:]
	k := strings.Index(rest, `"`)
	if k < 0 {
		return "" // truncated mid-value
	}
	return rest[:k]
}

// firstURL returns the first http(s) URL in s, trimmed of the surrounding
// prose's punctuation — OpenRouter's hint reads "Add credits at <url>, or lower
// max_tokens…", so the comma must not become part of the link.
func firstURL(s string) string {
	i := strings.Index(s, "https://")
	if i < 0 {
		i = strings.Index(s, "http://")
	}
	if i < 0 {
		return ""
	}
	u := s[i:]
	if j := strings.IndexAny(u, " \t\n\"'<>"); j >= 0 {
		u = u[:j]
	}
	return strings.TrimRight(u, ".,;:)]}")
}

func stringField(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
