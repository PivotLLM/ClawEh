package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	cogmemstore "github.com/PivotLLM/ClawEh/pkg/cogmem/store"
)

const cogmemDBSuffix = ".cogmem.db"

// registerMemoryRoutes binds read-only cognitive-memory browser endpoints.
func (h *Handler) registerMemoryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/memory", h.handleListMemoryStores)
	mux.HandleFunc("GET /api/memory/{id}", h.handleGetMemoryStore)
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
	Priority   int     `json:"priority"`
	Source     string  `json:"source"`
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
	Memories        []memoryMemory `json:"memories"`
}

type memoryRun struct {
	Trigger    string `json:"trigger"`
	Status     string `json:"status"`
	OpsApplied int    `json:"ops_applied"`
	StartedAt  string `json:"started_at"`
	Error      string `json:"error,omitempty"`
}

type memoryDetailResponse struct {
	ID             string         `json:"id"`
	Agent          string         `json:"agent"`
	ActiveDomains  int            `json:"active_domains"`
	ActiveMemories int            `json:"active_memories"`
	Pending        int            `json:"pending"`
	LastRun        *memoryRun     `json:"last_run"`
	Domains        []memoryDomain `json:"domains"`
	PendingList    []memoryMemory `json:"pending_list"`
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

// handleListMemoryStores lists every per-session cognitive-memory database
// across all configured agent workspaces.
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
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		agent := agentForSessionsDir(dir)
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, cogmemDBSuffix) {
				continue
			}
			id := strings.TrimSuffix(name, cogmemDBSuffix)
			if _, dup := seen[id]; dup {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				continue
			}
			seen[id] = struct{}{}
			items = append(items, memoryStoreItem{
				ID:        id,
				Agent:     agent,
				Updated:   info.ModTime().Format(time.RFC3339),
				SizeBytes: info.Size(),
			})
		}
	}

	sortMemoryStores(items)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"sessions": items})
}

// findMemoryDB locates the .cogmem.db for a sanitized session id across all
// agent workspaces, returning its path and owning agent id.
func (h *Handler) findMemoryDB(id string) (path, agent string, ok bool) {
	dirs, err := h.sessionsDirs()
	if err != nil {
		return "", "", false
	}
	for _, dir := range dirs {
		p := filepath.Join(dir, id+cogmemDBSuffix)
		if _, err := os.Stat(p); err == nil {
			return p, agentForSessionsDir(dir), true
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
		Priority:   m.Priority,
		Source:     string(m.Source),
		Created:    m.CreatedAt.Format(time.RFC3339),
		Updated:    m.UpdatedAt.Format(time.RFC3339),
	}
}

// handleGetMemoryStore returns the active domains, their memories, the pending
// digest, and the last consolidation run for one session's database. Read-only.
//
//	GET /api/memory/{id}
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
	defer s.Close()

	ctx := context.Background()
	db := s.DB()

	resp := memoryDetailResponse{ID: id, Agent: agent, Domains: []memoryDomain{}, PendingList: []memoryMemory{}}

	domains, err := s.ListDomains(ctx, db, cogmemstore.StatusActive)
	if err != nil {
		http.Error(w, "failed to read domains", http.StatusInternalServerError)
		return
	}
	resp.ActiveDomains = len(domains)
	for _, d := range domains {
		mems, _ := s.ListMemories(ctx, db, d.ID, cogmemstore.StatusActive)
		dm := memoryDomain{
			ID:              d.ID,
			Sticky:          d.Sticky(),
			Name:            d.Name,
			Status:          string(d.Status),
			Summary:         d.Summary,
			Triggers:        d.Triggers,
			KeywordTriggers: d.KeywordTriggers,
			Memories:        make([]memoryMemory, 0, len(mems)),
		}
		for _, m := range mems {
			dm.Memories = append(dm.Memories, toMemoryMemory(m))
		}
		resp.ActiveMemories += len(mems)
		resp.Domains = append(resp.Domains, dm)
	}

	if pending, err := s.ListPending(ctx, db, 100); err == nil {
		for _, m := range pending {
			resp.PendingList = append(resp.PendingList, toMemoryMemory(m))
		}
	}
	if n, err := s.PendingCount(ctx, db); err == nil {
		resp.Pending = n
	}

	if run, ok, err := s.LastRun(ctx, db); err == nil && ok {
		resp.LastRun = &memoryRun{
			Trigger:    run.Trigger,
			Status:     run.Status,
			OpsApplied: run.OpsApplied,
			StartedAt:  run.StartedAt.Format(time.RFC3339),
			Error:      run.Error,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
