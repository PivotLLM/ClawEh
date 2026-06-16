package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	cogmemstore "github.com/PivotLLM/ClawEh/pkg/cogmem/store"
)

// seedCogmemDB creates a .cogmem.db in the agent sessions dir with one active
// project domain holding a single fact memory, returning the store id (filename
// base) the API uses.
func seedCogmemDB(t *testing.T, configPath string) string {
	t.Helper()
	dir := sessionsTestDir(t, configPath)

	sessionKey := "agent:main:webui:direct:webui:mem-test"
	id := cogmemstore.SanitizeSessionKey(sessionKey)
	path := filepath.Join(dir, id+".cogmem.db")

	s, err := cogmemstore.Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	d, err := s.CreateDomain(ctx, s.DB(), cogmemstore.CreateDomainParams{
		Type:    cogmemstore.DomainProject,
		Name:    "Website Redesign",
		Status:  cogmemstore.StatusActive,
		Summary: "the redesign project",
	})
	if err != nil {
		t.Fatalf("CreateDomain() error = %v", err)
	}
	if _, err := s.AddMemory(ctx, s.DB(), cogmemstore.AddMemoryParams{
		DomainID:   d.ID,
		Type:       cogmemstore.TypeFact,
		Text:       "launch date is in the fall",
		Status:     cogmemstore.StatusActive,
		Confidence: 0.9,
		Source:     cogmemstore.SourceUserExplicit,
	}); err != nil {
		t.Fatalf("AddMemory() error = %v", err)
	}
	return id
}

func TestHandleListMemoryStores(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	id := seedCogmemDB(t, configPath)

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/memory", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Sessions []memoryStoreItem `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	found := false
	for _, s := range resp.Sessions {
		if s.ID == id {
			found = true
		}
	}
	if !found {
		t.Fatalf("store id %q not in list: %+v", id, resp.Sessions)
	}
}

func TestHandleGetMemoryStore(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	id := seedCogmemDB(t, configPath)

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/memory/"+id, nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var detail memoryDetailResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	// Open() auto-creates the always-on general domain, so there is at least
	// the project domain we seeded plus general.
	if detail.ActiveDomains < 1 {
		t.Fatalf("active_domains = %d, want >= 1", detail.ActiveDomains)
	}
	if detail.ActiveMemories != 1 {
		t.Fatalf("active_memories = %d, want 1", detail.ActiveMemories)
	}
	var project *memoryDomain
	for i := range detail.Domains {
		if detail.Domains[i].Type == "project" {
			project = &detail.Domains[i]
		}
	}
	if project == nil || len(project.Memories) != 1 {
		t.Fatalf("project domain shape unexpected: %+v", detail.Domains)
	}
	if project.Memories[0].Text != "launch date is in the fall" {
		t.Fatalf("memory text = %q", project.Memories[0].Text)
	}
}

func TestHandleGetMemoryStore_NotFound(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/memory/does-not-exist", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
