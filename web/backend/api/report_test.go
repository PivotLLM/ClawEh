// ClawEh
// License: MIT

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/report"
)

var testNow = time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)

// TestReportPDF: the endpoint streams a PDF inline, uncached, built from the
// loaded config. The body is checked for the PDF signature rather than its
// exact bytes.
func TestReportPDF(t *testing.T) {
	configPath := setupTestEnv(t)
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/report/pdf", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/pdf" {
		t.Errorf("Content-Type = %q, want application/pdf", ct)
	}
	if cd := rr.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, `inline; filename="claweh-report-`) {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if cc := rr.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	if !strings.HasPrefix(rr.Body.String(), "%PDF-") {
		t.Errorf("body does not start with a PDF signature: %q", rr.Body.String()[:min(8, rr.Body.Len())])
	}
	// The fixture's provider key must never reach the report.
	if strings.Contains(rr.Body.String(), "sk-default") {
		t.Error("provider API key leaked into the PDF")
	}
}

// TestReportAssessment: the JSON endpoint carries the identity fields and
// exactly the rows the PDF's assessment table has, and no secret value.
func TestReportAssessment(t *testing.T) {
	configPath := setupTestEnv(t)
	// A second secret, in a field the assessment table talks about (the WebUI
	// chat token is reported as set/not set).
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Channels.WebUI.Enabled = true
	cfg.Channels.WebUI.Token = "WEBUISECRET-fixture-7f3a"
	if err = config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/report/assessment", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cc := rr.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	for _, secret := range []string{"sk-default", "WEBUISECRET-fixture-7f3a"} {
		if strings.Contains(rr.Body.String(), secret) {
			t.Errorf("secret %q leaked into the assessment JSON", secret)
		}
	}

	var got report.Assessment
	if err = json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, rr.Body.String())
	}
	id := got.Identity
	if id.Name == "" || id.Version == "" || id.Platform == "" || id.GeneratedAt.IsZero() {
		t.Errorf("identity fields missing: %+v", id)
	}
	// The raw JSON must carry every identity key, including one whose value
	// is empty in an unstamped build (build).
	var raw struct {
		Identity map[string]json.RawMessage `json:"identity"`
	}
	if err = json.Unmarshal(rr.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"name", "version", "build", "platform", "generated_at"} {
		if _, ok := raw.Identity[k]; !ok {
			t.Errorf("identity.%s missing from JSON", k)
		}
	}

	// Rows are the PDF's assessment table, in order.
	cur, err := h.currentConfig()
	if err != nil {
		t.Fatal(err)
	}
	rep := report.Collect(context.Background(), cur, reportEnvironment(configPath, cur.DataDir(), testNow))
	var want [][]string
	for _, s := range rep.Sections {
		if s.Title == "Security assessment" {
			want = s.Tables[0].Rows
		}
	}
	if len(want) == 0 || len(got.Assessment) != len(want) {
		t.Fatalf("got %d rows, PDF table has %d", len(got.Assessment), len(want))
	}
	for i, w := range want {
		g := got.Assessment[i]
		if g.Item != w[1] || g.Status != w[2] || g.Action != (w[0] == "*") {
			t.Errorf("row %d = %+v, want %q", i, g, w)
		}
	}
	// The chat token row names the field as set, never by value.
	found := false
	for _, r := range got.Assessment {
		if r.Item == "WebUI chat token" {
			found = true
			if !strings.Contains(r.Status, "Token set") {
				t.Errorf("chat token row = %q", r.Status)
			}
		}
	}
	if !found {
		t.Error("no WebUI chat token row")
	}
}

// TestReportEnvironment: process facts are filled, never left empty.
func TestReportEnvironment(t *testing.T) {
	env := reportEnvironment("/tmp/config.json", "/tmp/data", testNow)
	for name, v := range map[string]string{
		"User": env.User, "Group": env.Group, "Hostname": env.Hostname,
		"Executable": env.Executable, "Version": env.Version, "OS": env.OS, "Arch": env.Arch,
	} {
		if v == "" {
			t.Errorf("%s is empty", name)
		}
	}
	if env.ConfigPath != "/tmp/config.json" || env.DataDir != "/tmp/data" || !env.Now.Equal(testNow) {
		t.Errorf("environment did not carry the caller's values: %+v", env)
	}
}

// TestReportAssessment_EncryptedMatrixRooms: rooms the Matrix channel saw to
// be encrypted reach the Check Up table through the API, naming the room, and
// the row is absent when there are none.
func TestReportAssessment_EncryptedMatrixRooms(t *testing.T) {
	configPath := setupTestEnv(t)
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Channels.Matrix.Enabled = true
	cfg.Channels.Matrix.Homeserver = "https://matrix.example.org"
	cfg.Channels.Matrix.UserID = "@alice:example.org"
	cfg.Channels.Matrix.AccessToken = "fixture-token"
	if err = config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		name  string
		rooms []string
	}{
		{"one encrypted room", []string{`"Bob's room" (!abc:example.org)`}},
		{"none", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHandler(configPath)
			h.encryptedMatrixRooms = func() []string { return tt.rooms }
			mux := http.NewServeMux()
			h.RegisterRoutes(mux)
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/report/assessment", nil))
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
			}
			var got report.Assessment
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			var row *report.AssessmentRow
			for i := range got.Assessment {
				if got.Assessment[i].Item == "Encrypted Matrix rooms" {
					row = &got.Assessment[i]
				}
			}
			if len(tt.rooms) == 0 {
				if row != nil {
					t.Fatalf("unexpected row %+v", *row)
				}
				return
			}
			if row == nil {
				t.Fatal("no Encrypted Matrix rooms row")
			}
			if !strings.Contains(row.Status, tt.rooms[0]) || !row.Action {
				t.Errorf("row = %+v, want the room named and marked for action", *row)
			}
		})
	}
}
