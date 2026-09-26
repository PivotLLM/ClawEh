// ClawEh
// License: MIT

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/audit"
)

// initTestAudit installs a process-wide audit store in a temp dir for the test.
func initTestAudit(t *testing.T) *audit.Store {
	t.Helper()
	if err := audit.Init(t.TempDir()); err != nil {
		t.Fatalf("audit.Init: %v", err)
	}
	t.Cleanup(func() {
		if err := audit.Close(); err != nil {
			t.Errorf("audit.Close: %v", err)
		}
	})
	return audit.Default()
}

func flushAudit(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := audit.Default().Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
}

func getAudit(t *testing.T, mux *http.ServeMux, query string) (int, auditListResponse, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/audit"+query, nil))
	var resp auditListResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("Unmarshal: %v: %s", err, rec.Body.String())
		}
	}
	return rec.Code, resp, rec.Body.String()
}

func TestHandleListAudit_NotInitialised(t *testing.T) {
	configPath := setupTestEnv(t)
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	code, _, body := getAudit(t, mux, "")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", code, body)
	}
}

func TestHandleListAudit_PaginationAndFilters(t *testing.T) {
	configPath := setupTestEnv(t)
	store := initTestAudit(t)
	base := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	for i := range 5 {
		store.Record(audit.Event{
			TS: base.Add(time.Duration(i) * time.Minute), Kind: audit.KindToolCall,
			Agent: "alice", Session: "s1", Tool: "t", DurationMS: int64(i),
		})
	}
	store.Record(audit.Event{TS: base.Add(time.Hour), Kind: audit.KindAuth, Actor: "eric", Summary: "login", Outcome: audit.OutcomeOK})
	store.Record(audit.Event{TS: base.Add(2 * time.Hour), Kind: audit.KindToolCall, Agent: "bob", Tool: "t"})
	flushAudit(t)

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// Page 1: newest first, limit 3.
	code, page1, body := getAudit(t, mux, "?limit=3")
	if code != http.StatusOK {
		t.Fatalf("status = %d: %s", code, body)
	}
	if len(page1.Events) != 3 || page1.Events[0].Kind != audit.KindToolCall || page1.Events[0].Agent != "bob" {
		t.Fatalf("page1 = %+v", page1.Events)
	}
	if page1.NextBeforeID != page1.Events[2].ID {
		t.Errorf("next_before_id = %d, want %d", page1.NextBeforeID, page1.Events[2].ID)
	}

	// Page 2 continues from next_before_id without overlap.
	code, page2, body := getAudit(t, mux, "?limit=3&before_id="+itoa(page1.NextBeforeID))
	if code != http.StatusOK {
		t.Fatalf("status = %d: %s", code, body)
	}
	if len(page2.Events) != 3 || page2.Events[0].ID >= page1.NextBeforeID {
		t.Fatalf("page2 = %+v", page2.Events)
	}
	code, page3, _ := getAudit(t, mux, "?limit=3&before_id="+itoa(page2.NextBeforeID))
	if code != http.StatusOK || len(page3.Events) != 1 {
		t.Fatalf("page3 = %+v (status %d)", page3.Events, code)
	}
	code, page4, _ := getAudit(t, mux, "?limit=3&before_id="+itoa(page3.NextBeforeID))
	if code != http.StatusOK || len(page4.Events) != 0 || page4.NextBeforeID != 0 {
		t.Fatalf("page4 = %+v next=%d", page4.Events, page4.NextBeforeID)
	}

	tests := []struct {
		name  string
		query string
		want  int
	}{
		{"kind", "?kind=auth", 1},
		{"agent", "?agent=alice", 5},
		{"session", "?session=s1", 5},
		{"since", "?since=" + base.Add(30*time.Minute).Format(time.RFC3339), 2},
		{"until", "?until=" + base.Add(90*time.Second).Format(time.RFC3339), 2},
		{"default limit", "", 7},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, resp, body := getAudit(t, mux, tc.query)
			if code != http.StatusOK {
				t.Fatalf("status = %d: %s", code, body)
			}
			if len(resp.Events) != tc.want {
				t.Errorf("rows = %d, want %d", len(resp.Events), tc.want)
			}
		})
	}
}

func TestHandleListAudit_BadParams(t *testing.T) {
	configPath := setupTestEnv(t)
	initTestAudit(t)
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	for _, q := range []string{"?since=yesterday", "?until=12", "?limit=abc", "?limit=-1", "?before_id=x"} {
		code, _, body := getAudit(t, mux, q)
		if code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400: %s", q, code, body)
		}
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// TestConfigWrite_AuditRecordsKeysNotValues: saving the config records which
// top-level sections changed, the actor and the client IP — and never a value,
// so a changed credential does not end up in the audit log.
func TestConfigWrite_AuditRecordsKeysNotValues(t *testing.T) {
	configPath := setupTestEnv(t)
	initTestAudit(t)
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	const secret = "sk-brand-new-secret-value-XYZ"

	// PUT: change one provider's API key; everything else is echoed back.
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	cfg.Providers[0].APIKey = secret
	body, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(string(body)))
	req.RemoteAddr = "192.0.2.7:51000"
	req = req.WithContext(audit.WithActor(req.Context(), "eric"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d: %s", rec.Code, rec.Body.String())
	}

	// PATCH: change the gateway port only, unauthenticated.
	patch := httptest.NewRequest(http.MethodPatch, "/api/config", strings.NewReader(`{"gateway":{"port":18999}}`))
	patch.RemoteAddr = "[2001:db8::9]:40000"
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, patch)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d: %s", rec.Code, rec.Body.String())
	}
	flushAudit(t)

	rows, err := audit.Default().Query(context.Background(), audit.Filter{Kind: audit.KindConfigWrite})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("config_write rows = %d, want 2: %+v", len(rows), rows)
	}
	patchRow, putRow := rows[0], rows[1]

	if putRow.Actor != "eric" || putRow.Sender != "192.0.2.7" || putRow.Outcome != audit.OutcomeOK {
		t.Errorf("PUT row = %+v", putRow)
	}
	if putRow.Summary != "providers" || putRow.Details != `{"keys":["providers"]}` {
		t.Errorf("PUT row keys: summary=%q details=%s", putRow.Summary, putRow.Details)
	}
	if patchRow.Actor != "" || patchRow.Sender != "2001:db8::9" {
		t.Errorf("PATCH row = %+v", patchRow)
	}
	if patchRow.Summary != "gateway" || patchRow.Details != `{"keys":["gateway"]}` {
		t.Errorf("PATCH row keys: summary=%q details=%s", patchRow.Summary, patchRow.Details)
	}
	for _, r := range rows {
		blob, err := json.Marshal(r)
		if err != nil {
			t.Fatalf("Marshal row: %v", err)
		}
		if strings.Contains(string(blob), secret) || strings.Contains(string(blob), "18999") {
			t.Fatalf("a config value reached the audit log: %s", blob)
		}
	}
}

func TestChangedTopLevelKeys(t *testing.T) {
	a := config.DefaultConfig()
	b := config.DefaultConfig()
	if got := changedTopLevelKeys(a, b); len(got) != 0 {
		t.Errorf("identical configs differ: %v", got)
	}
	b.Gateway.Port = 1
	b.Logging.Level = "debug"
	if got := changedTopLevelKeys(a, b); strings.Join(got, ",") != "gateway,logging" {
		t.Errorf("keys = %v, want [gateway logging]", got)
	}
	if got := changedTopLevelKeys(nil, b); len(got) == 0 {
		t.Error("nil old config should report every key")
	}
}
