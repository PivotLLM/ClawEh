package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/PivotLLM/ClawEh/pkg/msgtoken"
)

// fakeMessageTokenLoop is an in-memory stand-in for the AgentLoop's named-token
// store, sufficient to exercise the API handlers without a running gateway.
type fakeMessageTokenLoop struct {
	mu     sync.Mutex
	tokens map[string][]msgtoken.NamedToken
	hits   map[string]int // tokenID -> hits-in-window, for quota status
	nextID int
}

func newFakeMessageTokenLoop() *fakeMessageTokenLoop {
	return &fakeMessageTokenLoop{
		tokens: map[string][]msgtoken.NamedToken{},
		hits:   map[string]int{},
	}
}

func (f *fakeMessageTokenLoop) ListMessageTokens(agentID string) []msgtoken.NamedToken {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]msgtoken.NamedToken(nil), f.tokens[agentID]...)
}

func (f *fakeMessageTokenLoop) CreateMessageToken(agentID, name string) (msgtoken.NamedToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	nt := msgtoken.NamedToken{
		ID:          fmt.Sprintf("id%d", f.nextID),
		Name:        name,
		Token:       fmt.Sprintf("tok%d", f.nextID),
		CreatedAtMS: 1000,
	}
	f.tokens[agentID] = append(f.tokens[agentID], nt)
	return nt, nil
}

func (f *fakeMessageTokenLoop) DeleteMessageToken(agentID, id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	list := f.tokens[agentID]
	for i := range list {
		if list[i].ID == id {
			f.tokens[agentID] = append(list[:i], list[i+1:]...)
			return true
		}
	}
	return false
}

func (f *fakeMessageTokenLoop) MessageTokenQuota(agentID string) []msgtoken.TokenQuota {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]msgtoken.TokenQuota, 0, len(f.tokens[agentID]))
	for _, t := range f.tokens[agentID] {
		out = append(out, msgtoken.TokenQuota{
			ID:           t.ID,
			Name:         t.Name,
			RatePerMin:   t.EffectiveRatePerMin(),
			BlockMinutes: t.EffectiveBlockMinutes(),
			HitsInWindow: f.hits[t.ID],
		})
	}
	return out
}

func (f *fakeMessageTokenLoop) UpdateMessageToken(agentID, id string, ratePerMin, blockMinutes int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	list := f.tokens[agentID]
	for i := range list {
		if list[i].ID == id {
			list[i].RatePerMin = ratePerMin
			list[i].BlockMinutes = blockMinutes
			return true
		}
	}
	return false
}

func newMessageTokenTestHandler(t *testing.T) (*Handler, *http.ServeMux, *fakeMessageTokenLoop, func()) {
	t.Helper()
	configPath, cleanup := setupTestEnv(t)
	h := NewHandler(configPath)
	loop := newFakeMessageTokenLoop()
	h.SetMessageTokenLoop(loop)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return h, mux, loop, cleanup
}

func TestMessageTokens_ListEmptyIncludesEndpointBase(t *testing.T) {
	_, mux, _, cleanup := newMessageTokenTestHandler(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/agents/main/message-tokens", nil)
	req.Host = "gw.example:18790"
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Tokens       []messageTokenView `json:"tokens"`
		EndpointBase string             `json:"endpoint_base"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Tokens) != 0 {
		t.Errorf("tokens = %v, want empty", out.Tokens)
	}
	if out.EndpointBase != "http://gw.example:18790/api/message/" {
		t.Errorf("endpoint_base = %q", out.EndpointBase)
	}
}

func TestMessageTokens_CreateThenList(t *testing.T) {
	_, mux, _, cleanup := newMessageTokenTestHandler(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/agents/main/message-tokens",
		strings.NewReader(`{"name":"gps"}`))
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	var created messageTokenView
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	if created.Name != "gps" || created.Token == "" || created.ID == "" {
		t.Fatalf("created token incomplete: %+v", created)
	}

	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/api/agents/main/message-tokens", nil))
	var out struct {
		Tokens []messageTokenView `json:"tokens"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(out.Tokens) != 1 || out.Tokens[0].ID != created.ID {
		t.Fatalf("list = %+v, want the created token", out.Tokens)
	}
}

func TestMessageTokens_Delete(t *testing.T) {
	_, mux, loop, cleanup := newMessageTokenTestHandler(t)
	defer cleanup()
	tok, _ := loop.CreateMessageToken("main", "x")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete,
		"/api/agents/main/message-tokens/"+tok.ID, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if len(loop.ListMessageTokens("main")) != 0 {
		t.Errorf("token not removed from store")
	}
}

func TestMessageTokens_UnknownAgent404(t *testing.T) {
	_, mux, _, cleanup := newMessageTokenTestHandler(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents/ghost/message-tokens", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for unknown agent; body=%s", rec.Code, rec.Body.String())
	}
}

func TestMessageTokens_DeleteUnknownToken404(t *testing.T) {
	_, mux, _, cleanup := newMessageTokenTestHandler(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete,
		"/api/agents/main/message-tokens/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for unknown token; body=%s", rec.Code, rec.Body.String())
	}
}

// TestMessageTokens_ListIncludesQuotaStatus verifies the list handler zips the
// config list with the quota snapshot: default rate is surfaced and the live
// hits-in-window is carried through.
func TestMessageTokens_ListIncludesQuotaStatus(t *testing.T) {
	_, mux, loop, cleanup := newMessageTokenTestHandler(t)
	defer cleanup()
	tok, _ := loop.CreateMessageToken("main", "gps")
	loop.hits[tok.ID] = 4

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents/main/message-tokens", nil))
	var out struct {
		Tokens []messageTokenView `json:"tokens"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Tokens) != 1 {
		t.Fatalf("tokens = %+v, want 1", out.Tokens)
	}
	v := out.Tokens[0]
	if v.RatePerMin != msgtoken.DefaultRatePerMin || v.BlockMinutes != msgtoken.DefaultBlockMinutes {
		t.Errorf("defaults not surfaced: rate=%d block=%d", v.RatePerMin, v.BlockMinutes)
	}
	if v.HitsInWindow != 4 {
		t.Errorf("HitsInWindow = %d, want 4", v.HitsInWindow)
	}
}

func TestMessageTokens_UpdateConfig(t *testing.T) {
	_, mux, loop, cleanup := newMessageTokenTestHandler(t)
	defer cleanup()
	tok, _ := loop.CreateMessageToken("main", "gps")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch,
		"/api/agents/main/message-tokens/"+tok.ID,
		strings.NewReader(`{"rate_per_min":10,"block_minutes":5}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("patch status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	got := loop.ListMessageTokens("main")[0]
	if got.RatePerMin != 10 || got.BlockMinutes != 5 {
		t.Errorf("config not applied: %+v", got)
	}
}

func TestMessageTokens_UpdateUnknownToken404(t *testing.T) {
	_, mux, _, cleanup := newMessageTokenTestHandler(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch,
		"/api/agents/main/message-tokens/nope",
		strings.NewReader(`{"rate_per_min":10,"block_minutes":5}`)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}
