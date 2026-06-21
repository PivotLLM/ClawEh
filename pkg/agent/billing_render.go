package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/PivotLLM/ClawEh/pkg/providers"
	"github.com/PivotLLM/spawnllm/common"
)

// renderBillingError converts a provider-billing failure into a short,
// user-facing string suitable for posting back to chat. Returns "" if the
// error is not a billing failure — the caller falls back to the raw error
// rendering in that case.
//
// Sniffs the response body preview attached to each attempt for billing_url
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

// sniffBillingURL pulls billing_url out of a (possibly truncated) JSON body.
// Returns "" when empty, not JSON, or no field present. Falls back to a
// substring scan when JSON parsing fails — common.HandleErrorResponse may
// have truncated the body mid-document.
func sniffBillingURL(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}

	var top map[string]any
	if err := json.Unmarshal([]byte(body), &top); err == nil {
		if u := stringField(top, "billing_url"); u != "" {
			return u
		}
		if inner, ok := top["error"].(map[string]any); ok {
			if u := stringField(inner, "billing_url"); u != "" {
				return u
			}
		}
	}

	const key = `"billing_url"`
	if i := strings.Index(body, key); i >= 0 {
		rest := body[i+len(key):]
		if j := strings.Index(rest, `"`); j >= 0 {
			rest = rest[j+1:]
			if k := strings.Index(rest, `"`); k > 0 {
				return rest[:k]
			}
		}
	}
	return ""
}

func stringField(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
