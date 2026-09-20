package api

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	cogmemstore "github.com/PivotLLM/cogmem/store"

	"github.com/PivotLLM/ClawEh/cogmemhost"
	"github.com/PivotLLM/ClawEh/utils"
)

// registerMemoryRoutes binds the cognitive-memory browsing and curation
// endpoints. Curation is the point: the model chooses a memory's type when it
// writes and gets it wrong often enough — a trip log filed as a fact, its own
// bookkeeping filed as a rule — that correcting it by hand has to be practical,
// including in bulk.
func (h *Handler) registerMemoryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/memory", h.handleListMemoryStores)
	mux.HandleFunc("GET /api/memory/{id}", h.handleGetMemoryStore)
	mux.HandleFunc("DELETE /api/memory/{id}/domains/{domainID}", h.handleDeleteDomain)
	mux.HandleFunc("DELETE /api/memory/{id}/memories/{memoryID}", h.handleDeleteMemory)
	mux.HandleFunc("PATCH /api/memory/{id}/memories/{memoryID}", h.handlePatchMemory)
	mux.HandleFunc("POST /api/memory/{id}/domains", h.handleCreateDomain)
	mux.HandleFunc("POST /api/memory/{id}/domains/{domainID}/memories", h.handleCreateMemory)
	mux.HandleFunc("POST /api/memory/{id}/bulk", h.handleBulkMemories)
	mux.HandleFunc("GET /api/memory/{id}/export", h.handleExportMemory)
	mux.HandleFunc("POST /api/memory/{id}/import", h.handleImportMemory)
}

// memoryStoreItem is one per-session cognitive-memory database in the list view.
type memoryStoreItem struct {
	ID        string `json:"id"`    // sanitized session key (db filename base)
	Agent     string `json:"agent"` // agent id derived from the workspace dir
	Updated   string `json:"updated"`
	SizeBytes int64  `json:"size_bytes"`
}

type memoryMemory struct {
	ID         string  `json:"id"`
	Type       string  `json:"type"`
	Text       string  `json:"text"`
	Status     string  `json:"status"`
	Confidence float64 `json:"confidence"`
	Origin     string  `json:"origin"`
	FileRef    string  `json:"file_ref"`
	Created    string  `json:"created"`
	Updated    string  `json:"updated"`
}

type memoryDomain struct {
	ID              string         `json:"id"`
	Sticky          bool           `json:"sticky"`
	Name            string         `json:"name"`
	Status          string         `json:"status"`
	Summary         string         `json:"summary"`
	Triggers        string         `json:"triggers,omitempty"`
	KeywordTriggers string         `json:"keyword_triggers,omitempty"`
	LastUsed        string         `json:"last_used,omitempty"` // RFC3339; empty = never used since creation
	Memories        []memoryMemory `json:"memories"`
}

type memoryRun struct {
	Trigger    string `json:"trigger"`
	Status     string `json:"status"`
	OpsApplied int    `json:"ops_applied"`
	StartedAt  string `json:"started_at"`
	// Error is why the run failed; Note is information about a run that
	// succeeded (an auto-repair, say). Separate fields so the UI can style
	// them differently — a note rendered as an error reads as a fault.
	Error string `json:"error,omitempty"`
	Note  string `json:"note,omitempty"`
}

type memoryDetailResponse struct {
	ID             string         `json:"id"`
	Agent          string         `json:"agent"`
	ActiveDomains  int            `json:"active_domains"`
	ActiveMemories int            `json:"active_memories"`
	RetiredCount   int            `json:"retired_count"`
	LastRun        *memoryRun     `json:"last_run"`
	Domains        []memoryDomain `json:"domains"`
}

// domainLastUsed renders a domain's last-active time as RFC3339, or "" when it
// has never been recorded.
func domainLastUsed(d cogmemstore.Domain) string {
	if t, ok := d.LastActive(); ok {
		return t.Format(time.RFC3339)
	}
	return ""
}

// sortMemoryStores orders stores by agent name (alphabetical, case-insensitive)
// and, within an agent, most-recently-updated first.
func sortMemoryStores(items []memoryStoreItem) {
	sort.Slice(items, func(i, j int) bool {
		ai, aj := strings.ToLower(items[i].Agent), strings.ToLower(items[j].Agent)
		if ai != aj {
			return ai < aj
		}
		return items[i].Updated > items[j].Updated
	})
}

