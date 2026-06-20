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

func TestSortMemoryStores(t *testing.T) {
	items := []memoryStoreItem{
		{ID: "1", Agent: "wendy", Updated: "2026-06-17T10:00:00Z"},
		{ID: "2", Agent: "Alice", Updated: "2026-06-17T09:00:00Z"},
		{ID: "3", Agent: "bob", Updated: "2026-06-17T08:00:00Z"},
		{ID: "4", Agent: "alice", Updated: "2026-06-17T12:00:00Z"}, // newer alice
	}
	sortMemoryStores(items)

	// Agents alphabetical (case-insensitive): alice, alice, bob, wendy.
	gotAgents := []string{items[0].Agent, items[1].Agent, items[2].Agent, items[3].Agent}
	wantAgents := []string{"alice", "Alice", "bob", "wendy"} // newer alice first within the group
	for i := range wantAgents {
		if gotAgents[i] != wantAgents[i] {
			t.Fatalf("order[%d] agent = %q, want %q (full: %v)", i, gotAgents[i], wantAgents[i], gotAgents)
		}
	}
	// Within the alice group, the most-recently-updated comes first.
	if items[0].ID != "4" {
		t.Fatalf("within-agent tiebreak wrong: first alice id = %q, want 4 (newer)", items[0].ID)
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
	// Open() auto-seeds the sticky General domain, so there is at least the
	// (non-sticky) topic domain we seeded plus General.
	if detail.ActiveDomains < 1 {
		t.Fatalf("active_domains = %d, want >= 1", detail.ActiveDomains)
	}
	if detail.ActiveMemories != 1 {
		t.Fatalf("active_memories = %d, want 1", detail.ActiveMemories)
	}
	var project *memoryDomain
	for i := range detail.Domains {
		if detail.Domains[i].Name == "Website Redesign" {
			project = &detail.Domains[i]
		}
	}
	if project == nil || len(project.Memories) != 1 {
		t.Fatalf("project domain shape unexpected: %+v", detail.Domains)
	}
	if project.Sticky {
		t.Fatalf("topic domain should not be sticky")
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

func TestHandleDeleteMemoryAndDomain(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	id := seedCogmemDB(t, configPath)
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	getDetail := func() memoryDetailResponse {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/memory/"+id, nil))
		var d memoryDetailResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return d
	}

	// Find the seeded topic domain + its memory.
	var domainID, memoryID string
	for _, dm := range getDetail().Domains {
		if dm.Name == "Website Redesign" {
			domainID = dm.ID
			if len(dm.Memories) > 0 {
				memoryID = dm.Memories[0].ID
			}
		}
	}
	if domainID == "" || memoryID == "" {
		t.Fatalf("seeded domain/memory not found")
	}

	// Delete the memory.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/memory/"+id+"/memories/"+memoryID, nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete memory status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := getDetail().ActiveMemories; got != 0 {
		t.Fatalf("active memories after delete = %d, want 0", got)
	}

	// Delete the domain.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/memory/"+id+"/domains/"+domainID, nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete domain status = %d, body=%s", rec.Code, rec.Body.String())
	}
	for _, dm := range getDetail().Domains {
		if dm.ID == domainID {
			t.Fatalf("domain survived delete")
		}
	}

	// Deleting a missing memory returns 404.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/memory/"+id+"/memories/hMISSING", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("delete missing memory status = %d, want 404", rec.Code)
	}
}
