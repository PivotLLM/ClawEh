// ClawEh - WebUI API
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/PivotLLM/ClawEh/cogmem/portable"
	cogmemstore "github.com/PivotLLM/ClawEh/cogmem/store"
)

// maxImportBytes caps an uploaded memory document. A real export of the largest
// production store is well under a megabyte; this is a guard against a mistaken
// upload, not a meaningful limit.
const maxImportBytes = 32 << 20

// countRetired counts retired memories across the given domains without
// listing them, so the UI can offer "show retired" with a number on it.
func countRetired(ctx context.Context, s *cogmemstore.Store, domains []cogmemstore.Domain) int {
	n := 0
	for _, d := range domains {
		mems, err := s.ListMemories(ctx, s.DB(), d.ID, cogmemstore.StatusRetired)
		if err != nil {
			continue
		}
		n += len(mems)
	}
	return n
}

// patchMemoryRequest is a partial update. Both fields are optional; whichever
// is present is applied.
type patchMemoryRequest struct {
	Type   *string `json:"type,omitempty"`
	Status *string `json:"status,omitempty"`
}

// handlePatchMemory changes a memory's type and/or status.
//
// These are the two corrections that matter after the fact. Type decides
// whether a memory is in the prompt at all — an event is not — and the model
// picks it at write time with no way to revisit. Status is retire and restore.
// Text is deliberately not editable: retiring and recreating keeps the audit
// trail honest, which an in-place rewrite would not.
//
//	PATCH /api/memory/{id}/memories/{memoryID}
func (h *Handler) handlePatchMemory(w http.ResponseWriter, r *http.Request) {
	s, ok := h.openMemoryForWrite(w, r)
	if !ok {
		return
	}
	defer s.Close()

	var req patchMemoryRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Type == nil && req.Status == nil {
		http.Error(w, "nothing to change: give type, status, or both", http.StatusBadRequest)
		return
	}
	memoryID := r.PathValue("memoryID")
	ctx := context.Background()

	if req.Type != nil {
		t := cogmemstore.MemoryType(*req.Type)
		if !cogmemstore.ValidMemoryTypes(t) {
			http.Error(w, "unknown memory type", http.StatusBadRequest)
			return
		}
		if _, err := s.SetMemoryType(ctx, s.DB(), memoryID, t); err != nil {
			writeMemoryErr(w, err, "failed to change memory type")
			return
		}
	}
	if req.Status != nil {
		if err := applyStatus(ctx, s, memoryID, *req.Status); err != nil {
			writeMemoryErr(w, err, "failed to change memory status")
			return
		}
	}

	m, err := s.GetMemory(ctx, s.DB(), memoryID)
	if err != nil {
		writeMemoryErr(w, err, "failed to read memory")
		return
	}
	writeJSON(w, http.StatusOK, toMemoryMemory(m))
}

// applyStatus moves a memory to active or retired. Any other value is an error
// rather than a no-op: silently ignoring an unknown status would look like a
// successful change in the UI.
func applyStatus(ctx context.Context, s *cogmemstore.Store, memoryID, status string) error {
	switch cogmemstore.Status(status) {
	case cogmemstore.StatusActive:
		return s.RestoreMemory(ctx, s.DB(), memoryID)
	case cogmemstore.StatusRetired:
		return s.RetireMemory(ctx, s.DB(), memoryID, "retired from the WebUI")
	default:
		return fmt.Errorf("unknown status %q", status)
	}
}

// createDomainRequest creates a domain to hold hand-written memories.
type createDomainRequest struct {
	Name    string `json:"name"`
	Sticky  bool   `json:"sticky"`
	Summary string `json:"summary"`
}

// handleCreateDomain adds a domain.
//
//	POST /api/memory/{id}/domains
func (h *Handler) handleCreateDomain(w http.ResponseWriter, r *http.Request) {
	s, ok := h.openMemoryForWrite(w, r)
	if !ok {
		return
	}
	defer s.Close()

	var req createDomainRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	d, err := s.CreateDomain(context.Background(), s.DB(), cogmemstore.CreateDomainParams{
		AgentID:    r.PathValue("id"),
		SessionKey: r.PathValue("id"),
		Sticky:     req.Sticky,
		Name:       strings.TrimSpace(req.Name),
		Status:     cogmemstore.StatusActive,
		Summary:    req.Summary,
	})
	if err != nil {
		// A duplicate name is the expected mistake and deserves its own status.
		if strings.Contains(err.Error(), "already exists") || strings.Contains(err.Error(), "taken") {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, "failed to create domain", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, memoryDomain{
		ID: d.ID, Sticky: d.Sticky(), Name: d.Name, Status: string(d.Status),
		Summary: d.Summary, Memories: []memoryMemory{},
	})
}

// createMemoryRequest is a hand-written memory.
type createMemoryRequest struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	FileRef string `json:"file_ref,omitempty"`
}