// agentForSessionsDir derives the agent id from a sessions directory path of the
// form <base>/<agentid>/sessions, returning the parent directory's name.
func agentForSessionsDir(dir string) string {
	return filepath.Base(filepath.Dir(dir))
}

// memoryDBForSessionsDir returns the agent's memory database for a sessions
// directory: the memory is <workspace>/cogmem/cogmem.db and the sessions
// directory is the workspace's child.
func memoryDBForSessionsDir(dir string) string {
	return cogmemstore.DBPath(cogmemhost.Dir(filepath.Dir(dir)))
}

// handleListMemoryStores lists every agent's cognitive-memory database. One
// agent has one memory, so the store id is the agent's workspace name. The
// response key is "sessions" for compatibility with the page that reads it.
//
//	GET /api/memory
func (h *Handler) handleListMemoryStores(w http.ResponseWriter, r *http.Request) {
	dirs, err := h.sessionsDirs()
	if err != nil {
		http.Error(w, "failed to resolve sessions directory", http.StatusInternalServerError)
		return
	}

	items := []memoryStoreItem{}
	seen := make(map[string]struct{})
	for _, dir := range dirs {
		agent := agentForSessionsDir(dir)
		if _, dup := seen[agent]; dup {
			continue
		}
		info, err := os.Stat(memoryDBForSessionsDir(dir))
		if err != nil {
			continue
		}
		seen[agent] = struct{}{}
		items = append(items, memoryStoreItem{
			ID:        agent,
			Agent:     agent,
			Updated:   info.ModTime().Format(time.RFC3339),
			SizeBytes: info.Size(),
		})
	}

	sortMemoryStores(items)

	w.Header().Set("Content-Type", "application/json")
	encodeJSON(w, map[string]any{"sessions": items})
}

// findMemoryDB locates the memory database for a store id (an agent's
// workspace name), returning its path and the agent id.
func (h *Handler) findMemoryDB(id string) (path, agent string, ok bool) {
	dirs, err := h.sessionsDirs()
	if err != nil {
		return "", "", false
	}
	for _, dir := range dirs {
		if agentForSessionsDir(dir) != id {
			continue
		}
		p := memoryDBForSessionsDir(dir)
		if _, err := os.Stat(p); err == nil {
			return p, id, true
		}
	}
	return "", "", false
}

func toMemoryMemory(m cogmemstore.Memory) memoryMemory {
	return memoryMemory{
		ID:         m.ID,
		Type:       string(m.Type),
		Text:       m.Text,
		Status:     string(m.Status),
		Confidence: m.Confidence,
		Origin:     string(m.Origin),
		FileRef:    m.FileRef,
		Created:    m.CreatedAt.Format(time.RFC3339),
		Updated:    m.UpdatedAt.Format(time.RFC3339),
	}
}

