// ClawEh - Cognitive Memory
// License: MIT

package cogmem

// Compose defaults. These are levers exposed via the New(...) functional
// options (WithTopKDomains, etc.) and driven per-agent from memory.prompt
// config; the values here are the fallback defaults.
const (
	defaultTopKDomains   = 3
	defaultMaxChars      = 4000
	defaultMinConfidence = 0.65
	defaultPendingMax    = 8
)

// Pending-digest surfacing modes.
const (
	PendingSurfaceAsk        = "ask"         // show the digest so the agent can confirm
	PendingSurfaceExportOnly = "export_only" // keep out of the prompt; export only
)