// handleCreateMemory adds a memory the operator wrote.
//
// It is recorded with origin=user and confidence 1.0. That origin was declared,
// documented and rendered into the prompt from the beginning, but nothing could
// write it — this is the write side. It matters because it is the one piece of
// provenance that is verifiable rather than self-reported: the assistant can
// tell a memory the user typed from one it inferred, and weight it accordingly.
//
//	POST /api/memory/{id}/domains/{domainID}/memories
func (h *Handler) handleCreateMemory(w http.ResponseWriter, r *http.Request) {
	s, ok := h.openMemoryForWrite(w, r)
	if !ok {
		return
	}
	defer s.Close()

	var req createMemoryRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		http.Error(w, "text is required", http.StatusBadRequest)
		return
	}
	t := cogmemstore.MemoryType(req.Type)
	if !cogmemstore.ValidMemoryTypes(t) {
		http.Error(w, "unknown memory type", http.StatusBadRequest)
		return
	}
	m, err := s.AddMemory(context.Background(), s.DB(), cogmemstore.AddMemoryParams{
		DomainID:   r.PathValue("domainID"),
		Type:       t,
		Text:       strings.TrimSpace(req.Text),
		Status:     cogmemstore.StatusActive,
		Confidence: 1.0,
		Origin:     cogmemstore.OriginUser,
		FileRef:    req.FileRef,
	})
	if err != nil {
		writeMemoryErr(w, err, "failed to create memory")
		return
	}
	writeJSON(w, http.StatusCreated, toMemoryMemory(m))
}

// bulkRequest applies one action to many memories.
type bulkRequest struct {
	Action string   `json:"action"` // retype | retire | restore | delete
	Type   string   `json:"type,omitempty"`
	IDs    []string `json:"ids"`
}

// bulkResponse reports per-id outcomes without failing the whole request on one
// bad id — a bulk action over hundreds of rows that aborts halfway leaves the
// operator with no idea what happened.
type bulkResponse struct {
	Changed int               `json:"changed"`
	Failed  map[string]string `json:"failed,omitempty"`
}

// handleBulkMemories applies one action to many memories at once.
//
// This is not a convenience. Production stores hold hundreds of near-identical
// recurring notes — one per scheduled run — and retyping or clearing them one
// row at a time is not a workflow anybody completes.
//
//	POST /api/memory/{id}/bulk
func (h *Handler) handleBulkMemories(w http.ResponseWriter, r *http.Request) {
	s, ok := h.openMemoryForWrite(w, r)
	if !ok {
		return
	}
	defer s.Close()

	var req bulkRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if len(req.IDs) == 0 {
		http.Error(w, "ids is required", http.StatusBadRequest)
		return
	}
	var newType cogmemstore.MemoryType
	if req.Action == "retype" {
		newType = cogmemstore.MemoryType(req.Type)
		if !cogmemstore.ValidMemoryTypes(newType) {
			http.Error(w, "retype needs a valid type", http.StatusBadRequest)
			return
		}
	}

	ctx := context.Background()
	resp := bulkResponse{Failed: map[string]string{}}
	for _, id := range req.IDs {
		var err error
		switch req.Action {
		case "retype":
			_, err = s.SetMemoryType(ctx, s.DB(), id, newType)
		case "retire":
			err = s.RetireMemory(ctx, s.DB(), id, "retired from the WebUI")
		case "restore":
			err = s.RestoreMemory(ctx, s.DB(), id)
		case "delete":
			err = s.DeleteMemory(ctx, s.DB(), id)
		default:
			http.Error(w, "unknown action: use retype, retire, restore or delete",
				http.StatusBadRequest)
			return
		}
		if err != nil {
			resp.Failed[id] = err.Error()
			continue
		}
		resp.Changed++
	}
	if len(resp.Failed) == 0 {
		resp.Failed = nil
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleExportMemory downloads the whole store as a YAML document.
//
//	GET /api/memory/{id}/export
func (h *Handler) handleExportMemory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" || strings.ContainsAny(id, "/\\") || strings.Contains(id, "..") {
		http.Error(w, "invalid memory id", http.StatusBadRequest)
		return
	}
	path, _, ok := h.findMemoryDB(id)
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

	doc, err := portable.Export(context.Background(), s)
	if err != nil {
		http.Error(w, "failed to export memory", http.StatusInternalServerError)
		return
	}
	out, err := portable.Marshal(doc)
	if err != nil {
		http.Error(w, "failed to render export", http.StatusInternalServerError)
		return
	}
	name := fmt.Sprintf("%s-memory-%s.yaml", id, time.Now().UTC().Format("20060102"))
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	_, _ = w.Write(out)
}

// handleImportMemory loads a YAML document into a store.
//
// Mode comes from ?mode=merge (default) or ?mode=replace. Merge is the safe
// one and cannot destroy anything; replace empties the store first, which makes
// a document a true restore point and is why it must be asked for explicitly.
//
//	POST /api/memory/{id}/import?mode=merge|replace
func (h *Handler) handleImportMemory(w http.ResponseWriter, r *http.Request) {
	s, ok := h.openMemoryForWrite(w, r)
	if !ok {
		return
	}
	defer s.Close()

	mode := portable.ImportMode(r.URL.Query().Get("mode"))
	if mode == "" {
		mode = portable.ImportMerge
	}
	if mode != portable.ImportMerge && mode != portable.ImportReplace {
		http.Error(w, `mode must be "merge" or "replace"`, http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxImportBytes))
	if err != nil {
		http.Error(w, "failed to read upload", http.StatusBadRequest)
		return
	}
	doc, err := portable.Unmarshal(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	res, err := portable.Import(context.Background(), s, doc, mode, id, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// writeMemoryErr maps a store error onto a status code, so a missing id reads
// as 404 rather than a server fault.
func writeMemoryErr(w http.ResponseWriter, err error, msg string) {
	if errors.Is(err, cogmemstore.ErrNotFound) {
		http.Error(w, "memory not found", http.StatusNotFound)
		return
	}
	http.Error(w, msg, http.StatusInternalServerError)
}
