// ClawEh
// License: MIT

package global

import "testing"

func TestIsRefusal(t *testing.T) {
	cases := []struct {
		name         string
		finishReason string
		content      string
		want         bool
	}{
		{"finish_reason refusal", "refusal", "", true},
		{"finish_reason content_filter", "content_filter", "{}", true},
		{"finish_reason hyphen variant", "content-filter", "", true},
		{"finish_reason safety", "safety", "", true},
		{"trailing decline after partial json", "", `{"version":2,"state":{` + "\n\nI'm sorry, but I cannot assist with that request.", true},
		{"cannot help phrasing", "stop", "I can't help with that.", true},
		{"unable to assist", "", "I am unable to assist with this.", true},
		{"against guidelines", "", "That goes against my guidelines.", true},
		{"ordinary truncated json is not a refusal", "length", `{"version":2,"state":{"goals":[`, false},
		{"clean valid-ish content no markers", "stop", `{"version":2}`, false},
		{"empty", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsRefusal(tc.finishReason, tc.content); got != tc.want {
				t.Errorf("IsRefusal(%q, %q) = %v, want %v", tc.finishReason, tc.content, got, tc.want)
			}
		})
	}
}

func TestRefusalDetail(t *testing.T) {
	if got := RefusalDetail("refusal"); got != "content policy (finish_reason=refusal)" {
		t.Errorf("RefusalDetail(refusal) = %q", got)
	}
	if got := RefusalDetail("stop"); got != "content policy" {
		t.Errorf("RefusalDetail(stop) = %q", got)
	}
}
