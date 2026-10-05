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

type remoteOriginKey struct{}

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

// WithRemoteOrigin marks ctx as belonging to work that started with a message
// from a remote chat (any non-internal channel). The mark is never removed:
// an ask, a sub-agent or a background result started from such work keeps it,
// so tools restricted to local use (shell_exec) refuse it however many hops
// away from the chat it runs.
func WithRemoteOrigin(ctx context.Context) context.Context {
	if RemoteOrigin(ctx) {
		return ctx
	}
	return context.WithValue(ctx, remoteOriginKey{}, true)
}

// RemoteOrigin reports whether ctx carries the remote-origin mark.
func RemoteOrigin(ctx context.Context) bool {
	v, ok := ctx.Value(remoteOriginKey{}).(bool)
	return ok && v
}
