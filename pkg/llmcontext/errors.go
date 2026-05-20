// ClawEh
// License: MIT

package llmcontext

import "errors"

// Sentinel errors returned by Compress(), ForceCompress(), and Compact().
var (
	// ErrCompressionNotNeeded is returned when the session is below all
	// compression thresholds and no action was taken.
	ErrCompressionNotNeeded = errors.New("llmcontext: compression not needed")

	// ErrCompressionFailed is returned when all LLM clients in the compression
	// chain failed, when Save() fails, or when ForceCompress cannot fit the
	// current turn group within the context window.
	ErrCompressionFailed = errors.New("llmcontext: compression failed")

	// ErrCompressionPartial is returned when compression ran but the result
	// is still at or above the safety threshold.
	ErrCompressionPartial = errors.New("llmcontext: compression partial: still over safety threshold")
)
