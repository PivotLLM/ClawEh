// ClawEh
// License: MIT

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/PivotLLM/ClawEh/tools"
)

// GET /api/agents/tools gives the standard set the Agents page's "Default
// tools" button writes into an agent's list: every DefaultEnabled tool, as a
// list even when it is empty.
func TestHandleListAgentTools_DefaultTools(t *testing.T) {
	h := NewHandler(setupTestEnv(t))
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents/tools", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		DefaultTools *[]string `json:"default_tools"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.DefaultTools == nil {
		t.Fatalf("default_tools missing or null: %s", rec.Body.String())
	}
	if want := tools.DefaultEnabledToolNames(); !slices.Equal(*body.DefaultTools, want) {
		t.Errorf("default_tools = %v, want %v", *body.DefaultTools, want)
	}
}

// The default set lets an agent delete in its own folders (file_delete) but
// not in the common folder every agent shares (common_delete).
func TestDefaultTools_CommonDeleteNotDefault(t *testing.T) {
	names := tools.DefaultEnabledToolNames()
	for _, want := range []string{"file_delete", "common_get", "common_put"} {
		if !slices.Contains(names, want) {
			t.Errorf("%s missing from the default tools %v", want, names)
		}
	}
	if slices.Contains(names, "common_delete") {
		t.Errorf("common_delete must not be in the default tools %v", names)
	}
}
