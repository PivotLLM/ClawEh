// ClawEh
// License: MIT

package tools

// FreshAgentSpec describes a fresh temporary agent (one that is not a clone).
type FreshAgentSpec struct {
	Name   string // display name; optional
	Model  string // model_name of a configured model; must be one of AllowedModels
	Cogmem bool   // give the agent its own cognitive memory
}

// AgentServices lets a tool manage temporary agents on behalf of the agent it
// was built for (the caller). Every check is made against that caller.
type AgentServices interface {
	// CreateClone creates a temporary clone of agentID, which the caller must
	// be allowed to target (CanTarget). It returns the clone's id.
	CreateClone(agentID string) (string, error)
	// CreateFresh creates a fresh temporary agent from spec and returns its id.
	CreateFresh(spec FreshAgentSpec) (string, error)
	// Delete deletes a temporary agent this caller created (the owner is
	// recorded with the agent, so this holds across restarts).
	Delete(agentID string) error
	// AllowedModels lists the models the caller may use, in its fallback order
	// (the model_name of each of its candidates).
	AllowedModels() []string
	// CanTarget reports whether the caller may address agentID (the
	// subagents.allow_agents check).
	CanTarget(agentID string) bool
}
