// ClawEh
// License: MIT

package mcpserver

import (
	"context"
	"net/http"
	"strings"

	"github.com/mark3labs/mcp-go/server"
)

// bearerCtxKey is the context key under which the /mcp endpoint stores the
// Authorization: Bearer token for a request, so the tool dispatch handler can
// read it the same way /internal reads the in-call session_token parameter.
type bearerCtxKey struct{}

// withBearerToken returns ctx carrying the supplied bearer token.
func withBearerToken(ctx context.Context, tok string) context.Context {
	return context.WithValue(ctx, bearerCtxKey{}, tok)
}

// bearerTokenFromContext returns the bearer token placed in ctx by the /mcp
// HTTPContextFunc, or "" when none is present.
func bearerTokenFromContext(ctx context.Context) string {
	s, _ := ctx.Value(bearerCtxKey{}).(string)
	return s
}

// extractBearer pulls the token out of an "Authorization: Bearer <token>"
// header. The scheme match is case-insensitive per RFC 7235; the token is
// returned verbatim. Returns "" when the header is absent or not a bearer.
func extractBearer(r *http.Request) string {
	const prefix = "bearer "
	h := r.Header.Get("Authorization")
	if len(h) < len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

// bearerContextFunc copies the request's bearer token into the MCP context for
// the /mcp endpoint, where the bearer authMode reads it during dispatch.
func bearerContextFunc(_ context.Context, r *http.Request) context.Context {
	// The base ctx supplied by mcp-go is request-scoped; attach the token to it.
	return withBearerToken(r.Context(), extractBearer(r))
}

// bearerAuthMiddleware rejects /mcp requests lacking a valid bearer token at the
// HTTP layer with a 401 (matching standard MCP client expectations), before they
// reach the streamable handler. A token that is present and resolvable in the
// shared store is accepted; per-call routing/ACL still runs in dispatch.
func bearerAuthMiddleware(store *sessionTokenStore, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := extractBearer(r)
		if tok == "" {
			unauthorized(w, "missing bearer token")
			return
		}
		if store == nil {
			unauthorized(w, "invalid bearer token")
			return
		}
		if _, ok := store.Resolve(tok); !ok {
			unauthorized(w, "invalid bearer token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func unauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="claw-mcp"`)
	http.Error(w, msg, http.StatusUnauthorized)
}

// newStreamable builds a streamable HTTP server for one endpoint. The bearer
// endpoint additionally installs the bearer HTTPContextFunc.
func newStreamable(srv *server.MCPServer, path string, bearer bool) *server.StreamableHTTPServer {
	opts := []server.StreamableHTTPOption{
		server.WithEndpointPath(path),
		server.WithStateLess(true),
	}
	if bearer {
		opts = append(opts, server.WithHTTPContextFunc(bearerContextFunc))
	}
	return server.NewStreamableHTTPServer(srv, opts...)
}
