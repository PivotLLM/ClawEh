// ClawEh - Cognitive Memory
// License: MIT

package cogmemhost

import (
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

// The retention defaults live in ClawEh's config (0 = the documented default,
// -1 = forever); cogmem must receive them resolved.
func TestSettings_ResolvesRetention(t *testing.T) {
	var mem config.MemoryConfig
	mem.Prompt.TopKDomains = 5
	mem.Consolidation.EveryNMessages = 7
	got := Settings(mem)
	if got.Retention.EventDays != config.DefaultEventRetentionDays || got.Retention.RetiredDays != config.DefaultRetiredRetentionDays {
		t.Fatalf("unset retention resolved to %+v", got.Retention)
	}
	mem.Retention.EventDays = -1
	if Settings(mem).Retention.EventDays != 0 {
		t.Fatal("-1 (forever) must map to 0 for cogmem")
	}
	if got.Prompt.TopKDomains != 5 || got.Consolidation.EveryNMessages != 7 {
		t.Fatalf("plain fields not carried: %+v", got)
	}
}
