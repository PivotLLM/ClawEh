// ClawEh
// License: MIT

package tools

import "strings"

// TempPurposeForum is ToolDeps.TempPurpose of a temporary agent a forum
// created (a clone or fresh participant).
const TempPurposeForum = "forum"

// FreshOption configures AgentServices.CreateFresh: WithName,
// WithSystemPrompt, WithoutMemory, SingleShot or WithCharacterCard.
type FreshOption interface{ applyFresh(o *FreshOptions) }

// CloneOption configures AgentServices.CreateClone: WithCharacterCard.
type CloneOption interface{ applyClone(o *CloneOptions) }

// freshOnly is an option only CreateFresh takes.
type freshOnly func(*FreshOptions)

func (f freshOnly) applyFresh(o *FreshOptions) { f(o) }

// cardOption is WithCharacterCard, an option of both kinds.
type cardOption string

func (c cardOption) applyFresh(o *FreshOptions) { o.CharacterCard = string(c) }
func (c cardOption) applyClone(o *CloneOptions) { o.CharacterCard = string(c) }

// CloneOptions are the settings of a clone, as the CloneOption values set
// them. Implementations of AgentServices read them with NewCloneOptions.
type CloneOptions struct {
	// CharacterCard is the id of a card in the library the clone runs with
	// instead of its source's; empty keeps the source's.
	CharacterCard string
}

// NewCloneOptions applies opts in order.
func NewCloneOptions(opts ...CloneOption) CloneOptions {
	var o CloneOptions
	for _, opt := range opts {
		opt.applyClone(&o)
	}
	return o
}

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
	// CharacterCard is the id of a card in the library rendered as the
	// agent's prompt, before any SystemPrompt text; empty for none.
	CharacterCard string
}

// NewFreshOptions applies opts in order.
func NewFreshOptions(opts ...FreshOption) FreshOptions {
	var o FreshOptions
	for _, opt := range opts {
		opt.applyFresh(&o)
	}
	return o
}

// WithName sets the fresh agent's display name.
func WithName(name string) FreshOption { return freshOnly(func(o *FreshOptions) { o.Name = name }) }

// WithSystemPrompt sets the fresh agent's system prompt, replacing the
// default; with a character card it follows the card as instructions. A
// blank text is refused by CreateFresh.
func WithSystemPrompt(text string) FreshOption {
	return freshOnly(func(o *FreshOptions) { o.SystemPrompt, o.SystemPromptSet = text, true })
}

// WithoutMemory gives the fresh agent no cognitive memory; it keeps its
// conversation.
func WithoutMemory() FreshOption { return freshOnly(func(o *FreshOptions) { o.NoMemory = true }) }

// SingleShot makes the fresh agent keep nothing: no memory, and a blank
// context on every message.
func SingleShot() FreshOption { return freshOnly(func(o *FreshOptions) { o.SingleShot = true }) }

// WithCharacterCard gives the temporary agent a character card from the
// library: a fresh agent's prompt is the rendered card (followed by the
// WithSystemPrompt text, when given), a clone runs with it instead of its
// source's card. A card that does not exist is refused by name.
func WithCharacterCard(id string) cardOption { return cardOption(strings.TrimSpace(id)) }

// AgentServices lets a tool manage temporary agents on behalf of the agent it
// was built for (the caller). Every check is made against that caller.
type AgentServices interface {
	// CreateClone creates a temporary clone of agentID, which the caller must
	// be allowed to target (CanTarget). It returns the clone's id.
	CreateClone(agentID string, opts ...CloneOption) (string, error)
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
