package device

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/gatewayproto"
)

// newAuthTestServer builds a Server with the given shared secrets and no HTTP
// listener — these tests exercise authorizeGateway directly.
func newAuthTestServer(t *testing.T, shared, word string) *Server {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return NewServer(store, ServerOptions{SharedToken: shared, WordToken: word})
}

func connectWith(token string) *gatewayproto.ConnectParams {
	return &gatewayproto.ConnectParams{Auth: &gatewayproto.ConnectAuth{Token: token}}
}

// TestAuthorizeGateway_SharedSecrets covers the shared-token branch. The
// wrong-length cases matter beyond the obvious reject: authorizeGateway hashes
// both operands to a fixed 32 bytes before comparing, so a length mismatch must
// take the same path as a content mismatch rather than short-circuiting.
func TestAuthorizeGateway_SharedSecrets(t *testing.T) {
	const shared = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	const word = "anchor-velvet-puzzle-ranger-cobalt"

	tests := []struct {
		name  string
		token string
		want  bool
	}{
		{"shared token matches", shared, true},
		{"word token matches", word, true},
		{"same length, wrong content", strings.Repeat("f", len(shared)), false},
		{"shorter than both secrets", "short", false},
		{"longer than both secrets", strings.Repeat("a", len(shared)+64), false},
		{"prefix of the shared token", shared[:len(shared)-1], false},
		{"shared token plus a byte", shared + "0", false},
		{"empty token", "", false},
	}

	srv := newAuthTestServer(t, shared, word)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := srv.authorizeGateway(context.Background(), connectWith(tc.token)); got != tc.want {
				t.Fatalf("authorizeGateway(%q) = %v, want %v", tc.token, got, tc.want)
			}
		})
	}
}

// TestAuthorizeGateway_UnsetSecretsFailClosed pins the fail-closed property: no
// combination of unset shared secrets grants access, whatever the client
// presents. This is the property docs/security-stability-review.md #11 records
// as closed, so it is worth holding still independently of how the comparison
// is implemented.
func TestAuthorizeGateway_UnsetSecretsFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name         string
		shared, word string
		token        string
	}{
		{"both unset, empty token", "", "", ""},
		{"both unset, non-empty token", "", "", "anything"},
		{"shared set, word unset, empty token", "abc", "", ""},
		{"shared unset, word set, empty token", "", "abc", ""},
		{"both unset, whitespace token", "", "", " "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newAuthTestServer(t, tc.shared, tc.word)
			if srv.authorizeGateway(context.Background(), connectWith(tc.token)) {
				t.Fatalf("authorizeGateway granted access with shared=%q word=%q token=%q",
					tc.shared, tc.word, tc.token)
			}
		})
	}
}

// TestAuthorizeGateway_NilAuth guards the nil-params path.
func TestAuthorizeGateway_NilAuth(t *testing.T) {
	srv := newAuthTestServer(t, "shared", "word")
	if srv.authorizeGateway(context.Background(), &gatewayproto.ConnectParams{}) {
		t.Fatal("authorizeGateway granted access with nil Auth")
	}
}
