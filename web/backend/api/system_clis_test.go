package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleListCLIs(t *testing.T) {
	h := NewHandler("")
	req := httptest.NewRequest(http.MethodGet, "/api/system/clis", nil)
	rec := httptest.NewRecorder()
	h.handleListCLIs(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var got []cliInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != len(knownCLIs) {
		t.Fatalf("got %d entries, want %d", len(got), len(knownCLIs))
	}
	for i, c := range got {
		if c.Protocol != knownCLIs[i].Protocol || c.Label != knownCLIs[i].Label || c.Binary != knownCLIs[i].Binary {
			t.Errorf("entry %d = %+v, want protocol/label/binary %q/%q/%q",
				i, c, knownCLIs[i].Protocol, knownCLIs[i].Label, knownCLIs[i].Binary)
		}
		// An installed CLI must report its resolved path; a missing one must not
		// claim a path. (Version is best-effort, so it's not asserted.)
		if c.Installed && c.Path == "" {
			t.Errorf("entry %d installed but has no path", i)
		}
		if !c.Installed && c.Path != "" {
			t.Errorf("entry %d not installed but has path %q", i, c.Path)
		}
	}
}