// handleGetMemoryStore returns a session's domains, their memories, and the
// last consolidation run.
//
// Retired memories are included only with ?include_retired=1. They are the
// minority and would otherwise bury the active ones, but they have to be
// reachable: restoring one is impossible if it cannot be seen.
//
//	GET /api/memory/{id}[?include_retired=1]
func (h *Handler) handleGetMemoryStore(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "missing memory id", http.StatusBadRequest)
		return
	}
	// Reject path traversal: the id is a flat filename base.
	if strings.ContainsAny(id, "/\\") || strings.Contains(id, "..") {
		http.Error(w, "invalid memory id", http.StatusBadRequest)
		return
	}

	path, agent, ok := h.findMemoryDB(id)
	if !ok {
		http.Error(w, "memory store not found", http.StatusNotFound)
		return
	}

	s, err := cogmemstore.Open(path)
	if err != nil {
		http.Error(w, "failed to open memory store", http.StatusInternalServerError)
		return
	}
	defer utils.CloseQuietly(s)

	ctx := context.Background()
	db := s.DB()

	includeRetired := r.URL.Query().Get("include_retired") != ""
	statuses := []cogmemstore.Status{cogmemstore.StatusActive}
	if includeRetired {
		statuses = append(statuses, cogmemstore.StatusRetired)
	}

	resp := memoryDetailResponse{ID: id, Agent: agent, Domains: []memoryDomain{}}

	domains, err := s.ListDomains(ctx, db, cogmemstore.StatusActive)
	if err != nil {
		http.Error(w, "failed to read domains", http.StatusInternalServerError)
		return
	}
	resp.ActiveDomains = len(domains)
	for _, d := range domains {
		mems, err := s.ListMemories(ctx, db, d.ID, statuses...)
		if err != nil {
			http.Error(w, "failed to read memories", http.StatusInternalServerError)
			return
		}
		dm := memoryDomain{
			ID:              d.ID,
			Sticky:          d.Sticky(),
			Name:            d.Name,
			Status:          string(d.Status),
			Summary:         d.Summary,
			Triggers:        d.Triggers,
			KeywordTriggers: d.KeywordTriggers,
			LastUsed:        domainLastUsed(d),
			Memories:        make([]memoryMemory, 0, len(mems)),
		}
		for _, m := range mems {
			dm.Memories = append(dm.Memories, toMemoryMemory(m))
			switch m.Status {
			case cogmemstore.StatusRetired:
				resp.RetiredCount++
			default:
				resp.ActiveMemories++
			}
		}
		resp.Domains = append(resp.Domains, dm)
	}

	// Retired memories are counted even when they are not listed, so the UI can
	// offer to show them without a second request.
	if !includeRetired {
		resp.RetiredCount = countRetired(ctx, s, domains)
	}

	if run, ok, err := s.LastRun(ctx, db); err == nil && ok {
		resp.LastRun = &memoryRun{
			Trigger:    run.Trigger,
			Status:     run.Status,
			OpsApplied: run.OpsApplied,
			StartedAt:  run.StartedAt.Format(time.RFC3339),
			Error:      run.Error,
			Note:       run.Note,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	encodeJSON(w, resp)
}

// openMemoryForWrite validates the {id} path value, locates the agent's
// memory store, and opens it. On any failure it writes the HTTP error and returns
// ok=false. The caller must Close the returned store.
func (h *Handler) openMemoryForWrite(w http.ResponseWriter, r *http.Request) (*cogmemstore.Store, bool) {
	id := r.PathValue("id")
	if id == "" || strings.ContainsAny(id, "/\\") || strings.Contains(id, "..") {
		http.Error(w, "invalid memory id", http.StatusBadRequest)
		return nil, false
	}
	path, _, ok := h.findMemoryDB(id)
	if !ok {
		http.Error(w, "memory store not found", http.StatusNotFound)
		return nil, false
	}
	s, err := cogmemstore.Open(path)
	if err != nil {
		http.Error(w, "failed to open memory store", http.StatusInternalServerError)
		return nil, false
	}
	return s, true
}

// handleDeleteDomain hard-deletes a domain (and its memories) from a session's
// memory store. DELETE /api/memory/{id}/domains/{domainID}
func (h *Handler) handleDeleteDomain(w http.ResponseWriter, r *http.Request) {
	s, ok := h.openMemoryForWrite(w, r)
	if !ok {
		return
	}
	defer utils.CloseQuietly(s)
	if err := s.DeleteDomain(context.Background(), s.DB(), r.PathValue("domainID")); err != nil {
		if errors.Is(err, cogmemstore.ErrNotFound) {
			http.Error(w, "domain not found", http.StatusNotFound)
			return
		}
		http.Error(w, "failed to delete domain", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteMemory hard-deletes a single memory from a session's memory store.
// DELETE /api/memory/{id}/memories/{memoryID}
func (h *Handler) handleDeleteMemory(w http.ResponseWriter, r *http.Request) {
	s, ok := h.openMemoryForWrite(w, r)
	if !ok {
		return
	}
	defer utils.CloseQuietly(s)
	if err := s.DeleteMemory(context.Background(), s.DB(), r.PathValue("memoryID")); err != nil {
		if errors.Is(err, cogmemstore.ErrNotFound) {
			http.Error(w, "memory not found", http.StatusNotFound)
			return
		}
		http.Error(w, "failed to delete memory", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
