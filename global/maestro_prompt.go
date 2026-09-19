// ClawEh
// License: MIT

package global

// MaestroPromptRule is the identity rule added to an agent's system prompt when
// Maestro is enabled for it. Kept short on purpose: it tells the model what
// Maestro is for and to read the orientation guide first; the guide itself
// (returned by maestro_start_here) explains how Maestro works.
const MaestroPromptRule = "**Maestro** - Maestro is an orchestration framework for large, repeatable, quality-controlled processes. " +
	"Use it for multi-step or repeatable work instead of doing everything in this conversation. " +
	"Call `maestro_start_here` before using any other Maestro tool."

// MaestroEntryTool is the Maestro tool kept visible to the in-loop model under
// progressive discovery, so the model can always find the orientation guide.
const MaestroEntryTool = "maestro_start_here"
