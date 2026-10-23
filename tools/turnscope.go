// ClawEh
// License: MIT

package tools

import (
	"context"
	"slices"
)

// Turn-scope values a turn hands to everything it starts: the tools it calls,
// the sub-agents it spawns, the agents it asks, and the turns its background
// results re-enter. They are carried on the context in process, on bus
// metadata between turns, and on the session token for a CLI agent's MCP
// calls.

type askChainKey struct{}

// WithAskChain returns ctx carrying chain: the agents waiting for a reply in
// the exchange the running turn belongs to, ending with the running agent.
func WithAskChain(ctx context.Context, chain []string) context.Context {
	return context.WithValue(ctx, askChainKey{}, chain)
}

// AskChain returns the ask chain on ctx (nil when none).
func AskChain(ctx context.Context) []string {
	if chain, ok := ctx.Value(askChainKey{}).([]string); ok {
		return chain
	}
	return nil
}

// WithAskChainAgent returns ctx with agentID appended to its ask chain (once).
func WithAskChainAgent(ctx context.Context, agentID string) context.Context {
	chain := AskChain(ctx)
	if agentID == "" || slices.Contains(chain, agentID) {
		return ctx
	}
	return WithAskChain(ctx, append(slices.Clone(chain), agentID))
}
