// ClawEh
// License: MIT

package agent

import (
	"errors"
	"fmt"
	"strings"

	"github.com/PivotLLM/ClawEh/agentreg"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/routing"
	"github.com/PivotLLM/ClawEh/tools"
	toolsagents "github.com/PivotLLM/ClawEh/tools/agents"
)

// errNotOwner is returned when an agent deletes a temporary agent it did not
// create through its AgentServices.
var errNotOwner = errors.New("temporary agent was not created by this agent")

// agentServices is the tools.AgentServices of one calling agent. The caller
// is looked up live on every call, so a reload's new models and allow list
// apply at once.
type agentServices struct {
	al       *AgentLoop
	callerID string
}

var _ tools.AgentServices = (*agentServices)(nil)

func newAgentServices(al *AgentLoop, callerID string) *agentServices {
	return &agentServices{al: al, callerID: routing.NormalizeAgentID(callerID)}
}

func (s *agentServices) caller() (*AgentInstance, error) {
	registry := s.al.GetRegistry()
	if registry == nil {
		return nil, errors.New("agent registry is not available")
	}
	a, ok := registry.Get(s.callerID)
	if !ok || a == nil {
		return nil, fmt.Errorf("%w: %s", agentreg.ErrNotFound, s.callerID)
	}
	return a, nil
}

// CanTarget is true only for an enabled config agent the caller may spawn
// (subagents.allow_agents); never for a temporary or unknown id, even when the
// allow list is "*".
func (s *agentServices) CanTarget(agentID string) bool {
	registry := s.al.GetRegistry()
	if registry == nil {
		return false
	}
	if _, ok := registry.GetConfigured(agentID); !ok {
		return false
	}
	return canSpawnSubagent(registry, s.callerID, agentID)
}

func (s *agentServices) AllowedModels() []string {
	a, err := s.caller()
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(a.Candidates))
	for _, c := range a.Candidates {
		name := c.Alias
		if name == "" {
			name = c.Model
		}
		names = append(names, name)
	}
	return names
}

func (s *agentServices) CreateClone(agentID string) (string, error) {
	if !s.CanTarget(agentID) {
		return "", fmt.Errorf("agent %q may not target agent %q (subagents.allow_agents)", s.callerID, agentID)
	}
	return s.al.GetRegistry().Create(config.AgentConfig{}, agentreg.CloneOf(agentID), agentreg.OwnedBy(s.callerID))
}

func (s *agentServices) CreateFresh(model string, opts ...tools.FreshOption) (string, error) {
	a, err := s.caller()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(model) == "" {
		return "", errors.New("a fresh agent needs a model")
	}
	matched, ok := toolsagents.MatchCandidate(a.Candidates, model)
	if !ok {
		return "", fmt.Errorf("model %q is not one of agent %q's models", model, s.callerID)
	}
	modelName := matched.Alias
	if modelName == "" {
		modelName = matched.Model
	}
	o := tools.NewFreshOptions(opts...)
	regOpts := []agentreg.Option{agentreg.OwnedBy(s.callerID)}
	if o.SystemPromptSet {
		regOpts = append(regOpts, agentreg.WithSystemPrompt(o.SystemPrompt))
	}
	if o.NoMemory {
		regOpts = append(regOpts, agentreg.WithoutMemory())
	}
	if o.SingleShot {
		regOpts = append(regOpts, agentreg.SingleShot())
	}
	return s.al.GetRegistry().Create(config.AgentConfig{
		Name:   o.Name,
		Models: []string{modelName},
	}, regOpts...)
}

// Delete deletes a temporary agent whose recorded owner (agentreg.Spec.Owner,
// saved across restarts) is the caller. An unknown id is agentreg.ErrNotFound.
func (s *agentServices) Delete(agentID string) error {
	registry := s.al.GetRegistry()
	info, ok := registry.Info(agentID)
	if !ok {
		return fmt.Errorf("%w: %s", agentreg.ErrNotFound, agentID)
	}
	if info.Spec.Owner == "" || info.Spec.Owner != s.callerID {
		return fmt.Errorf("%w: %s", errNotOwner, agentID)
	}
	return registry.Delete(info.Spec.ID)
}
