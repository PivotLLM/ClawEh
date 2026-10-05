// ClawEh
// License: MIT

package tools

// TempPurposeForum is ToolDeps.TempPurpose of a temporary agent a forum
// created (a clone or fresh participant).
const TempPurposeForum = "forum"

// FreshOption configures AgentServices.CreateFresh.
type FreshOption func(*FreshOptions)

// FreshOptions are the settings of a fresh temporary agent (one that is not a
// clone), as the FreshOption functions set them. Implementations of
// AgentServices read them with NewFreshOptions.
type FreshOptions struct {
	// Name is the display name; empty for none.
	Name string
	// SystemPrompt is the agent's whole system prompt, when SystemPromptSet;
	// otherwise the default (agentreg.DefaultSystemPrompt) applies.
	SystemPrompt string
	// SystemPromptSet records that WithSystemPrompt was given. A blank prompt
	// given explicitly is refused, never taken for the default.
	SystemPromptSet bool
	// NoMemory: the agent keeps its conversation but has no cognitive memory.
	NoMemory bool
	// SingleShot: no memory, and every turn sees only the system prompt and
	// the new message.
	SingleShot bool
}

// NewFreshOptions applies opts in order.
func NewFreshOptions(opts ...FreshOption) FreshOptions {
	var o FreshOptions
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// WithName sets the fresh agent's display name.
func WithName(name string) FreshOption { return func(o *FreshOptions) { o.Name = name } }

// WithSystemPrompt sets the fresh agent's whole system prompt, replacing the
// default. A blank text is refused by CreateFresh.
func WithSystemPrompt(text string) FreshOption {
	return func(o *FreshOptions) { o.SystemPrompt, o.SystemPromptSet = text, true }
}

// WithoutMemory gives the fresh agent no cognitive memory; it keeps its
// conversation.
func WithoutMemory() FreshOption { return func(o *FreshOptions) { o.NoMemory = true } }

// SingleShot makes the fresh agent keep nothing: no memory, and a blank
// context on every message.
func SingleShot() FreshOption { return func(o *FreshOptions) { o.SingleShot = true } }

// AgentServices lets a tool manage temporary agents on behalf of the agent it
// was built for (the caller). Every check is made against that caller.
type AgentServices interface {
	// CreateClone creates a temporary clone of agentID, which the caller must
	// be allowed to target (CanTarget). It returns the clone's id.
	CreateClone(agentID string) (string, error)
	// CreateFresh creates a fresh temporary agent on model, which must be one
	// of AllowedModels, and returns its id. By default the agent keeps its
	// conversation and has cognitive memory; it never has tools, workspace
	// prompt files or skills.
	CreateFresh(model string, opts ...FreshOption) (id string, err error)
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
