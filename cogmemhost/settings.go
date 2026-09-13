// ClawEh - Cognitive Memory
// License: MIT

package cogmemhost

import (
	"github.com/PivotLLM/cogmem"

	"github.com/PivotLLM/ClawEh/config"
)

// Settings maps an agent's effective memory config onto cogmem's settings.
// Retention windows are resolved here (0 means the documented default,
// negative means forever), so cogmem only ever sees a concrete number of days
// with 0 meaning keep forever.
func Settings(mem config.MemoryConfig) cogmem.Settings {
	return cogmem.Settings{
		Prompt: cogmem.PromptSettings{
			TopKDomains:       mem.Prompt.TopKDomains,
			MaxChars:          mem.Prompt.MaxChars,
			MinConfidence:     mem.Prompt.MinConfidence,
			IncludeDebugTrace: mem.Prompt.IncludeDebugTrace,
			FileMaxBytes:      mem.Prompt.FileMaxBytes,
			FileTotalMaxBytes: mem.Prompt.FileTotalMaxBytes,
		},
		Consolidation: cogmem.ConsolidationSettings{
			EveryNMessages:   mem.Consolidation.EveryNMessages,
			IdleMinutes:      mem.Consolidation.IdleMinutes,
			Nightly:          mem.Consolidation.Nightly,
			NightlyAt:        mem.Consolidation.NightlyAt,
			ProposeDomains:   mem.Consolidation.ProposeDomains,
			AutoPromote:      mem.Consolidation.AutoPromote,
			DebugDump:        mem.Consolidation.DebugDump,
			MaxBatchMessages: mem.Consolidation.MaxBatchMessages,
			MaxInputTokens:   mem.Consolidation.MaxInputTokens,
			PerMessageChars:  mem.Consolidation.PerMessageChars,
		},
		Retention: cogmem.RetentionSettings{
			EventDays:   mem.Retention.EffectiveEventDays(),
			RetiredDays: mem.Retention.EffectiveRetiredDays(),
		},
	}
}
