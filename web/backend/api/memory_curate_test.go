package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/cogmem/portable"
	cogmemstore "github.com/PivotLLM/ClawEh/cogmem/store"
)

// curateEnv seeds a store and returns its id plus a mux, for the curation
// endpoints. The seeded domain holds one memory of each shape the UI has to be
// able to correct.
func curateEnv(t *testing.T) (string, *http.ServeMux, string) {
	t.Helper()
	configPath, cleanup := setupTestEnv(t)
	t.Cleanup(cleanup)

	dir := sessionsTestDir(t, configPath)
	sessionKey := "agent:main:webui:direct:webui:curate-test"
	id := cogmemstore.SanitizeSessionKey(sessionKey)
	path := filepath.Join(dir, id+".cogmem.db")

	s, err := cogmemstore.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	ctx := context.Background()
	d, err := s.CreateDomain(ctx, s.DB(), cogmemstore.CreateDomainParams{
		Name: "Trips", Status: cogmemstore.StatusActive,
	})
	if err != nil {
		t.Fatalf("create domain: %v", err)
	}
	for _, txt := range []string{"drove to the KOA", "drove home", "home is Ottawa"} {
		if _, err := s.AddMemory(ctx, s.DB(), cogmemstore.AddMemoryParams{
			DomainID: d.ID, Type: cogmemstore.TypeFact, Text: txt,
			Status: cogmemstore.StatusActive, Confidence: 0.9,
		}); err != nil {
			t.Fatalf("add: %v", err)
		}
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return id, mux, d.ID
}

// memoriesOf reads the detail endpoint and returns the memories of one domain.
func memoriesOf(t *testing.T, mux *http.ServeMux, id, domain string, includeRetired bool) []memoryMemory {
	t.Helper()
	url := "/api/memory/" + id
	if includeRetired {
		url += "?include_retired=1"
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", url, rec.Code, rec.Body.String())
	}
	var detail memoryDetailResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, d := range detail.Domains {
		if d.ID == domain {
			return d.Memories
		}
	}
	return nil
}

func do(t *testing.T, mux *http.ServeMux, method, url string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == nil {
		r = httptest.NewRequest(method, url, nil)
	} else {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		r = httptest.NewRequest(method, url, bytes.NewReader(b))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

// Retyping is the correction that matters most: type decides whether a memory
// is in the prompt at all, the model picks it at write time, and it gets it
// wrong often enough that fixing it by hand has to work.
func TestPatchMemoryChangesType(t *testing.T) {
	id, mux, domain := curateEnv(t)
	mems := memoriesOf(t, mux, id, domain, false)
	if len(mems) != 3 {
		t.Fatalf("expected 3 seeded memories, got %d", len(mems))
	}
	target := mems[0].ID

	rec := do(t, mux, http.MethodPatch,
		"/api/memory/"+id+"/memories/"+target, map[string]string{"type": "event"})
	if rec.Code != http.StatusOK {
		t.Fatalf("patch = %d: %s", rec.Code, rec.Body.String())
	}
	var got memoryMemory
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Type != "event" {
		t.Errorf("type = %q, want event", got.Type)
	}
}

// Retire and restore, both directions. A one-way control would let the operator
// hide a memory with no way to get it back.
func TestPatchMemoryRetiresAndRestores(t *testing.T) {
	id, mux, domain := curateEnv(t)
	target := memoriesOf(t, mux, id, domain, false)[0].ID

	if rec := do(t, mux, http.MethodPatch, "/api/memory/"+id+"/memories/"+target,
		map[string]string{"status": "retired"}); rec.Code != http.StatusOK {
		t.Fatalf("retire = %d: %s", rec.Code, rec.Body.String())
	}
	if n := len(memoriesOf(t, mux, id, domain, false)); n != 2 {
		t.Errorf("active memories = %d, want 2 after retiring one", n)
	}
	// Retired memories must be reachable, or restoring one is impossible.
	found := false
	for _, m := range memoriesOf(t, mux, id, domain, true) {
		if m.ID == target && m.Status == "retired" {
			found = true
		}
	}
	if !found {
		t.Error("retired memory not returned with include_retired")
	}

	if rec := do(t, mux, http.MethodPatch, "/api/memory/"+id+"/memories/"+target,
		map[string]string{"status": "active"}); rec.Code != http.StatusOK {
		t.Fatalf("restore = %d: %s", rec.Code, rec.Body.String())
	}
	if n := len(memoriesOf(t, mux, id, domain, false)); n != 3 {
		t.Errorf("active memories = %d, want 3 after restoring", n)
	}
}

func TestPatchMemoryRejectsBadInput(t *testing.T) {
	id, mux, domain := curateEnv(t)
	target := memoriesOf(t, mux, id, domain, false)[0].ID

	cases := map[string]struct {
		body any
		want int
	}{
		"unknown type":   {map[string]string{"type": "observation"}, http.StatusBadRequest},
		"unknown status": {map[string]string{"status": "review"}, http.StatusInternalServerError},
		"nothing to do":  {map[string]string{}, http.StatusBadRequest},
	}
	for name, tc := range cases {
		rec := do(t, mux, http.MethodPatch, "/api/memory/"+id+"/memories/"+target, tc.body)
		if rec.Code != tc.want {
			t.Errorf("%s: status = %d, want %d (%s)", name, rec.Code, tc.want, rec.Body.String())
		}
	}
	// A missing memory is 404, not a server fault.
	rec := do(t, mux, http.MethodPatch, "/api/memory/"+id+"/memories/hNOPE",
		map[string]string{"type": "fact"})
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing memory: status = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
}

// A memory the operator writes carries origin=user. That origin was declared,
// documented and rendered into the prompt from the start, and nothing could
// write it — this is the write side, and it is the one piece of provenance that
// is verifiable rather than self-reported.
func TestCreateMemoryIsTaggedAsUserWritten(t *testing.T) {
	id, mux, domain := curateEnv(t)

	rec := do(t, mux, http.MethodPost, "/api/memory/"+id+"/domains/"+domain+"/memories",
		map[string]string{"type": "rule", "text": "Do not use the word thuddy."})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}
	var got memoryMemory
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Origin != "user" {
		t.Errorf("origin = %q, want user", got.Origin)
	}
	if got.Confidence != 1.0 {
		t.Errorf("confidence = %v, want 1.0 for something the operator typed", got.Confidence)
	}
	if got.Type != "rule" {
		t.Errorf("type = %q, want rule", got.Type)
	}
}

func TestCreateMemoryValidates(t *testing.T) {
	id, mux, domain := curateEnv(t)
	base := "/api/memory/" + id + "/domains/" + domain + "/memories"
	for name, body := range map[string]any{
		"no text":      map[string]string{"type": "fact"},
		"unknown type": map[string]string{"type": "observation", "text": "x"},
		"no type":      map[string]string{"text": "x"},
	} {
		if rec := do(t, mux, http.MethodPost, base, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (%s)", name, rec.Code, rec.Body.String())
		}
	}
}

func TestCreateDomain(t *testing.T) {
	id, mux, _ := curateEnv(t)
	rec := do(t, mux, http.MethodPost, "/api/memory/"+id+"/domains",
		map[string]any{"name": "Writing", "sticky": true})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create domain = %d: %s", rec.Code, rec.Body.String())
	}
	var got memoryDomain
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Name != "Writing" || !got.Sticky {
		t.Errorf("got %+v, want a sticky domain named Writing", got)
	}
	// A duplicate name is the expected mistake and gets its own status.
	if rec := do(t, mux, http.MethodPost, "/api/memory/"+id+"/domains",
		map[string]any{"name": "Writing"}); rec.Code != http.StatusConflict {
		t.Errorf("duplicate name: status = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
	if rec := do(t, mux, http.MethodPost, "/api/memory/"+id+"/domains",
		map[string]any{"name": "  "}); rec.Code != http.StatusBadRequest {
		t.Errorf("blank name: status = %d, want 400", rec.Code)
	}
}

// Bulk is on the critical path, not a convenience: production stores hold
// hundreds of near-identical recurring notes, and one row at a time is not a
// workflow anybody finishes.
func TestBulkActions(t *testing.T) {
	id, mux, domain := curateEnv(t)
	mems := memoriesOf(t, mux, id, domain, false)
	ids := []string{mems[0].ID, mems[1].ID}

	rec := do(t, mux, http.MethodPost, "/api/memory/"+id+"/bulk",
		map[string]any{"action": "retype", "type": "event", "ids": ids})
	if rec.Code != http.StatusOK {
		t.Fatalf("bulk retype = %d: %s", rec.Code, rec.Body.String())
	}
	var res bulkResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Changed != 2 {
		t.Errorf("changed = %d, want 2", res.Changed)
	}
	events := 0
	for _, m := range memoriesOf(t, mux, id, domain, false) {
		if m.Type == "event" {
			events++
		}
	}
	if events != 2 {
		t.Errorf("%d memories are events, want 2", events)
	}

	if rec := do(t, mux, http.MethodPost, "/api/memory/"+id+"/bulk",
		map[string]any{"action": "delete", "ids": ids}); rec.Code != http.StatusOK {
		t.Fatalf("bulk delete = %d: %s", rec.Code, rec.Body.String())
	}
	if n := len(memoriesOf(t, mux, id, domain, true)); n != 1 {
		t.Errorf("%d memories remain, want 1", n)
	}
}

// One bad id must not abort the whole batch. A bulk action over hundreds of
// rows that stops halfway leaves the operator with no idea what happened.
func TestBulkReportsPerIDFailuresWithoutAborting(t *testing.T) {
	id, mux, domain := curateEnv(t)
	good := memoriesOf(t, mux, id, domain, false)[0].ID

	rec := do(t, mux, http.MethodPost, "/api/memory/"+id+"/bulk",
		map[string]any{"action": "retire", "ids": []string{good, "hNOPE"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("bulk = %d: %s", rec.Code, rec.Body.String())
	}
	var res bulkResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Changed != 1 {
		t.Errorf("changed = %d, want the one good id applied", res.Changed)
	}
	if _, ok := res.Failed["hNOPE"]; !ok {
		t.Errorf("failed = %v, want the bad id reported", res.Failed)
	}
}

func TestBulkRejectsBadRequests(t *testing.T) {
	id, mux, domain := curateEnv(t)
	good := memoriesOf(t, mux, id, domain, false)[0].ID
	for name, body := range map[string]any{
		"no ids":         map[string]any{"action": "retire", "ids": []string{}},
		"unknown action": map[string]any{"action": "vaporise", "ids": []string{good}},
		"retype no type": map[string]any{"action": "retype", "ids": []string{good}},
	} {
		if rec := do(t, mux, http.MethodPost, "/api/memory/"+id+"/bulk", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (%s)", name, rec.Code, rec.Body.String())
		}
	}
}

// Export downloads a document that import can read back.
func TestExportImportRoundTripOverHTTP(t *testing.T) {
	id, mux, domain := curateEnv(t)

	rec := do(t, mux, http.MethodGet, "/api/memory/"+id+"/export", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("export = %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "yaml") {
		t.Errorf("Content-Type = %q, want yaml", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
		t.Errorf("Content-Disposition = %q, want an attachment", cd)
	}
	body := rec.Body.Bytes()
	if _, err := portable.Unmarshal(body); err != nil {
		t.Fatalf("exported document does not parse: %v", err)
	}

	// Delete everything, then restore from the download.
	mems := memoriesOf(t, mux, id, domain, false)
	ids := make([]string, 0, len(mems))
	for _, m := range mems {
		ids = append(ids, m.ID)
	}
	do(t, mux, http.MethodPost, "/api/memory/"+id+"/bulk",
		map[string]any{"action": "delete", "ids": ids})
	if n := len(memoriesOf(t, mux, id, domain, true)); n != 0 {
		t.Fatalf("%d memories left after deleting them all", n)
	}

	req := httptest.NewRequest(http.MethodPost,
		"/api/memory/"+id+"/import?mode=merge", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("import = %d: %s", rec.Code, rec.Body.String())
	}
	if n := len(memoriesOf(t, mux, id, domain, false)); n != 3 {
		t.Errorf("%d memories restored, want 3", n)
	}
}

func TestImportRejectsBadInput(t *testing.T) {
	id, mux, _ := curateEnv(t)
	base := "/api/memory/" + id + "/import"

	for name, tc := range map[string]struct {
		url  string
		body string
	}{
		"unknown mode": {base + "?mode=obliterate", "format_version: 1\ndomains: []\n"},
		"not a dump":   {base, "hello: world\n"},
		"bad yaml":     {base, "format_version: [1\n"},
	} {
		req := httptest.NewRequest(http.MethodPost, tc.url, strings.NewReader(tc.body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (%s)", name, rec.Code, rec.Body.String())
		}
	}
}
